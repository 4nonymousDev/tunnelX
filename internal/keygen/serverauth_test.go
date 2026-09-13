package keygen_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/keygen"
)

// TestServerAcceptsGeneratedKey 复刻服务端 internal/server/authkeys.go 的
// 加载与比对逻辑，验证界面生成的密钥贴进 authorized_keys 后确实会被授权。
//
// 单测"公钥能被解析"还不够：真正要保证的是这条链路——生成的公钥文本原样
// 写入 authorized_keys、服务端解析出的字节，与客户端用私钥签名时呈现的
// 公钥字节一致。任一环节的格式偏差都表现为"认证被拒绝"，而这个症状看不出
// 是密钥生成的问题。
// TestServerAcceptsGeneratedKey reproduces the server's authorized_keys parsing and
// comparison path to prove that the exact generated text authenticates successfully.
func TestServerAcceptsGeneratedKey(t *testing.T) {
	dir := t.TempDir()

	res, err := keygen.Generate(dir, "tunnel_key", "DEV-PC")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// 服务端侧：用户把公钥行追加进 authorized_keys，服务端按此解析。
	// Server side: parse the line exactly as appended to authorized_keys.
	akPath := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(akPath, []byte(res.PublicKey), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(akPath)
	if err != nil {
		t.Fatal(err)
	}

	authorized := map[string]bool{}
	rest := data
	for len(rest) > 0 {
		key, _, _, remaining, err := ssh.ParseAuthorizedKey(rest)
		if err != nil {
			break
		}
		authorized[string(key.Marshal())] = true
		rest = remaining
	}
	if len(authorized) != 1 {
		t.Fatalf("服务端解析出 %d 个公钥，期望 1", len(authorized))
	}

	// 客户端侧：用私钥构造 signer，即认证时实际呈现的公钥。
	// Client side: construct the signer whose public key is presented during authentication.
	privData, err := os.ReadFile(res.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(privData)
	if err != nil {
		t.Fatalf("解析私钥: %v", err)
	}

	if !authorized[string(signer.PublicKey().Marshal())] {
		t.Fatal("服务端不认这把生成的密钥")
	}
}

// TestAppendToExistingAuthorizedKeys 验证公钥行可直接追加到已有内容之后。
// 服务端的 authorized_keys 通常已有其他机器的公钥，README 给出的登记方式
// 正是 tee -a 追加——若生成的行尾缺换行，会与下一行粘连导致两行都失效。
// TestAppendToExistingAuthorizedKeys verifies safe append behavior, including the
// trailing newline needed to keep adjacent keys from merging.
func TestAppendToExistingAuthorizedKeys(t *testing.T) {
	dir := t.TempDir()

	first, err := keygen.Generate(dir, "key_a", "PC-A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := keygen.Generate(dir, "key_b", "PC-B")
	if err != nil {
		t.Fatal(err)
	}

	combined := append([]byte(first.PublicKey), []byte(second.PublicKey)...)

	count := 0
	rest := combined
	for len(rest) > 0 {
		if _, _, _, remaining, err := ssh.ParseAuthorizedKey(rest); err != nil {
			break
		} else {
			count++
			rest = remaining
		}
	}
	if count != 2 {
		t.Fatalf("追加后解析出 %d 个公钥，期望 2", count)
	}
}
