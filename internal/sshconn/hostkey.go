package sshconn

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"tunnelx/internal/tunnel"
)

// HostKeyPrompt 在首次遇到未知主机时征询用户。
// 返回 true 表示信任并写入 known_hosts。UI 实现应展示指纹供用户核对。
// HostKeyPrompt asks the user about an unknown host on first contact.
// Returning true trusts it and writes known_hosts; the UI should show the fingerprint.
type HostKeyPrompt func(host string, fingerprint string) bool

// hostKeyCallback 构造主机密钥校验回调。
// 现有脚本用 StrictHostKeyChecking=no（start_forwarding_dbg.ps1:116）关闭了校验，
// 等于放弃中间人防护。此处改为：首次连接展示指纹供用户确认后写入 known_hosts，
// 之后严格校验；不匹配则拒绝并告警。
// hostKeyCallback constructs host-key verification.
// The previous StrictHostKeyChecking=no disabled MITM protection. The first connection
// now displays the fingerprint for confirmation and records it in known_hosts; later
// connections verify strictly and reject mismatches with a warning.
func hostKeyCallback(knownHostsPath string, prompt HostKeyPrompt) (ssh.HostKeyCallback, error) {
	// knownhosts.New 在文件不存在时报错，先确保其存在。
	// knownhosts.New rejects a missing file, so create it first.
	if err := ensureFile(knownHostsPath); err != nil {
		return nil, err
	}

	verify, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("读取 known_hosts: %w", err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err
		}

		// Want 非空表示该主机已有记录但密钥不符——可能是中间人攻击，
		// 也可能是服务器重装。无论如何都不能自动接受。
		// A nonempty Want means the host was recorded with a different key. Whether caused
		// by a MITM attack or server reinstall, it must never be accepted automatically.
		if len(keyErr.Want) > 0 {
			return tunnel.WrapHostKeyMismatch(fmt.Errorf(
				"主机 %s 的密钥与 known_hosts 中的记录不符（当前指纹 %s）",
				hostname, ssh.FingerprintSHA256(key)))
		}

		// Want 为空表示主机未知，属首次连接。
		// An empty Want means an unknown host on first connection.
		if prompt == nil || !prompt(hostname, ssh.FingerprintSHA256(key)) {
			return tunnel.WrapHostKeyRejected(fmt.Errorf("用户拒绝信任主机 %s", hostname))
		}

		if err := appendKnownHost(knownHostsPath, hostname, remote, key); err != nil {
			return fmt.Errorf("记录主机密钥: %w", err)
		}
		return nil
	}, nil
}

func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建 known_hosts: %w", err)
	}
	return f.Close()
}

func appendKnownHost(path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	_, err = fmt.Fprintln(f, line)
	return err
}
