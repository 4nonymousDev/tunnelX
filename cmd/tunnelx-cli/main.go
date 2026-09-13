// Command tunnelx-cli runs and controls the headless TunnelX client.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/core"
	"tunnelx/internal/keygen"
	"tunnelx/internal/localapi"
	"tunnelx/internal/logbuf"
)

var Version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tunnelx-cli:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			usage()
			return nil
		case "version":
			fmt.Println(Version)
			return nil
		case "keygen":
			return runKeygen(args[1:], os.Stdout)
		}
	}
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file (default: executable directory/config.json)")
	endpointPath := fs.String("endpoint", "", "local control endpoint file")
	stateDir := fs.String("state-dir", "", "writable directory for known_hosts, logs and control state")
	acceptFingerprint := fs.String("accept-host-key", "", "accept only this SHA256 host fingerprint")
	confirmViaAPI := fs.Bool("confirm-via-api", false, "publish confirmation requests through the local control API")
	follow := fs.Bool("follow", false, "follow new log entries (logs command)")
	confirmationID := fs.Uint64("id", 0, "confirmation request id")
	accept := fs.Bool("accept", false, "accept a confirmation request")
	reject := fs.Bool("reject", false, "reject a confirmation request")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("未知参数: %s", strings.Join(fs.Args(), " "))
	}

	var cfg *config.Config
	var err error
	if *configPath == "" {
		cfg, err = config.Load()
	} else {
		cfg, err = config.LoadPath(*configPath)
	}
	if err != nil {
		return err
	}
	if err := cfg.SetStateDir(*stateDir); err != nil {
		return err
	}
	endpoint := *endpointPath
	if endpoint == "" {
		endpoint, err = localapi.DefaultEndpointPath(cfg)
		if err != nil {
			return err
		}
	}

	switch command {
	case "run":
		return runDaemon(cfg, endpoint, *acceptFingerprint, *confirmViaAPI)
	case "status", "connect", "disconnect", "logs", "tunnels", "confirm":
		return runControl(command, endpoint, *follow, *confirmationID, *accept, *reject)
	default:
		return fmt.Errorf("未知命令 %q", command)
	}
}

func usage() {
	fmt.Println(`TunnelX headless client

Usage:
  tunnelx-cli run [--config path] [--accept-host-key SHA256:...] [--confirm-via-api]
  tunnelx-cli keygen --username NAME --email EMAIL [--output path]
  tunnelx-cli status [--config path]
  tunnelx-cli connect|disconnect [--config path]
  tunnelx-cli logs [--follow] [--config path]
  tunnelx-cli tunnels [--config path]
  tunnelx-cli confirm --id N (--accept|--reject) [--config path]`)
}

func runKeygen(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(out)
	username := fs.String("username", "", "username stored in plaintext .pub metadata")
	email := fs.String("email", "", "email stored in plaintext .pub metadata")
	output := fs.String("output", config.DefaultKeyName, "private-key output path")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("未知参数: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(*username) == "" {
		return fmt.Errorf("keygen需要 --username")
	}
	if strings.TrimSpace(*email) == "" {
		return fmt.Errorf("keygen需要 --email")
	}
	rawOutput := strings.TrimSpace(*output)
	if rawOutput == "" {
		return fmt.Errorf("keygen的 --output 不能为空")
	}
	if rawOutput == "." || strings.HasSuffix(rawOutput, "/") || strings.HasSuffix(rawOutput, `\`) {
		return fmt.Errorf("keygen的 --output 必须包含私钥文件名")
	}

	keyPath, err := filepath.Abs(filepath.Clean(rawOutput))
	if err != nil {
		return fmt.Errorf("解析输出路径: %w", err)
	}
	parent := filepath.Dir(keyPath)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("输出目录不可用 %s: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("输出目录不是目录: %s", parent)
	}
	metadata, err := keygen.NewMetadata(*username, *email)
	if err != nil {
		return err
	}
	result, err := keygen.GenerateWithMetadata(parent, filepath.Base(keyPath), metadata)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Private key: %s\n", result.KeyPath)
	fmt.Fprintf(out, "Public key: %s\n", result.PubPath)
	fmt.Fprintf(out, "Fingerprint: %s\n", result.Fingerprint)
	fmt.Fprintln(out, "Authorized key line:")
	fmt.Fprint(out, result.PublicKey)
	if result.PermErr != nil {
		fmt.Fprintf(out, "Warning: could not tighten private-key permissions: %v\n", result.PermErr)
	}
	return nil
}

func runDaemon(cfg *config.Config, endpoint, fingerprint string, confirmViaAPI bool) error {
	confirmation, err := confirmationForRun(fingerprint, confirmViaAPI, os.Stdin)
	if err != nil {
		return err
	}

	log := logbuf.New(nil)
	dir, err := cfg.StateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}
	sink, err := logbuf.NewFileSink(filepath.Join(dir, "tunnelx.log"))
	if err != nil {
		return err
	}
	log.SetSink(sink)
	defer log.CloseSink()

	service, err := core.New(cfg, core.Options{Version: Version, Log: log, Confirmation: confirmation})
	if err != nil {
		return err
	}

	console := make(chan logbuf.Entry, 256)
	go func() {
		for entry := range console {
			fmt.Printf("%s %-5s [%s] %s\n", entry.Time.Format("2006-01-02 15:04:05"), entry.Level, entry.Source, entry.Msg)
		}
	}()
	unsub := log.Subscribe(func(entry logbuf.Entry) {
		select {
		case console <- entry:
		default:
		}
	})
	defer func() { unsub(); close(console) }()

	api, err := localapi.Start(service, endpoint)
	if err != nil {
		service.Close()
		return err
	}
	defer func() {
		// Keep the endpoint file until tunnels and the SSH connection have fully
		// stopped. Its removal then tells the desktop process that the executable
		// is about to be released.
		service.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = api.Close(ctx)
	}()
	if err := service.Persist(); err != nil {
		return fmt.Errorf("保存客户端身份: %w", err)
	}

	log.Infof("app", "TunnelX CLI %s 已启动，配置 %s", Version, cfg.Path())
	service.Start()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-signals:
		log.Infof("app", "收到退出信号")
	case <-api.Done():
		log.Infof("app", "收到桌面端退出请求")
	}
	return nil
}

func confirmationForRun(fingerprint string, confirmViaAPI bool, stdin *os.File) (core.ConfirmationHandler, error) {
	if confirmViaAPI && fingerprint != "" {
		return nil, fmt.Errorf("--confirm-via-api 不能与 --accept-host-key 同时使用")
	}
	if confirmViaAPI {
		return nil, nil
	}
	stdinInfo, _ := stdin.Stat()
	if fingerprint != "" || stdinInfo != nil && stdinInfo.Mode()&os.ModeCharDevice != 0 {
		return &terminalConfirmation{expectedFingerprint: fingerprint, in: bufio.NewReader(stdin)}, nil
	}
	return nil, nil
}

func runControl(command, endpoint string, follow bool, confirmationID uint64, accept, reject bool) error {
	client, err := localapi.NewClient(endpoint)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch command {
	case "connect":
		return client.Connect(ctx)
	case "disconnect":
		return client.Disconnect(ctx)
	case "confirm":
		if confirmationID == 0 || accept == reject {
			return fmt.Errorf("confirm需要 --id N，并且必须且只能指定 --accept 或 --reject")
		}
		return client.Confirm(ctx, confirmationID, accept)
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}
	switch command {
	case "status":
		fmt.Printf("TunnelX %s: %s\n", snapshot.ServerAddr, snapshot.Connection.State)
		if snapshot.Connection.Reason != "" {
			fmt.Println(snapshot.Connection.Reason)
		}
		for _, pending := range snapshot.Pending {
			fmt.Printf("待确认 %d [%s]: %s\n", pending.ID, pending.Kind, pending.Message)
			if pending.Fingerprint != "" {
				fmt.Printf("  %s %s\n", pending.Host, pending.Fingerprint)
			}
		}
	case "tunnels":
		for _, item := range snapshot.Tunnels {
			cfg := item.Config
			if cfg.Kind == string(config.KindExport) {
				fmt.Printf("%d\texport\t%s\t%s:%d\tid=%s\t%s\n",
					item.Index, cfg.Name, cfg.LocalHost, cfg.LocalPort, cfg.ID, item.State)
			} else {
				peer := cfg.PeerID
				if cfg.PeerTunnelID != "" {
					peer += "/" + cfg.PeerTunnelID
				}
				fmt.Printf("%d\timport\t%s\t127.0.0.1:%d -> %s:%d\t%s\n",
					item.Index, cfg.Name, cfg.ListenPort, peer, cfg.PeerSrcPort, item.State)
			}
		}
	case "logs":
		for _, entry := range snapshot.Logs {
			fmt.Printf("%s %-5s [%s] %s\n", entry.Time.Format("2006-01-02 15:04:05"), entry.Level, entry.Source, entry.Message)
		}
		if follow {
			return followLogs(client)
		}
	}
	return nil
}

func followLogs(client *localapi.Client) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	events, errs := client.Events(ctx)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if event.Log != nil {
				entry := event.Log
				fmt.Printf("%s %-5s [%s] %s\n", entry.Time.Format("2006-01-02 15:04:05"), entry.Level, entry.Source, entry.Message)
			}
		case err, ok := <-errs:
			if ok && err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

type terminalConfirmation struct {
	expectedFingerprint string
	in                  *bufio.Reader
}

func (t *terminalConfirmation) ConfirmHostKey(host, fingerprint string) bool {
	if t.expectedFingerprint != "" {
		return fingerprint == t.expectedFingerprint
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintf(os.Stderr, "未知主机 %s，指纹 %s；无人值守运行请使用 --accept-host-key 指定该指纹\n", host, fingerprint)
		return false
	}
	fmt.Printf("首次连接 %s，主机指纹 %s。确认信任？[y/N] ", host, fingerprint)
	answer, _ := t.in.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
}

func (t *terminalConfirmation) FixKeyPermissions(path string, readers []string) bool {
	fmt.Fprintf(os.Stderr, "警告：私钥 %s 权限过宽（%s）\n", path, strings.Join(readers, ", "))
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, "请执行 chmod 600 后重启；本次按现有权限继续")
		return false
	}
	fmt.Print("是否自动收紧私钥权限？[y/N] ")
	answer, _ := t.in.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
}

func (t *terminalConfirmation) ConfirmConfigOverwrite(path string) bool {
	fmt.Fprintf(os.Stderr, "拒绝覆盖运行期间出现的配置文件 %s；请重启后重试\n", path)
	return false
}
