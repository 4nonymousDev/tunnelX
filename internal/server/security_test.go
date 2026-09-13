package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestNoShellAccess 验证隧道密钥无法取得 shell。
// 这是 DEPLOY.md 中权限隔离承诺的实证：即便持有合法的隧道密钥、
// 即便通过了认证，也拿不到 shell / exec / sftp——因为服务端根本没实现
// session channel，它落进 default 分支被直接拒绝。
// 若将来有人为了调试给服务端加上 session 支持，此测试会失败并提醒
// 该改动破坏了权限隔离。
// TestNoShellAccess proves the tunnel key cannot obtain shell, exec, or SFTP access;
// adding session-channel support would intentionally fail this security-boundary test.
func TestNoShellAccess(t *testing.T) {
	client := connectTestServer(t)

	// NewSession 会请求 "session" 类型的 channel。
	// NewSession requests a channel of type "session".
	sess, err := client.NewSession()
	if err == nil {
		sess.Close()
		t.Fatal("session channel 被接受了——隧道密钥不应能取得 shell")
	}

	// 错误应明确是"不支持的 channel 类型"，而非网络故障之类的偶然错误。
	// The failure must explicitly report an unsupported channel rather than a transient network error.
	if !strings.Contains(err.Error(), "unknown channel type") &&
		!strings.Contains(err.Error(), "不支持的 channel 类型") {
		t.Errorf("拒绝原因不符预期: %v", err)
	}
	t.Logf("session 已被拒绝: %v", err)
}

// TestNoArbitraryForwardTarget 验证服务端不会沦为任意目标的跳板。
// direct-tcpip 只允许连往 127.0.0.1；否则持有隧道密钥的人即可借服务端
// 访问其所在内网的任意主机（open relay）。
// TestNoArbitraryForwardTarget proves direct-tcpip cannot turn the server into an open relay.
func TestNoArbitraryForwardTarget(t *testing.T) {
	client := connectTestServer(t)

	// 起一个"内网服务"，代表服务端能访问但客户端不应能经由服务端访问的目标。
	// Start a private-network service reachable by the server but not exposed to clients.
	victim, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动目标服务: %v", err)
	}
	defer victim.Close()
	victimPort := victim.Addr().(*net.TCPAddr).Port

	// 用非环回地址请求转发。client.Dial 的目标地址会原样进入
	// direct-tcpip 的 payload。
	// Request a non-loopback target, passed unchanged in the direct-tcpip payload.
	for _, target := range []string{
		"192.168.1.1:80",
		"10.0.0.1:22",
		"169.254.169.254:80", // 云元数据服务——最典型的越权目标 / Cloud metadata: the canonical unauthorized target.
		// Cloud metadata is the canonical unauthorized target.
	} {
		conn, err := client.Dial("tcp", target)
		if err == nil {
			conn.Close()
			t.Errorf("转发到 %s 被允许了——服务端可被用作跳板", target)
			continue
		}
		t.Logf("转发到 %s 已拒绝: %v", target, err)
	}

	// 环回但未由在线 Exporter 发布的端口也必须拒绝；仅检查 loopback
	// 会暴露管理端口、数据库及服务器上的任意本地服务。
	// Reject unpublished loopback ports too, otherwise local admin and database services would be exposed.
	conn, err := client.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(victimPort)))
	if err == nil {
		conn.Close()
		t.Error("未登记的环回端口被允许访问")
	}
}

// TestUnauthorizedKeyRejected 验证未登记的公钥无法认证。
// TestUnauthorizedKeyRejected verifies that an unregistered key cannot authenticate.
func TestUnauthorizedKeyRejected(t *testing.T) {
	addr, _ := startServer(t)

	// 用一把未登记的密钥尝试连接。
	// Attempt to connect with an unregistered key.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(otherPriv)
	if err != nil {
		t.Fatalf("构造签名者: %v", err)
	}

	_, err = ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err == nil {
		t.Fatal("未登记的公钥通过了认证")
	}
	t.Logf("未授权公钥已被拒绝: %v", err)
}

// startServer 启动测试服务端，返回地址与客户端私钥路径。
// startServer starts a test server and returns its address and client private-key path.
func startServer(t *testing.T) (addr, clientKeyPath string) {
	t.Helper()

	dir := t.TempDir()
	hostKey := filepath.Join(dir, "host_key")
	clientKey := filepath.Join(dir, "client_key")
	authKeys := filepath.Join(dir, "authorized_keys")

	genKey(t, hostKey)
	pub := genKey(t, clientKey)
	if err := os.WriteFile(authKeys, pub, 0o600); err != nil {
		t.Fatalf("写入 authorized_keys: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测端口: %v", err)
	}
	addr = ln.Addr().String()
	ln.Close()

	srv, err := New(Config{
		Addr:           addr,
		HostKeyPath:    hostKey,
		AuthorizedKeys: authKeys,
		Version:        "test",
	}, func(f string, a ...any) { t.Logf("[server] "+f, a...) })
	if err != nil {
		t.Fatalf("创建服务端: %v", err)
	}
	go srv.ListenAndServe()
	t.Cleanup(func() { srv.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return addr, clientKey
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("服务端未就绪")
	return "", ""
}

func connectTestServer(t *testing.T) *ssh.Client {
	t.Helper()

	addr, keyPath := startServer(t)

	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取密钥: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("解析密钥: %v", err)
	}

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("SSH 连接失败: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func genKey(t *testing.T, path string) []byte {
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
