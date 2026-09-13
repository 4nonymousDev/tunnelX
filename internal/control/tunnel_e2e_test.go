package control

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/proto"
	"tunnelx/internal/tunnel"
)

// TestEndToEndTunnel 验证完整数据通路：
//
//	回声服务 ← Exporter(-R) ← 服务端 ← Importer(-L) ← 测试客户端
//
// 这是对整个架构的实证：Exporter 把本地服务推到服务端并上报，Importer 从注册表
// 查出端口并拉到本地，两侧全程只用一条 SSH 连接。
// TestEndToEndTunnel verifies the complete path through Exporter, server, and Importer,
// with each side using one SSH connection.
// TestEndToEndTunnel verifies the complete path through Exporter, server, and Importer,
// with each side using one SSH connection.
func TestEndToEndTunnel(t *testing.T) {
	addr, keyPath := startTestServer(t)

	// 1. 起一个回声服务，充当"内网里被访问的服务"。
	// 1. Start an echo service representing the private-network target.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动回声服务: %v", err)
	}
	defer echoLn.Close()
	go serveEcho(echoLn)
	echoPort := echoLn.Addr().(*net.TCPAddr).Port

	// 2. Exporter：把回声服务推到服务端。
	// 2. Export the echo service to the server.
	expSSH := dialSSH(t, addr, keyPath)
	expCtrl := dialClientOnSSH(t, expSSH, "peer-uuid", "办公室PC", proto.RoleExporter)

	expLog := logbuf.New(nil)
	t.Cleanup(func() { dumpLog(t, "exporter", expLog) })
	expTun := tunnel.New(config.Tunnel{
		Kind:      config.KindExport,
		Name:      "Echo",
		Enabled:   true,
		LocalHost: "127.0.0.1",
		LocalPort: echoPort,
	}, expLog, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	expTun.Start(ctx, expSSH, expCtrl, expCtrl)
	defer expTun.Stop()

	waitState(t, expTun, tunnel.StateRunning)

	// 3. Importer：从注册表查出对端端口，拉到本地。
	// 3. Resolve the peer port and import it locally.
	impSSH := dialSSH(t, addr, keyPath)
	impCtrl := dialClientOnSSH(t, impSSH, "importer-uuid", "笔记本", proto.RoleImporter)
	waitRegistry(t, impCtrl, 1)

	listenPort := freePort(t)
	impLog := logbuf.New(nil)
	t.Cleanup(func() { dumpLog(t, "importer", impLog) })
	impTun := tunnel.New(config.Tunnel{
		Kind:        config.KindImport,
		Name:        "Echo接入",
		Enabled:     true,
		PeerID:      "peer-uuid",
		PeerName:    "办公室PC",
		PeerSrcPort: echoPort,
		ListenPort:  listenPort,
	}, impLog, nil)

	impTun.Start(ctx, impSSH, impCtrl, impCtrl)
	defer impTun.Stop()

	waitState(t, impTun, tunnel.StateRunning)

	// 4. 通过 Importer 的本地端口访问，数据应原样回来。
	// 4. Access the local import and expect an exact echo.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), 5*time.Second)
	if err != nil {
		t.Fatalf("连接本地转发端口: %v", err)
	}
	defer conn.Close()

	want := "hello through the tunnel"
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte(want)); err != nil {
		t.Fatalf("写入: %v", err)
	}

	buf := make([]byte, len(want))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读取: %v", err)
	}

	if got := string(buf); got != want {
		t.Errorf("经隧道往返得到 %q, 期望 %q", got, want)
	}

	// 再往返一次：验证连接在首轮之后仍然可用。
	// 若 pipe 在单方向结束时就拆连接，这一步会失败。
	// A second round trip proves one-way completion does not tear down the connection.
	const second = "second round"
	if _, err := conn.Write([]byte(second)); err != nil {
		t.Fatalf("二次写入: %v", err)
	}
	buf2 := make([]byte, len(second))
	if _, err := io.ReadFull(conn, buf2); err != nil {
		t.Fatalf("二次读取: %v", err)
	}
	if got := string(buf2); got != second {
		t.Errorf("二次往返得到 %q, 期望 %q", got, second)
	}
}

// TestImportPeerOffline 验证对端不在线时隧道进入"对端离线"状态而非报错。
// 这类失败是可自愈的——对端开机即恢复。
// TestImportPeerOffline verifies the recoverable peer-offline state rather than a terminal error.
func TestImportPeerOffline(t *testing.T) {
	addr, keyPath := startTestServer(t)

	impSSH := dialSSH(t, addr, keyPath)
	impCtrl := dialClient(t, addr, keyPath, "importer-uuid", "笔记本", proto.RoleImporter)

	impTun := tunnel.New(config.Tunnel{
		Kind:        config.KindImport,
		Name:        "接入不存在的对端",
		Enabled:     true,
		PeerID:      "nobody-home",
		PeerSrcPort: 80,
		ListenPort:  freePort(t),
	}, logbuf.New(nil), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	impTun.Start(ctx, impSSH, impCtrl, impCtrl)
	defer impTun.Stop()

	// 对端离线后会进入退避重试，故最终状态可能是 PeerOffline 或 Reconnecting，
	// 两者都表示"在等对端回来"，关键是不能是 Error。
	// PeerOffline or Reconnecting both mean waiting; Error would be wrong.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := impTun.Status().State
		if st == tunnel.StatePeerOffline || st == tunnel.StateReconnecting {
			// 符合预期。
			// Expected state.
			return
		}
		if st == tunnel.StateError {
			t.Fatalf("对端离线被误判为不可自愈错误: %s", impTun.Status().Reason)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("隧道未进入预期状态，当前 %s", impTun.Status().State)
}

// TestWrongPeerIDIsDiagnosable 验证 peer_id 填错时日志能指出实际可选项。
// 配错 peer_id 与对端确实未上线的症状完全相同（都是"对端不在线"），
// 若不列出注册表内容，用户无从判断该改配置还是该去开对端机器。
// TestWrongPeerIDIsDiagnosable lists actual peers to distinguish a typo from an offline peer.
func TestWrongPeerIDIsDiagnosable(t *testing.T) {
	addr, keyPath := startTestServer(t)

	// Exporter 正常上线并上报。
	// Bring the Exporter online and publish normally.
	expSSH := dialSSH(t, addr, keyPath)
	expCtrl := dialClientOnSSH(t, expSSH, "correct-peer-id", "办公室PC", proto.RoleExporter)

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动回声服务: %v", err)
	}
	defer echoLn.Close()
	go serveEcho(echoLn)
	echoPort := echoLn.Addr().(*net.TCPAddr).Port

	expTun := tunnel.New(config.Tunnel{
		Kind: config.KindExport, Name: "nginx", Enabled: true,
		LocalHost: "127.0.0.1", LocalPort: echoPort,
	}, logbuf.New(nil), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	expTun.Start(ctx, expSSH, expCtrl, expCtrl)
	defer expTun.Stop()
	waitState(t, expTun, tunnel.StateRunning)

	// Importer 用错误的 peer_id。
	// Configure the Importer with an incorrect peer_id.
	impSSH := dialSSH(t, addr, keyPath)
	impCtrl := dialClientOnSSH(t, impSSH, "importer-id", "笔记本", proto.RoleImporter)
	waitRegistry(t, impCtrl, 1)

	impLog := logbuf.New(nil)
	impTun := tunnel.New(config.Tunnel{
		Kind: config.KindImport, Name: "接入nginx", Enabled: true,
		PeerID: "wrong-peer-id", // ← 填错 / Intentionally incorrect.
		// Intentionally incorrect.
		PeerSrcPort: echoPort,
		ListenPort:  freePort(t),
	}, impLog, nil)

	impTun.Start(ctx, impSSH, impCtrl, impCtrl)
	defer impTun.Stop()

	// 等待解析失败并输出诊断。
	// Wait for resolution failure and diagnostics.
	deadline := time.Now().Add(5 * time.Second)
	var logText string
	for time.Now().Before(deadline) {
		var sb []string
		for _, e := range impLog.Snapshot(logbuf.Debug) {
			sb = append(sb, e.Msg)
		}
		logText = strings.Join(sb, "\n")
		if strings.Contains(logText, "注册表当前可选对端") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("Importer 日志:\n%s", logText)

	// 日志须同时给出"我在找什么"与"实际有什么"。
	// Logs must show both the requested and available peers.
	if !strings.Contains(logText, "wrong-peer-id") {
		t.Error("日志未显示本机的查找条件（peer_id）")
	}
	if !strings.Contains(logText, "correct-peer-id") {
		t.Error("日志未列出注册表中实际可用的 peer_id")
	}
	if !strings.Contains(logText, "办公室PC") {
		t.Error("日志未列出可用对端的名称")
	}
}

func dialSSH(t *testing.T, addr, keyPath string) *ssh.Client {
	t.Helper()

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取密钥: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		t.Fatalf("解析密钥: %v", err)
	}

	c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("SSH 连接失败: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func dialClientOnSSH(t *testing.T, client *ssh.Client, id, name, role string) *Client {
	t.Helper()
	cfg := &config.Config{ID: id, Name: name}
	c, err := Dial(client, cfg, role, "test", logbuf.New(nil))
	if err != nil {
		t.Fatalf("控制连接失败: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func serveEcho(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			io.Copy(conn, conn)
		}()
	}
}

func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测可用端口: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitState(t *testing.T, tun *tunnel.Tunnel, want tunnel.State) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := tun.Status()
		if st.State == want {
			return
		}
		if st.State == tunnel.StateError {
			t.Fatalf("隧道进入错误状态: %s", st.Reason)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("隧道未在超时内进入 %s，当前 %s", want, tun.Status().State)
}

func dumpLog(t *testing.T, tag string, buf *logbuf.Buffer) {
	t.Helper()
	for _, e := range buf.Snapshot(logbuf.Debug) {
		t.Logf("[%s/%s] %s: %s", tag, e.Level, e.Source, e.Msg)
	}
}
