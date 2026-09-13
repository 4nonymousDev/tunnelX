package control

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/proto"
	"tunnelx/internal/server"
)

// startTestServer 起一个真实的 tunnel-server，返回其地址与客户端私钥路径。
// 用真服务端而非 mock：本测试要验证的正是两端协议实现是否一致，
// mock 掉一边就失去了意义。
// startTestServer starts a real tunnel-server and returns its address and client key;
// a mock would not verify protocol compatibility between both implementations.
// startTestServer starts a real tunnel-server and returns its address and client key;
// a mock would not verify protocol compatibility between both implementations.
func startTestServer(t *testing.T) (addr, keyPath string) {
	t.Helper()

	dir := t.TempDir()
	hostKey := filepath.Join(dir, "host_key")
	clientKey := filepath.Join(dir, "client_key")
	authKeys := filepath.Join(dir, "authorized_keys")

	writeKey(t, hostKey)
	pub := writeKey(t, clientKey)
	if err := os.WriteFile(authKeys, pub, 0o600); err != nil {
		t.Fatalf("写入 authorized_keys: %v", err)
	}

	// 端口 0 让系统分配，避免测试间抢占固定端口。
	// Port 0 lets the OS allocate a port and avoids conflicts between tests.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测可用端口: %v", err)
	}
	addr = ln.Addr().String()
	ln.Close()

	srv, err := server.New(server.Config{
		Addr:           addr,
		HostKeyPath:    hostKey,
		AuthorizedKeys: authKeys,
		Version:        "test",
	}, func(format string, args ...any) { t.Logf("[server] "+format, args...) })
	if err != nil {
		t.Fatalf("创建服务端: %v", err)
	}

	go srv.ListenAndServe()
	t.Cleanup(func() { srv.Close() })

	waitPort(t, addr)
	return addr, clientKey
}

// writeKey 生成一对 ed25519 密钥，写入私钥并返回 authorized_keys 格式的公钥。
// writeKey generates an ed25519 pair, writes the private key, and returns the authorized_keys line.
func writeKey(t *testing.T, path string) []byte {
	t.Helper()

	priv, pubLine := genEd25519(t)
	if err := os.WriteFile(path, priv, 0o600); err != nil {
		t.Fatalf("写入密钥 %s: %v", path, err)
	}
	return pubLine
}

func waitPort(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("服务端未在超时内就绪: %s", addr)
}

// dialClient 建立 SSH 连接并打开控制通道。
// dialClient establishes SSH and opens the control channel.
func dialClient(t *testing.T, addr, keyPath, id, name, role string) *Client {
	t.Helper()

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取客户端密钥: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		t.Fatalf("解析客户端密钥: %v", err)
	}

	sshClient, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 测试环境，主机密钥每次新生成 / Test host keys are regenerated each time.
		// Test host keys are regenerated on every run.
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("SSH 连接失败: %v", err)
	}
	t.Cleanup(func() { sshClient.Close() })

	c, err := Dial(sshClient, &config.Config{ID: id, Name: name},
		role, "test", logbuf.New(nil))
	if err != nil {
		t.Fatalf("打开控制通道失败: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestHandshakeAndPublish 验证握手、上报、注册表推送的最小闭环。
// TestHandshakeAndPublish verifies the minimal handshake, publish, and registry-push loop.
func TestHandshakeAndPublish(t *testing.T) {
	addr, keyPath := startTestServer(t)

	exporter := dialClient(t, addr, keyPath, "exporter-uuid", "办公室PC", proto.RoleExporter)

	exporter.Track("web", "127.0.0.1", 80, 18080, "Web服务")
	exporter.Track("ssh", "127.0.0.1", 22, 18022, "")
	exporter.Publish()

	// 上报后服务端会广播新快照，客户端自身也会收到。
	// Publishing broadcasts the new snapshot back to the publisher too.
	entries := waitRegistry(t, exporter, 2)

	byPort := map[int]proto.RegistryEntry{}
	for _, e := range entries {
		byPort[e.SrcPort] = e
	}

	web, ok := byPort[80]
	if !ok {
		t.Fatalf("注册表中缺少源端口 80 的条目: %+v", entries)
	}
	if web.RemotePort != 18080 {
		t.Errorf("服务端端口 = %d, 期望 18080", web.RemotePort)
	}
	if web.Name != "办公室PC" {
		t.Errorf("机器名 = %q, 期望 办公室PC", web.Name)
	}
	if web.TunnelName != "Web服务" {
		t.Errorf("隧道名 = %q, 期望 Web服务", web.TunnelName)
	}
	if web.ID != "exporter-uuid" {
		t.Errorf("客户端标识 = %q, 期望 exporter-uuid", web.ID)
	}
	if web.TunnelID != "web" || web.SrcHost != "127.0.0.1" {
		t.Errorf("隧道身份/目标未透传: %+v", web)
	}
}

// TestResolveRemotePort 验证 Importer 按「对端身份 + 隧道 ID」解析服务端端口。
// 服务端端口是动态分配的，重连后会变，因此不能按端口号记忆，只能按身份匹配。
// TestResolveRemotePort verifies identity-based lookup because dynamic ports change on reconnect.
// TestResolveRemotePort verifies lookup by peer identity and tunnel ID because dynamic ports change on reconnect.
func TestResolveRemotePort(t *testing.T) {
	addr, keyPath := startTestServer(t)

	exporter := dialClient(t, addr, keyPath, "peer-uuid", "办公室PC", proto.RoleExporter)
	exporter.Track("web", "127.0.0.1", 80, 18080, "Web")
	exporter.Publish()

	importer := dialClient(t, addr, keyPath, "importer-uuid", "笔记本", proto.RoleImporter)
	waitRegistry(t, importer, 1)

	port, ok := importer.ResolveRemotePort("peer-uuid", "web", 80)
	if !ok {
		t.Fatal("未能解析出对端端口")
	}
	if port != 18080 {
		t.Errorf("解析得到端口 %d, 期望 18080", port)
	}

	// 对端不存在时须报告 not ok，供隧道进入"对端离线"状态。
	// A missing peer must report not-ok so the tunnel enters Peer Offline state.
	if _, ok := importer.ResolveRemotePort("nonexistent", "web", 80); ok {
		t.Error("对不存在的对端应返回 ok=false")
	}
	if _, ok := importer.ResolveRemotePort("peer-uuid", "nonexistent", 9999); ok {
		t.Error("对不存在的源端口应返回 ok=false")
	}
}

func TestPublishAndResolveSamePortByTunnelID(t *testing.T) {
	addr, keyPath := startTestServer(t)

	exporter := dialClient(t, addr, keyPath, "peer-uuid", "办公室PC", proto.RoleExporter)
	exporter.Track("local-web", "127.0.0.1", 80, 18080, "本机 Web")
	exporter.Track("file-server", "192.0.2.10", 80, 28080, "文件服务")
	exporter.Publish()

	importer := dialClient(t, addr, keyPath, "importer-uuid", "笔记本", proto.RoleImporter)
	entries := waitRegistry(t, importer, 2)
	if len(entries) != 2 {
		t.Fatalf("相同源端口应保留两条注册记录: %+v", entries)
	}

	if port, ok := importer.ResolveRemotePort("peer-uuid", "local-web", 80); !ok || port != 18080 {
		t.Fatalf("本机 Web 解析 = %d, %v，期望 18080, true", port, ok)
	}
	if port, ok := importer.ResolveRemotePort("peer-uuid", "file-server", 80); !ok || port != 28080 {
		t.Fatalf("文件服务解析 = %d, %v，期望 28080, true", port, ok)
	}

	// 没有 peer_tunnel_id 的旧配置仍按源端口工作。
	// Legacy configurations without peer_tunnel_id still resolve by source port.
	if port, ok := importer.ResolveRemotePort("peer-uuid", "", 80); !ok ||
		(port != 18080 && port != 28080) {
		t.Fatalf("旧配置回退解析 = %d, %v", port, ok)
	}

	exporter.Untrack("local-web")
	exporter.Publish()
	wantRemaining := waitRegistry(t, importer, 1)
	if wantRemaining[0].TunnelID != "file-server" {
		t.Fatalf("注销一条隧道误删了另一条同端口隧道: %+v", wantRemaining)
	}
	if _, ok := importer.ResolveRemotePort("peer-uuid", "local-web", 80); ok {
		t.Fatal("已注销的隧道仍能解析")
	}
	if port, ok := importer.ResolveRemotePort("peer-uuid", "file-server", 80); !ok || port != 28080 {
		t.Fatalf("剩余同端口隧道解析 = %d, %v，期望 28080, true", port, ok)
	}
}

// TestDuplicateID 验证同一 UUID 重复注册被拒，且错误码为不可自愈。
// 静默覆盖会让先连的那个"莫名其妙消失"，排查极其困难。
// TestDuplicateID verifies duplicate UUID rejection instead of silently replacing the first client.
// TestDuplicateID verifies that duplicate UUID registration is rejected as nonrecoverable.
func TestDuplicateID(t *testing.T) {
	addr, keyPath := startTestServer(t)

	dialClient(t, addr, keyPath, "same-uuid", "机器A", proto.RoleExporter)

	// 第二个客户端用同一 UUID，应被拒绝。
	// A second client using the same UUID must be rejected.
	keyData, _ := os.ReadFile(keyPath)
	signer, _ := ssh.ParsePrivateKey(keyData)
	sshClient, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("SSH 连接失败: %v", err)
	}
	defer sshClient.Close()

	_, err = Dial(sshClient, &config.Config{ID: "same-uuid", Name: "机器B"},
		proto.RoleExporter, "test", logbuf.New(nil))
	if err == nil {
		t.Fatal("重复 UUID 应被拒绝，但握手成功了")
	}

	perr, ok := err.(*proto.Error)
	if !ok {
		t.Fatalf("错误类型 = %T (%v), 期望 *proto.Error", err, err)
	}
	if perr.Code != proto.CodeDuplicateID {
		t.Errorf("错误码 = %q, 期望 %q", perr.Code, proto.CodeDuplicateID)
	}
	if proto.Retryable(perr.Code) {
		t.Error("duplicate_id 应归为不可自愈，不应重试")
	}
}

// TestPeerOfflineRemovesEntry 验证 Exporter 断开后其条目从注册表中消失。
// 这是对端离线通知的基础：Importer 收到新快照后自行比对，发现对端已不在。
// TestPeerOfflineRemovesEntry verifies removal on disconnect so Importers detect offline peers.
// TestPeerOfflineRemovesEntry verifies removal on disconnect, enabling Importers to detect offline peers.
func TestPeerOfflineRemovesEntry(t *testing.T) {
	addr, keyPath := startTestServer(t)

	importer := dialClient(t, addr, keyPath, "importer-uuid", "笔记本", proto.RoleImporter)

	keyData, _ := os.ReadFile(keyPath)
	signer, _ := ssh.ParsePrivateKey(keyData)
	peerConn, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("对端 SSH 连接失败: %v", err)
	}

	peer, err := Dial(peerConn, &config.Config{ID: "peer-uuid", Name: "办公室PC"},
		proto.RoleExporter, "test", logbuf.New(nil))
	if err != nil {
		t.Fatalf("对端控制通道失败: %v", err)
	}
	peer.Track("web", "127.0.0.1", 80, 18080, "Web")
	peer.Publish()

	waitRegistry(t, importer, 1)
	if _, ok := importer.ResolveRemotePort("peer-uuid", "web", 80); !ok {
		t.Fatal("对端上线后应能解析到端口")
	}

	// 对端下线。
	// Take the peer offline.
	peer.Close()
	peerConn.Close()

	waitRegistry(t, importer, 0)
	if _, ok := importer.ResolveRemotePort("peer-uuid", "web", 80); ok {
		t.Error("对端下线后不应再解析到端口")
	}
}

// waitRegistry 等待注册表达到期望条目数。
// waitRegistry waits for the expected registry size.
// waitRegistry waits for the expected number of registry entries.
func waitRegistry(t *testing.T, c *Client, want int) []proto.RegistryEntry {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var last []proto.RegistryEntry
	for time.Now().Before(deadline) {
		last = c.Registry()
		if len(last) == want {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("注册表未在超时内达到 %d 条，当前 %d 条: %+v", want, len(last), last)
	return nil
}

// genEd25519 生成一对 ed25519 密钥，返回 PEM 私钥与 authorized_keys 格式公钥。
// genEd25519 returns a PEM private key and authorized_keys public key.
func genEd25519(t *testing.T) (privPEM, pubLine []byte) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("编码私钥: %v", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("编码公钥: %v", err)
	}

	return pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(sshPub)
}
