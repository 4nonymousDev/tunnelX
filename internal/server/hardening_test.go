package server

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/store"
)

// TestAuthKeysHotReload 验证 authorized_keys 变更后无需重启即可生效。
// 新增一台客户端或撤销某台的访问都需要改这个文件。若必须重启服务，
// 正在运行的所有隧道都会被切断——而这恰恰是最频繁的运维操作。
// TestAuthKeysHotReload verifies that additions and revocations take effect without
// restarting and interrupting every live tunnel.
func TestAuthKeysHotReload(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "authorized_keys")

	// 初始只授权 key1。
	// Initially authorize only key1.
	key1Path := filepath.Join(dir, "key1")
	pub1 := genPEMKey(t, key1Path)
	if err := os.WriteFile(authPath, pub1, 0o600); err != nil {
		t.Fatalf("写入 authorized_keys: %v", err)
	}

	a, err := newAuthKeys(authPath, func(f string, args ...any) { t.Logf(f, args...) })
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	if a.Count() != 1 {
		t.Fatalf("初始公钥数 = %d, 期望 1", a.Count())
	}

	// key2 此刻尚未授权。
	// key2 is not authorized yet.
	key2Path := filepath.Join(dir, "key2")
	pub2 := genPEMKey(t, key2Path)
	signer2 := loadSigner(t, key2Path)

	if a.Authorized(signer2.PublicKey()) {
		t.Fatal("未登记的公钥不应通过")
	}

	// 追加 key2，模拟运维新增一台客户端。
	// 修改时间的精度在部分文件系统上只有秒级，故等待以确保变更可被检出。
	// Append key2; wait because some filesystems expose modification time only to whole seconds.
	time.Sleep(reloadInterval + 100*time.Millisecond)
	f, err := os.OpenFile(authPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("追加公钥: %v", err)
	}
	f.Write(pub2)
	f.Close()

	if !a.Authorized(signer2.PublicKey()) {
		t.Error("追加的公钥未在重载后生效")
	}
	if a.Count() != 2 {
		t.Errorf("重载后公钥数 = %d, 期望 2", a.Count())
	}

	// 撤销 key2：写回只含 key1 的内容。
	// Revoke key2 by writing back only key1.
	time.Sleep(reloadInterval + 100*time.Millisecond)
	if err := os.WriteFile(authPath, pub1, 0o600); err != nil {
		t.Fatalf("重写 authorized_keys: %v", err)
	}

	if a.Authorized(signer2.PublicKey()) {
		t.Error("撤销的公钥仍然有效")
	}
	t.Log("新增与撤销均已即时生效")
}

// A malformed replacement makes the active authorization state untrustworthy;
// new authentication must fail closed until a valid file has been loaded.
func TestAuthKeysFailsClosedOnError(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "authorized_keys")

	keyPath := filepath.Join(dir, "key")
	pub := genPEMKey(t, keyPath)
	os.WriteFile(authPath, pub, 0o600)

	a, err := newAuthKeys(authPath, func(f string, args ...any) { t.Logf(f, args...) })
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	signer := loadSigner(t, keyPath)

	// 写入无效内容。
	// Write invalid content.
	time.Sleep(reloadInterval + 100*time.Millisecond)
	os.WriteFile(authPath, []byte("这不是公钥\n"), 0o600)

	if a.Authorized(signer.PublicKey()) {
		t.Error("文件损坏后必须拒绝新认证，不能沿用旧授权准入")
	}
	t.Log("文件损坏时关闭了新的认证准入")
}

// TestAuditLogRecordsSession 验证审计日志记录了会话与转发的关键事件。
// 在线注册表是纯内存、断开即摘，回答的是"现在谁在线"；
// 审计日志回答的是"上周三谁访问了谁"，二者不可互相替代。
// TestAuditLogRecordsSession verifies historical session and forwarding events independently of live state.
func TestAuditLogRecordsSession(t *testing.T) {
	cfg := managementConfig(t)
	cfg.AdminAddr, cfg.AdminTokenFile = "", ""
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Addr = probe.Addr().String()
	probe.Close()
	srv, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	t.Cleanup(func() { srv.Close() })
	keyPath := filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")
	client := dialWith(t, cfg.Addr, keyPath)
	client.Close()
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(otherPriv)
	if rejected, err := ssh.Dial("tcp", cfg.Addr, &ssh.ClientConfig{User: "untrusted", Auth: []ssh.AuthMethod{ssh.PublicKeys(otherSigner)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}); err == nil {
		rejected.Close()
		t.Fatal("unauthorized key accepted")
	}
	time.Sleep(100 * time.Millisecond)
	srv.Close()
	<-done
	persisted, err := store.Open(filepath.Join(cfg.DataDir, "tunnel-server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer persisted.Close()
	events, err := persisted.ListConnections(context.Background(), store.AuditFilter{Limit: 20})
	if err != nil || len(events) == 0 {
		t.Fatalf("successful authentication audit missing: %+v %v", events, err)
	}
	fp := ssh.FingerprintSHA256(loadSigner(t, keyPath).PublicKey())
	found := false
	for _, event := range events {
		if event.Fingerprint == fp {
			found = true
		}
		if event.AuthenticatedAt.IsZero() {
			t.Fatal("audit time missing")
		}
	}
	if !found {
		t.Fatal("authenticated identity absent from audit")
	}
	rejected, _, err := persisted.TodayRejected(context.Background(), time.Now())
	if err != nil || rejected < 1 {
		t.Fatalf("rejection aggregate missing: %d %v", rejected, err)
	}
}

// TestAuditDisabledByDefault 验证未配置路径时不产生文件且不影响运行。
// TestAuditDisabledByDefault verifies that no path means no file and no runtime impact.
func TestAuditDisabledByDefault(t *testing.T) {
	a, err := newAuditLog("")
	if err != nil {
		t.Fatalf("创建禁用的审计日志: %v", err)
	}

	// 不应 panic。
	// Must not panic.
	a.write(auditEvent{Event: auditConnect, RemoteAddr: "1.2.3.4"})
	if err := a.Close(); err != nil {
		t.Errorf("关闭禁用的审计日志应返回 nil, 得到 %v", err)
	}
}

func readAudit(t *testing.T, path string) []auditEvent {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开审计日志: %v", err)
	}
	defer f.Close()

	var out []auditEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e auditEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("审计日志行不是有效 JSON: %s", line)
			continue
		}
		out = append(out, e)
	}
	return out
}

func genPEMKey(t *testing.T, path string) []byte {
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

func loadSigner(t *testing.T, path string) ssh.Signer {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取私钥: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("解析私钥: %v", err)
	}
	return signer
}
