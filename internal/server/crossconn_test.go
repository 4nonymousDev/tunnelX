package server

import (
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/proto"
)

// TestCrossConnectionForward 复现真实拓扑：Exporter 与 Importer 是**两个独立的
// SSH 连接**（两台机器上的两个进程）。
// 既有的 e2e 测试虽然也建了两条连接，但都在同一进程内，且转发端口的分配与访问
// 时序被测试代码串起来了。此处严格模拟：
//
//	连接A（Exporter）请求 -R，服务端分配端口 P 并在 127.0.0.1:P 监听
//	连接B（Importer）请求 direct-tcpip 连往 127.0.0.1:P
//	→ 服务端须把 B 的数据经由 A 送到 A 那侧的本地服务
//
// 关键在于：服务端收到 B 的 direct-tcpip 后，会去 Dial 自己的 127.0.0.1:P，
// 而那个监听属于连接 A——数据须跨连接流转。
// TestCrossConnectionForward models separate Exporter and Importer SSH connections:
// B reaches a loopback port owned by A and the server must route data across connections.
func TestCrossConnectionForward(t *testing.T) {
	addr, keyPath := startServer(t)

	// Exporter 那侧的本地服务（相当于 nginx）
	// The Exporter's local service, equivalent to nginx.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动本地服务: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				// 模拟 HTTP：收到任意请求就回一段固定内容
				// Simulate HTTP by returning fixed content for any request.
				buf := make([]byte, 1024)
				n, _ := c.Read(buf)
				fmt.Fprintf(c, "OK:%s", string(buf[:n]))
			}()
		}
	}()
	localAddr := echoLn.Addr().String()

	// ---- 连接 A：Exporter ----
	// ---- Connection A: Exporter ----
	expClient := dialWith(t, addr, keyPath)
	expPC, expCtrl := openTestControl(t, expClient, proto.RoleExporter, "cross-exporter")
	defer expCtrl.Close()

	remoteLn, err := expClient.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("请求远程转发: %v", err)
	}
	defer remoteLn.Close()

	remotePort := remoteLn.Addr().(*net.TCPAddr).Port
	t.Logf("服务端分配转发端口: %d", remotePort)
	publishTestPort(t, expPC, remotePort)

	// Exporter 侧：接受回送的连接并转给本地服务
	// Exporter side: accept returned connections and proxy them to the local service.
	go func() {
		for {
			in, err := remoteLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer in.Close()
				out, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
				if err != nil {
					t.Logf("Exporter 连本地服务失败: %v", err)
					return
				}
				defer out.Close()

				done := make(chan struct{}, 2)
				go func() { io.Copy(out, in); done <- struct{}{} }()
				go func() { io.Copy(in, out); done <- struct{}{} }()
				<-done
			}()
		}
	}()

	// ---- 连接 B：Importer（独立的 SSH 连接）----
	// ---- Connection B: Importer on an independent SSH connection ----
	impClient := dialWith(t, addr, keyPath)
	_, impCtrl := openTestControl(t, impClient, proto.RoleImporter, "cross-importer")
	defer impCtrl.Close()

	conn, err := impClient.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
	if err != nil {
		t.Fatalf("Importer 经服务端连接转发端口失败: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))

	const req = "GET / HTTP/1.0\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("写入: %v", err)
	}

	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("读取失败（数据未能跨连接流转）: %v", err)
	}

	got := string(buf[:n])
	t.Logf("经隧道收到: %q", got)
	if len(got) == 0 {
		t.Fatal("收到空响应")
	}
}

func openTestControl(t *testing.T, client *ssh.Client, role, id string) (*proto.Conn, ssh.Channel) {
	t.Helper()
	ch, reqs, err := client.OpenChannel(proto.ChannelType, nil)
	if err != nil {
		t.Fatalf("打开控制通道: %v", err)
	}
	go ssh.DiscardRequests(reqs)
	pc := proto.NewConn(ch)
	if err = pc.Send(proto.Hello{V: proto.Version, Type: proto.TypeHello, Role: role, ID: id, Name: id, ClientVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	for {
		env, _, e := pc.Recv()
		if e != nil {
			t.Fatal(e)
		}
		if env.Type == proto.TypeHelloOK {
			break
		}
	}
	return pc, ch
}
func publishTestPort(t *testing.T, pc *proto.Conn, port int) {
	t.Helper()
	if err := pc.Send(proto.Publish{V: proto.Version, Type: proto.TypePublish, Tunnels: []proto.TunnelSpec{{TunnelID: "test", Name: "test", SrcPort: 1, RemotePort: port}}}); err != nil {
		t.Fatal(err)
	}
	for {
		env, _, e := pc.Recv()
		if e != nil {
			t.Fatal(e)
		}
		if env.Type == proto.TypeRegistry {
			continue
		}
		if env.Type != proto.TypePublishOK {
			t.Fatalf("publish response %s", env.Type)
		}
		return
	}
}

// TestForwardedTCPIPAddrMatching 验证服务端回送 forwarded-tcpip 时填写的地址
// 能被 Exporter 的 x/crypto/ssh 客户端匹配到已注册的监听。
// 匹配是按 "IP:端口" 字符串精确比对的（forwardList.forward）。若服务端回送的
// Addr 与客户端 Listen 时用的地址写法不一致（如 "localhost" vs "127.0.0.1"，
// 或 IPv4 与 IPv6 写法不同），OpenChannel 会被拒绝——症状是连接建立后**挂起**，
// 请求发出去却永远收不到响应，且两侧日志都没有明显错误。
// TestForwardedTCPIPAddrMatching verifies exact address matching between the
// server's forwarded-tcpip channel and the x/crypto/ssh registered listener.
func TestForwardedTCPIPAddrMatching(t *testing.T) {
	addr, keyPath := startServer(t)

	expClient := dialWith(t, addr, keyPath)

	// 与 tunnel.serveExport 完全一致的调用方式。
	// Invoke it exactly as tunnel.serveExport does.
	remoteLn, err := expClient.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("请求远程转发: %v", err)
	}
	defer remoteLn.Close()

	remotePort := remoteLn.Addr().(*net.TCPAddr).Port
	t.Logf("Exporter 注册的监听地址: %s", remoteLn.Addr())

	accepted := make(chan error, 1)
	go func() {
		c, err := remoteLn.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer c.Close()
		// 立即回一段数据，证明回送通道确实建立并可传输。
		// Reply immediately to prove the returned channel carries data.
		c.Write([]byte("PONG"))
		accepted <- nil
	}()

	// 直接从服务端本机连转发端口，模拟"服务端 curl 34707 能通"的场景。
	// Connect locally to the forwarded port, like running curl on the server.
	probe, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", remotePort), 5*time.Second)
	if err != nil {
		t.Fatalf("连接转发端口: %v", err)
	}
	defer probe.Close()

	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("Exporter 未能接受回送连接: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exporter 未在超时内收到回送连接——" +
			"服务端的 forwarded-tcpip 地址很可能与客户端注册的监听不匹配")
	}

	probe.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(probe, buf); err != nil {
		t.Fatalf("读取回送数据失败: %v", err)
	}
	if string(buf) != "PONG" {
		t.Errorf("收到 %q, 期望 PONG", buf)
	}
	t.Log("回送通道正常，地址匹配无误")
}

func dialWith(t *testing.T, addr, keyPath string) *ssh.Client {
	t.Helper()

	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取密钥: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(data)
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
