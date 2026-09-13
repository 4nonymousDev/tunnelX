package keygen

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestGenerateRoundTrip 验证生成的密钥对能被标准 SSH 实现读回，
// 且公钥与私钥匹配——服务端正是用 ParseAuthorizedKey 加载公钥、
// 用 Marshal 后的字节比对，此测试即"服务端会认这把钥匙"的直接验证。
// TestGenerateRoundTrip verifies that standard SSH code can read the generated pair
// and that its public and private halves match exactly as the server compares them.
func TestGenerateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	res, err := Generate(dir, "tunnel_key", "DEV-PC")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	privData, err := os.ReadFile(res.KeyPath)
	if err != nil {
		t.Fatalf("读取私钥: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(privData)
	if err != nil {
		t.Fatalf("解析私钥: %v", err)
	}

	pubData, err := os.ReadFile(res.PubPath)
	if err != nil {
		t.Fatalf("读取公钥: %v", err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(pubData)
	if err != nil {
		t.Fatalf("解析公钥: %v", err)
	}

	if !bytes.Equal(parsed.Marshal(), signer.PublicKey().Marshal()) {
		t.Fatal("公钥与私钥不匹配")
	}

	if parsed.Type() != ssh.KeyAlgoED25519 {
		t.Errorf("密钥类型 = %s，期望 %s", parsed.Type(), ssh.KeyAlgoED25519)
	}

	// Result.PublicKey 是 UI 复制到剪贴板的内容，必须与落盘公钥一致，
	// 否则用户贴到服务端的是另一把钥匙。
	// Result.PublicKey is copied by the UI and must match the public-key file.
	if strings.TrimSpace(res.PublicKey) != strings.TrimSpace(string(pubData)) {
		t.Error("Result.PublicKey 与公钥文件内容不一致")
	}

	if res.Fingerprint != ssh.FingerprintSHA256(parsed) {
		t.Errorf("指纹 = %q，期望 %q", res.Fingerprint,
			ssh.FingerprintSHA256(parsed))
	}
}

// TestGenerateComment 验证公钥带上本机名称——服务端管理员靠它在
// authorized_keys 里认出每一行属于哪台机器。
// TestGenerateComment verifies that the public-key comment identifies the local machine.
func TestGenerateComment(t *testing.T) {
	dir := t.TempDir()

	res, err := Generate(dir, "tunnel_key", "DEV-PC")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	_, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(res.PublicKey))
	if err != nil {
		t.Fatalf("解析公钥: %v", err)
	}
	if !strings.HasPrefix(comment, "DEV-PC@") {
		t.Errorf("注释 = %q，期望以 %q 开头", comment, "DEV-PC@")
	}
}

// TestGenerateEmptyName 验证名称为空时仍生成合法公钥。
// 名称来自配置，理论上非空，但配置可被手工编辑成空值。
// TestGenerateEmptyName verifies that an empty, manually edited name still produces a valid key.
func TestGenerateEmptyName(t *testing.T) {
	dir := t.TempDir()

	res, err := Generate(dir, "tunnel_key", "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(res.PublicKey)); err != nil {
		t.Fatalf("解析公钥: %v", err)
	}
}

// TestGenerateRefusesExistingKey 验证私钥已存在时拒绝生成且不动原文件。
// 覆盖既有私钥无法找回：它可能已登记在服务端，还被其他机器依赖。
// TestGenerateRefusesExistingKey verifies that generation never overwrites an existing private key.
func TestGenerateRefusesExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnel_key")

	const original = "已有的私钥内容"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Generate(dir, "tunnel_key", "DEV-PC"); err == nil {
		t.Fatal("私钥已存在时应返回错误")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Error("原私钥文件被修改")
	}
}

// TestGenerateRefusesExistingPub 验证仅公钥存在时同样拒绝——
// 否则会写出与既有 .pub 不配对的私钥，用户登记的公钥将永远认证失败。
// TestGenerateRefusesExistingPub verifies that generation also refuses an existing public-key half.
func TestGenerateRefusesExistingPub(t *testing.T) {
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "tunnel_key.pub")

	if err := os.WriteFile(pubPath, []byte("ssh-ed25519 AAAA... old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Generate(dir, "tunnel_key", "DEV-PC"); err == nil {
		t.Fatal("公钥已存在时应返回错误")
	}

	// 私钥不应被创建：留下孤立私钥会让目录状态难以理解。
	// No orphan private key should be created.
	if _, err := os.Stat(filepath.Join(dir, "tunnel_key")); err == nil {
		t.Error("拒绝生成时不应创建私钥文件")
	}
}

// TestGeneratePrivateKeyMode 验证私钥权限位。
// Windows 不用 Unix 权限位表达访问控制（见 internal/keyperm），故跳过。
// TestGeneratePrivateKeyMode verifies the Unix private-key mode; Windows uses ACLs instead.
func TestGeneratePrivateKeyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上访问控制由 ACL 表达，不看权限位")
	}

	dir := t.TempDir()
	res, err := Generate(dir, "tunnel_key", "DEV-PC")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	info, err := os.Stat(res.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("私钥权限 = %o，期望 600", mode)
	}
}

// TestGenerateRejectsBadName 验证文件名不接受路径分隔符。
// 名称直接来自设置界面的输入框，用户可能填入路径。
// TestGenerateRejectsBadName verifies that a settings value cannot smuggle in path separators.
func TestGenerateRejectsBadName(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"", "  ", "sub/tunnel_key", `sub\tunnel_key`, ".."} {
		if _, err := Generate(dir, name, "DEV-PC"); err == nil {
			t.Errorf("名称 %q 应被拒绝", name)
		}
	}
}
