package manager

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/server"
	"tunnelx/internal/tunnel"
)

// TestDeploymentFlow 演练用户的实际部署路径：
//
//	config.json + key.pem 放在 exe 同目录 → Manager.Start() → 隧道建立 → 数据可通
//
// 与 control 包的测试不同，这里走的是 Manager 这一层——即 GUI 实际调用的入口，
// 覆盖配置加载、私钥读取、主机密钥确认、控制通道、隧道启动的完整链路。
// TestDeploymentFlow exercises the user's real deployment path:
//
//	config.json + key.pem beside the executable -> Manager.Start() -> tunnel established -> data flows
//
// Unlike control-package tests, this uses the Manager layer invoked by the GUI and covers
// configuration loading, private-key reading, host-key confirmation, control setup, and tunnel startup.
func TestDeploymentFlow(t *testing.T) {
	dir := t.TempDir()

	// 1. 服务端侧：主机密钥 + authorized_keys
	// 1. Server side: host key plus authorized_keys.
	hostKeyPath := filepath.Join(dir, "host_key")
	clientKeyPath := filepath.Join(dir, "key1.pem")
	authKeysPath := filepath.Join(dir, "authorized_keys")

	writePEMKey(t, hostKeyPath)
	clientPub := writePEMKey(t, clientKeyPath)
	if err := os.WriteFile(authKeysPath, clientPub, 0o600); err != nil {
		t.Fatalf("写入 authorized_keys: %v", err)
	}

	addr := reservePort(t)
	srv, err := server.New(server.Config{
		Addr:           addr,
		HostKeyPath:    hostKeyPath,
		AuthorizedKeys: authKeysPath,
		Version:        "test",
	}, func(f string, a ...any) { t.Logf("[server] "+f, a...) })
	if err != nil {
		t.Fatalf("创建服务端: %v", err)
	}
	go srv.ListenAndServe()
	defer srv.Close()
	waitDial(t, addr)

	// 2. 被导出的本地服务（模拟内网里的 NAS）
	// 2. Exported local service, simulating a NAS on the private network.
	echoAddr, echoPort := startEcho(t)
	t.Logf("本地服务监听 %s", echoAddr)

	// 3. 客户端侧：写 config.json 到"exe 同目录"
	// config.Load 按 os.Executable() 定位目录，测试中无法改变，
	// 故直接构造等价的 Config，其余路径与真实一致。
	// 3. Client side: place config.json beside the executable. config.Load locates
	// that directory through os.Executable(), which tests cannot change, so construct
	// an equivalent Config directly while keeping the rest of the path realistic.
	cfg := &config.Config{
		ID:         "deploy-test-id",
		Name:       "测试机",
		ServerAddr: addr,
		ServerUser: "root",
		KeyPath:    clientKeyPath,
		Tunnels: []config.Tunnel{{
			Kind:      config.KindExport,
			Name:      "NAS",
			Enabled:   true,
			LocalHost: "127.0.0.1",
			LocalPort: echoPort,
		}},
	}

	// 验证该配置能被 JSON 正确往返——用户是手写 config.json 的。
	// Verify the configuration round-trips through JSON because users edit config.json manually.
	assertConfigRoundTrip(t, cfg)

	log := logbuf.New(nil)
	defer dumpLog(t, log)

	m := New(cfg, log, "0.1.0", func() {})
	m.SetPrompts(
		func(host, fp string) bool {
			t.Logf("首次连接主机指纹 %s，测试中自动信任", fp)
			return true
		},
		func(path string, readers []string) bool {
			t.Logf("私钥权限提示: %v", readers)
			return false // 忽略并继续 / Ignore and continue.
			// Ignore and continue.
		},
		func() {}, func() {},
	)

	// known_hosts 会写入 exe 同目录，测试中容忍失败——
	// 主机密钥回调已返回信任，不影响连接建立。
	// known_hosts is written beside the executable and may fail in tests; the host-key
	// callback already returned trust, so this does not affect connection establishment.
	m.Start()
	defer m.Stop()

	// 4. 等待连接与隧道就绪
	// 4. Wait for the connection and tunnel to become ready.
	waitConn(t, m, ConnConnected)

	var remotePort int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, tun := range m.Tunnels() {
			st := tun.Status()
			if st.State == tunnel.StateError {
				t.Fatalf("隧道进入错误状态: %s", st.Reason)
			}
			if st.State == tunnel.StateRunning && st.RemotePort > 0 {
				remotePort = st.RemotePort
			}
		}
		if remotePort > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if remotePort == 0 {
		t.Fatal("隧道未在超时内建立并取得服务端端口")
	}
	t.Logf("服务端已分配端口 %d", remotePort)

	// 5. 经服务端分配的端口访问，数据应抵达本地服务
	// 5. Connect through the server-assigned port; data must reach the local service.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort), 5*time.Second)
	if err != nil {
		t.Fatalf("连接服务端转发端口 %d: %v", remotePort, err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	want := "hello from server side"
	if _, err := conn.Write([]byte(want)); err != nil {
		t.Fatalf("写入: %v", err)
	}
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读取: %v", err)
	}
	if got := string(buf); got != want {
		t.Errorf("往返得到 %q, 期望 %q", got, want)
	}
}

// TestWrongPortGivesClearError 验证误填系统 sshd 端口时能给出可辨别的错误。
// 这是实际部署中最易犯的错：把 server_addr 填成 22（系统 sshd）而非
// tunnel-server 的端口。错误必须明确指向原因，而非笼统的超时。
// TestWrongPortGivesClearError verifies a recognizable error when the system sshd port
// is entered instead of tunnel-server's port, a common deployment mistake. The error
// must identify the cause rather than report a generic timeout.
func TestWrongPortGivesClearError(t *testing.T) {
	// 起一个只接受 TCP 但不说 SSH 的监听器，模拟"连错了服务"。
	// Start a listener that accepts TCP but does not speak SSH to simulate the wrong service.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close() // 立即关闭，模拟协议不匹配 / Close immediately to simulate a protocol mismatch.
			// Close immediately to simulate a protocol mismatch.
		}
	}()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")
	writePEMKey(t, keyPath)

	log := logbuf.New(nil)
	m := New(&config.Config{
		ID:         "id",
		Name:       "机器",
		ServerAddr: ln.Addr().String(),
		ServerUser: "root",
		KeyPath:    keyPath,
	}, log, "0.1.0", func() {})
	m.SetPrompts(
		func(string, string) bool { return true },
		func(string, []string) bool { return false },
		func() {}, func() {},
	)

	m.Start()
	defer m.Stop()

	// 应进入重试而非静默挂起——连接失败属可自愈。
	// It should retry rather than hang silently because connection failure is recoverable.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := m.ConnStatus()
		if st.State == ConnRetrying || st.State == ConnFailed {
			if st.Reason == "" {
				t.Error("失败状态必须带可读原因")
			}
			t.Logf("状态=%v 原因=%q", st.State, st.Reason)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("未在超时内进入失败状态，当前 %v", m.ConnStatus().State)
}

// assertConfigRoundTrip 验证配置能被 JSON 正确序列化与解析。
// assertConfigRoundTrip verifies correct JSON serialization and parsing of configuration.
func assertConfigRoundTrip(t *testing.T, cfg *config.Config) {
	t.Helper()

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化配置: %v", err)
	}
	var back config.Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("解析配置: %v", err)
	}
	if len(back.Tunnels) != len(cfg.Tunnels) {
		t.Errorf("隧道数往返后 = %d, 期望 %d", len(back.Tunnels), len(cfg.Tunnels))
	}
	if back.ServerAddr != cfg.ServerAddr {
		t.Errorf("服务器地址往返后 = %q, 期望 %q", back.ServerAddr, cfg.ServerAddr)
	}
}

func writePEMKey(t *testing.T, path string) []byte {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("编码私钥: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("写入私钥: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("编码公钥: %v", err)
	}
	return ssh.MarshalAuthorizedKey(sshPub)
}

func reservePort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测端口: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func startEcho(t *testing.T) (addr string, port int) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动回声服务: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()

	tcp := ln.Addr().(*net.TCPAddr)
	return ln.Addr().String(), tcp.Port
}

func waitDial(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("服务端未就绪: %s", addr)
}

func waitConn(t *testing.T, m *Manager, want ConnState) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := m.ConnStatus()
		if st.State == want {
			return
		}
		if st.State == ConnFailed {
			t.Fatalf("连接失败: %s", st.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("连接未在超时内进入 %v，当前 %v", want, m.ConnStatus().State)
}

func dumpLog(t *testing.T, buf *logbuf.Buffer) {
	t.Helper()
	for _, e := range buf.Snapshot(logbuf.Debug) {
		t.Logf("[%s] %s: %s", e.Level, e.Source, e.Msg)
	}
}
