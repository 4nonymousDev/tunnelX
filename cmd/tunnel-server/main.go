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
	flag.StringVar(&cfg.AdminTokenFile, "admin-token-file", "admin.token", "管理端 Bearer Token 文件")
	flag.StringVar(&cfg.DataDir, "data-dir", "data", "SQLite 数据目录")
	showVersion := flag.Bool("version", false, "显示版本后退出")
	flag.Parse()

	if *showVersion {
		fmt.Println("tunnel-server", Version)
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
