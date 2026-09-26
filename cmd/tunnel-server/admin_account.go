package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"tunnelx/internal/accountauth"
	"tunnelx/internal/store"
)

func runAdminAccount(dataDir, username string, in *os.File, out io.Writer) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if !store.ValidAccountUsername(username) || strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("请提供有效的管理员账号和现有服务使用的 -data-dir")
	}
	if !term.IsTerminal(int(in.Fd())) {
		return fmt.Errorf("请在交互终端输入密码；不支持命令行、环境变量或重定向传入密码")
	}
	fmt.Fprintln(out, "请确认服务已停止，并使用原服务的 -data-dir。此操作会创建或提升该账号为管理员、启用账号并重设密码。")
	password, err := readAdminPassword(out, func() ([]byte, error) { return term.ReadPassword(int(in.Fd())) })
	if err != nil {
		return err
	}
	defer clear(password)
	return provisionAdminAccount(dataDir, username, string(password), out)
}

func readAdminPassword(out io.Writer, read func() ([]byte, error)) ([]byte, error) {
	fmt.Fprint(out, "新密码（15–128 个字符）: ")
	password, err := read()
	fmt.Fprintln(out)
	if err != nil {
		clear(password)
		return nil, fmt.Errorf("无法安全读取密码")
	}
	if !accountauth.ValidPassword(string(password)) {
		clear(password)
		return nil, fmt.Errorf("密码须为 15–128 个字符，且不得超过 512 字节")
	}
	fmt.Fprint(out, "再次输入新密码: ")
	confirmation, err := read()
	fmt.Fprintln(out)
	defer clear(confirmation)
	if err != nil {
		clear(password)
		return nil, fmt.Errorf("无法安全读取密码")
	}
	if !bytes.Equal(password, confirmation) {
		clear(password)
		return nil, fmt.Errorf("两次密码不一致，未修改账号")
	}
	return password, nil
}

func provisionAdminAccount(dataDir, username, password string, out io.Writer) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("无法打开服务数据目录: %w", err)
	}
	s, err := store.Open(filepath.Join(dataDir, "tunnel-server.db"))
	if err != nil {
		return fmt.Errorf("无法打开服务数据库: %w", err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = s.ProvisionAdminAccount(ctx, username, password, store.AdminAction{Operator: "local-cli", TransportPeer: "local", Reason: "local administrator creation or recovery"})
	if err != nil {
		return fmt.Errorf("管理员账号操作失败: %w", err)
	}
	fmt.Fprintf(out, "管理员账号 %s 已就绪。启动服务后可用该账号密码登录管理界面；该账号之前的登录凭据已失效。\n", username)
	return nil
}
