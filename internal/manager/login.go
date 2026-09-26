package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"tunnelx/internal/config"
	"tunnelx/internal/enrollment"
	"tunnelx/internal/keygen"
	"tunnelx/internal/sshconn"
)

// Login enrolls the existing device identity once, then resumes ordinary key authentication.
// The caller serializes settings changes; credentials are never added to configuration.
func (m *Manager) Login(ctx context.Context, username, password string) (err error) {
	username = enrollment.NormalizeUsername(username)
	if !enrollment.ValidUsername(username) || !enrollment.ValidLoginPassword(password) {
		return errors.New("请输入有效账号和密码")
	}
	ctx, cancel := context.WithTimeout(ctx, sshconn.EnrollmentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	addr := m.cfg.ServerAddr
	if addr == "" {
		return errors.New("请先填写服务器地址")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "22")
	}
	wasRunning := m.ConnStatus().State != ConnIdle
	m.Stop()
	defer func() {
		if err != nil && wasRunning {
			m.Start()
		}
	}()
	m.setConnStatus(ConnStatus{State: ConnConnecting, Reason: "正在登录账号"})
	defer func() {
		if err != nil && !wasRunning {
			m.setConnStatus(ConnStatus{State: ConnIdle})
		}
	}()
	keyPath, err := m.ensureLoginKey()
	if err != nil {
		return err
	}
	if err := m.checkKeyPerm(keyPath); err != nil {
		return err
	}
	knownHosts, err := m.cfg.KnownHostsPath()
	if err != nil {
		return err
	}
	// Persist the stable identity before a remote registration could commit. A lost
	// response must not make the next process invent a different device ID.
	if err := m.saveConfig(); err != nil {
		return fmt.Errorf("保存设备身份: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := (sshconn.Dialer{Addr: addr, KeyPath: keyPath, KnownHosts: knownHosts, Prompt: m.hostKeyPrompt}).Enroll(ctx, username, password, m.cfg.ID, m.cfg.Name); err != nil {
		return err
	}
	m.log.Infof("conn", "账号登录成功，正在连接设备")
	m.Start()
	return nil
}

func (m *Manager) ensureLoginKey() (string, error) {
	copy := *m.cfg
	if copy.KeyPath == "" {
		copy.KeyPath = config.DefaultKeyName
	}
	path, err := copy.ResolvedKeyPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("读取设备凭据: %w", err)
		}
		if _, err := keygen.Generate(filepath.Dir(path), filepath.Base(path), ""); err != nil {
			return "", err
		}
	}
	m.mu.Lock()
	m.cfg.KeyPath = copy.KeyPath
	m.mu.Unlock()
	return path, nil
}
