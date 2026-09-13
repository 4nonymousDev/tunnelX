// Package keygen 生成隧道专用的 SSH 密钥对。
// 背景：原先要求用户在命令行执行 ssh-keygen，这与本项目"单文件、免安装、
// 图形界面"的定位相悖——客户端连 SSH 客户端都不需要，却在第一步就要求
// 用户有 ssh-keygen。该命令在 Windows 上属可选组件 OpenSSH Client，并非
// 处处可用。
//
// 本包只负责"生成"。把公钥登记到服务端仍由用户自行完成：客户端不应持有
// 服务器的登录凭据，否则"隧道密钥只能开隧道、开不了 shell"这条权限边界
// 就被绕过了。
// Package keygen generates SSH key pairs dedicated to tunnels. It removes the
// dependency on the optional Windows ssh-keygen component while preserving the
// project's single-file, installation-free GUI experience. This package only
// generates keys; users still register the public key themselves so clients never
// receive server login credentials or bypass the tunnel-only security boundary.
package keygen

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/keyperm"
)

const metadataPrefix = "tunnelx:"

// Metadata is embedded as human-readable JSON in the public-key comment.
// It is descriptive only and never participates in authentication.
type Metadata struct {
	Username     string `json:"username"`
	Email        string `json:"email"`
	ComputerName string `json:"computer_name"`
}

func NewMetadata(username, email string) (Metadata, error) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown-host"
	}
	m := Metadata{Username: strings.TrimSpace(username), Email: strings.TrimSpace(email), ComputerName: strings.TrimSpace(host)}
	return m, m.Validate()
}

func (m Metadata) Validate() error {
	if m.Username == "" || len([]rune(m.Username)) > 128 {
		return fmt.Errorf("用户名不能为空且不能超过 128 个字符")
	}
	if m.Email == "" || len(m.Email) > 254 {
		return fmt.Errorf("邮箱不能为空且不能超过 254 个字符")
	}
	addr, err := mail.ParseAddress(m.Email)
	if err != nil || addr.Address != m.Email {
		return fmt.Errorf("邮箱格式无效")
	}
	if strings.TrimSpace(m.ComputerName) == "" || len([]rune(m.ComputerName)) > 255 {
		return fmt.Errorf("计算机名不能为空且不能超过 255 个字符")
	}
	return nil
}

func (m Metadata) Comment() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return metadataPrefix + string(b), nil
}

func ParseMetadataComment(comment string) (Metadata, error) {
	comment = strings.TrimSpace(comment)
	if !strings.HasPrefix(comment, metadataPrefix) {
		return Metadata{}, fmt.Errorf("公钥未包含 TunnelX 元数据")
	}
	var m Metadata
	dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(comment, metadataPrefix)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Metadata{}, fmt.Errorf("解析公钥元数据: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Metadata{}, fmt.Errorf("解析公钥元数据: 包含多余内容")
	}
	if err := m.Validate(); err != nil {
		return Metadata{}, err
	}
	return m, nil
}

// Result 是一次密钥生成的产物。
// Result contains the output of one key-generation operation.
type Result struct {
	KeyPath   string // 私钥绝对路径 / Absolute private-key path.
	PubPath   string // 公钥绝对路径 / Absolute public-key path.
	PublicKey string // authorized_keys 单行文本，供用户复制到服务端 / One authorized_keys line to copy to the server.

	// Fingerprint 是 SHA256 指纹，与 ssh-keygen -lf 的输出同格式，
	// 便于用户在服务端核对贴过去的是不是这一把。
	// Fingerprint is the SHA256 fingerprint in ssh-keygen -lf format for server-side verification.
	Fingerprint string

	// PermErr 记录收紧文件权限时的失败。密钥此时已生成成功，
	// 不作为错误返回——调用方记日志即可，权限问题由连接前的检查再次提示。
	// PermErr records a permission-tightening failure after successful generation;
	// callers log it and the preconnection check warns again later.
	PermErr error
}

// Generate 在 dir 下生成名为 name 的 ed25519 密钥对，公钥写至 name+".pub"。
// name 参数为文件名而非路径；comment 是写进公钥的本机名称。
//
// 选 ed25519 而非 RSA：与 README 原先给出的 ssh-keygen -t ed25519 一致，
// 密钥短、验签快，且 golang.org/x/crypto/ssh 与 OpenSSH 都原生支持。
//
// 不设密码短语（等同原命令的 -N ""）：本程序需无人值守自动重连，有短语则
// 每次重连都要人工输入，与常驻托盘的用法不相容。私钥的保护手段是文件
// 访问控制（见 internal/keyperm），不是短语。
// Generate creates an ed25519 key pair named name under dir and writes the public
// key to name+".pub". name is a filename, while comment identifies the machine.
// Ed25519 matches the documented OpenSSH command and is compact, fast, and widely
// supported. The key has no passphrase because unattended reconnects rely instead
// on file access control in internal/keyperm.
func Generate(dir, name, comment string) (Result, error) {
	return generate(dir, name, keyComment(comment))
}

// GenerateWithMetadata writes username, email, and computer name as plaintext
// structured metadata in the .pub comment.
func GenerateWithMetadata(dir, name string, metadata Metadata) (Result, error) {
	comment, err := metadata.Comment()
	if err != nil {
		return Result{}, err
	}
	return generate(dir, name, comment)
}

func generate(dir, name, comment string) (Result, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Result{}, fmt.Errorf("私钥文件名不能为空")
	}
	// 文件名来自设置界面的输入框，用户可能填成路径。此处只接受纯文件名：
	// 生成的密钥应与 exe 同目录（配置里的相对路径以此为基准），
	// 允许路径会让密钥落在别处，而配置中的相对路径又找不到它。
	// Accept only a plain filename from settings so the key remains beside the executable
	// and is reachable through the configured relative path.
	if strings.ContainsAny(name, `/\`) || name != filepath.Base(name) {
		return Result{}, fmt.Errorf("私钥文件名不能包含路径分隔符: %s", name)
	}

	keyPath := filepath.Join(dir, name)
	pubPath := keyPath + ".pub"

	// 两个文件都不得已存在。覆盖既有私钥无法找回——它可能已登记在服务端，
	// 还被其他机器依赖。仅公钥存在时也拒绝：否则写出的私钥与那份 .pub
	// 不配对，用户登记的公钥将永远认证失败，且症状（认证被拒）指向不了原因。
	// Refuse either existing file: overwriting a registered private key is irreversible,
	// while pairing a new private key with an existing .pub guarantees authentication failure.
	for _, p := range []string{keyPath, pubPath} {
		if _, err := os.Stat(p); err == nil {
			return Result{}, fmt.Errorf("%s 已存在，请换个文件名，"+
				"或用「浏览…」选择现有私钥", filepath.Base(p))
		}
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Result{}, fmt.Errorf("生成密钥: %w", err)
	}

	// MarshalPrivateKey 需要指针形式的 ed25519 私钥，传值会被当作未知类型。
	// MarshalPrivateKey requires an ed25519 private-key pointer; a value is treated as an unknown type.
	block, err := ssh.MarshalPrivateKey(&priv, comment)
	if err != nil {
		return Result{}, fmt.Errorf("编码私钥: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Result{}, fmt.Errorf("编码公钥: %w", err)
	}
	pubLine := authorizedKeyLine(sshPub, comment)

	// 私钥以 0600 创建，且用 O_EXCL——从 Stat 到写入之间文件可能被创建，
	// 用 O_EXCL 让"不覆盖"这条保证落在系统调用上，而不只靠先前的检查。
	// Create the private key as 0600 with O_EXCL so the no-overwrite guarantee is enforced atomically.
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Result{}, fmt.Errorf("创建私钥文件: %w", err)
	}
	if _, err := f.Write(pem.EncodeToMemory(block)); err != nil {
		f.Close()
		os.Remove(keyPath)
		return Result{}, fmt.Errorf("写入私钥: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(keyPath)
		return Result{}, fmt.Errorf("写入私钥: %w", err)
	}

	if err := os.WriteFile(pubPath, []byte(pubLine), 0o644); err != nil {
		// 公钥写失败则连私钥一并删除：留下一把没有公钥的私钥，用户既不知道
		// 该往服务端贴什么，也看不出目录里这个文件是什么。
		// Remove the private key when writing its public half fails; an orphan is unusable and confusing.
		os.Remove(keyPath)
		return Result{}, fmt.Errorf("写入公钥: %w", err)
	}

	// 新生成的密钥不该一出生就是宽权限。此处直接收紧而不像连接前检查那样
	// 弹窗询问：文件刚由本程序创建，不存在"用户有意放宽"的可能。
	// Tighten a newly created key immediately; unlike an existing key, it cannot intentionally be permissive.
	permErr := keyperm.Fix(keyPath)

	return Result{
		KeyPath:     keyPath,
		PubPath:     pubPath,
		PublicKey:   pubLine,
		Fingerprint: ssh.FingerprintSHA256(sshPub),
		PermErr:     permErr,
	}, nil
}

// authorizedKeyLine 拼出 authorized_keys 单行文本。
// ssh.MarshalAuthorizedKey 不写注释，而注释在这里有实际用处：服务端的
// authorized_keys 汇集了所有客户端的公钥，管理员撤销某台机器的访问时，
// 得先认出哪一行是它。
// authorizedKeyLine builds one authorized_keys line. The comment lets an administrator
// identify a client's entry when revoking it.
func authorizedKeyLine(pub ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if c := strings.TrimSpace(comment); c != "" {
		line += " " + c
	}
	return line + "\n"
}

// keyComment 组出 "名称@主机名"，与 ssh-keygen 的默认注释同形。
// 空格会截断注释后半段，一律替换为连字符。
// keyComment builds "name@hostname" like ssh-keygen and replaces whitespace with hyphens.
func keyComment(name string) string {
	name = strings.TrimSpace(name)
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	if name == "" {
		name = host
	}
	return sanitize(name) + "@" + sanitize(host)
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return '-'
		}
		return r
	}, s)
}
