// Command tunnel-server 是隧道服务端。
// 部署在公网服务器上，独立于系统 sshd 运行：
// Command tunnel-server is the tunnel server.
// It runs on a public server independently of the system sshd:
//
//	tunnel-server -addr :2222 -hostkey host_key -auth authorized_keys
//
// 主机密钥可用 ssh-keygen 生成：
// The host key can be generated with ssh-keygen:
//
//	ssh-keygen -t ed25519 -f host_key -N ""
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tunnelx/internal/server"
)

// Version 由构建时注入：go build -ldflags "-X main.Version=0.1.0"
// Version is injected at build time: go build -ldflags "-X main.Version=0.1.0".
var Version = "dev"

func main() {
	var cfg server.Config
	flag.StringVar(&cfg.Addr, "addr", ":2222", "监听地址")
	flag.StringVar(&cfg.HostKeyPath, "hostkey", "host_key", "服务端主机密钥路径")
	flag.StringVar(&cfg.AuthorizedKeys, "auth", "authorized_keys", "授权公钥文件路径")
	flag.StringVar(&cfg.AdminAddr, "admin-addr", "127.0.0.1:2223", "管理端监听地址（仅允许明确的 loopback IP）")
	flag.StringVar(&cfg.AdminTokenFile, "admin-token-file", "", "已弃用并忽略；管理端改用管理员账号登录")
	flag.StringVar(&cfg.DataDir, "data-dir", "data", "SQLite 数据目录")
	flag.DurationVar(&cfg.HandshakeTimeout, "handshake-timeout", 10*time.Second, "SSH 握手最长耗时")
	flag.DurationVar(&cfg.OpenTimeout, "open-timeout", 10*time.Second, "等待导出端接受转发的最长耗时")
	flag.DurationVar(&cfg.WriteTimeout, "write-timeout", 10*time.Second, "控制消息写入最长耗时")
	flag.IntVar(&cfg.Limits.MaxConnections, "max-connections", 64, "全部 TCP 连接上限")
	flag.IntVar(&cfg.Limits.MaxHandshakes, "max-handshakes", 16, "并发 SSH 握手上限")
	flag.IntVar(&cfg.Limits.MaxHandshakesPerIP, "max-handshakes-per-ip", 4, "单个来源 IP 并发握手上限")
	flag.IntVar(&cfg.Limits.SessionsPerKey, "max-sessions-per-key", 8, "每个公钥的会话上限")
	flag.IntVar(&cfg.Limits.ExportsPerSession, "max-exports-per-session", 64, "每个会话的发布隧道上限")
	flag.IntVar(&cfg.Limits.ExportsTotal, "max-exports", 1024, "全部发布隧道上限")
	flag.IntVar(&cfg.Limits.ChannelsPerSession, "max-channels-per-session", 32, "每个会话的业务通道上限")
	flag.IntVar(&cfg.Limits.ChannelsTotal, "max-channels", 256, "全部业务通道上限（一次转发占用两端各一个）")
	showVersion := flag.Bool("version", false, "显示版本后退出")
	checkAuth := flag.Bool("check-auth", false, "只校验授权公钥文件，成功后退出，不监听或迁移数据库")
	adminAccount := flag.String("admin-account", "", "先停止服务，再创建或重设指定管理员账号后退出（交互输入密码）")
	flag.Parse()
	// In particular, reject a trailing "-- -version". Otherwise an upgrade
	// preflight can accidentally start the real server and migrate its database.
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "不接受位置参数；请使用明确的命令行选项")
		os.Exit(2)
	}

	if *showVersion {
		fmt.Println("tunnel-server", Version)
		return
	}
	if *checkAuth {
		if err := server.CheckAuthorizedKeys(cfg.AuthorizedKeys); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("authorized_keys: valid")
		return
	}
	if *adminAccount != "" {
		if err := runAdminAccount(cfg.DataDir, *adminAccount, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cfg.Version = Version

	logger := log.New(os.Stdout, "", log.LstdFlags)

	srv, err := server.New(cfg, logger.Printf)
	if err != nil {
		logger.Fatalf("启动失败: %v", err)
	}

	// 收到终止信号时优雅关闭，让已建立的连接有机会正常收尾。
	// Shut down gracefully on a termination signal so established connections can finish cleanly.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		logger.Println("正在关闭…")
		srv.Close()
	}()

	if err := srv.ListenAndServe(); err != nil {
		logger.Fatalf("运行失败: %v", err)
	}
}
