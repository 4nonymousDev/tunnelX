// Package tunnel 实现单条隧道的生命周期管理。
// 每条隧道一个独立 goroutine，独立启停、独立重连、独立退避。这是相对现有脚本的
// 关键改进：现有脚本把三条 -R 塞进同一个 ssh 进程并配 ExitOnForwardFailure=yes，
// 只要一条转发失败，整个进程退出、三条一起断。
// Package tunnel manages the lifecycle of an individual tunnel.
// Each tunnel has its own goroutine, start/stop cycle, reconnect behavior, and backoff.
// Unlike the old script, one failed forwarding rule no longer terminates every tunnel.
package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
)

// Resolver 供 Import 模式在每次（重）建立时查询对端的当前服务端端口。
// 服务端端口是动态分配的，重连后大概率会变，因此不能记住端口号，必须按
// 「对端身份 + 源端口」重新解析。
// Resolver looks up the peer's current server port whenever an Import tunnel is
// established or re-established. Dynamic ports must be resolved again by peer identity
// and source port rather than cached across reconnects.
type Resolver interface {
	// ResolveRemotePort 按稳定隧道 ID（旧配置按源端口）返回对端当前的服务端端口。
	// 对端不在线时返回 ok=false，调用方据此进入 StatePeerOffline。
	// ResolveRemotePort uses the stable tunnel ID, or source port for legacy configs,
	// and returns ok=false for an offline peer so the caller enters StatePeerOffline.
	ResolveRemotePort(peerID, peerTunnelID string, srcPort int) (port int, ok bool)

	// DescribeRegistry 返回注册表中当前可选的对端，用于在解析失败时
	// 告诉用户"实际有什么可用"——否则只报"对端不在线"，无法区分是
	// 对端真没上线，还是 peer_id / peer_tunnel_id / peer_src_port 填错了。
	// DescribeRegistry lists available peers when resolution fails, distinguishing a
	// genuinely offline peer from incorrect peer configuration.
	DescribeRegistry() []string
}

// Publisher 供 Export 模式在拿到服务端分配的端口后上报。
// 必须在 -R 建立、拿到实际端口之后调用——服务端端口是动态分配的，建立前无从
// 得知。
// Publisher reports an Export tunnel only after -R has returned the dynamically
// allocated server port, which cannot be known beforehand.
type Publisher interface {
	// Track 登记一条已建立的隧道。
	// Track records an established tunnel.
	Track(tunnelID, srcHost string, srcPort, remotePort int, name string)
	// Untrack 注销一条隧道（停止或失败时）。
	// Untrack removes a stopped or failed tunnel.
	Untrack(tunnelID string)
	// Publish 上报当前全部隧道的全量快照。
	// Publish reports a full snapshot of all current tunnels.
	Publish()
}

// Tunnel 是一条运行中的隧道。
// Tunnel is a running tunnel.
type Tunnel struct {
	cfg config.Tunnel
	log *logbuf.Buffer

	mu     sync.RWMutex
	status Status

	cancel   context.CancelFunc
	wg       sync.WaitGroup
	onChange func()
}

// New 创建隧道，尚未启动。onChange 在状态变化时调用，供 UI 刷新；可为 nil。
// New creates but does not start a tunnel. onChange refreshes the UI and may be nil.
func New(cfg config.Tunnel, log *logbuf.Buffer, onChange func()) *Tunnel {
	return &Tunnel{
		cfg:      cfg,
		log:      log,
		status:   Status{State: StateStopped},
		onChange: onChange,
	}
}

// Config 返回隧道配置的副本。
// Config returns a copy of the tunnel configuration.
func (t *Tunnel) Config() config.Tunnel {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cfg
}

// Status 返回当前状态快照。
// Status returns the current state snapshot.
func (t *Tunnel) Status() Status {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.status
}

// Label 返回用于日志与 UI 的名称。隧道名为空时退回端口号——
// 80/22 尚可自解释，但 8020、5007 这类过几天自己也认不出。
// Label returns the log/UI name, falling back to the port when no name is configured.
func (t *Tunnel) Label() string {
	cfg := t.Config()
	if cfg.Name != "" {
		return cfg.Name
	}
	if cfg.Kind == config.KindExport {
		return fmt.Sprintf("导出:%d", cfg.LocalPort)
	}
	return fmt.Sprintf("导入:%d", cfg.ListenPort)
}

func (t *Tunnel) setStatus(s Status) {
	t.mu.Lock()
	t.status = s
	t.mu.Unlock()
	if t.onChange != nil {
		t.onChange()
	}
}

// Start 启动隧道。deps 提供 Import 所需的端口解析与 Export 所需的上报回调。
// conn 失效时隧道会自行退出——重连由上层统一处理：SSH 连接是全局的，
// 每条隧道各自重连没有意义。
// Start starts the tunnel using dependencies for Import resolution and Export reports.
// A failed shared SSH connection ends the tunnel; the upper layer owns reconnection.
func (t *Tunnel) Start(ctx context.Context, client *ssh.Client, res Resolver, pub Publisher) {
	t.Stop() // 幂等：重复 Start 不应叠加 goroutine / Idempotent: repeated Start must not stack goroutines.

	ctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.run(ctx, client, res, pub)
	}()
}

// Stop 停止隧道并等待其 goroutine 退出。
// Stop stops the tunnel and waits for its goroutine to exit.
func (t *Tunnel) Stop() {
	t.mu.Lock()
	cancel := t.cancel
	t.cancel = nil
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	t.wg.Wait()
	t.setStatus(Status{State: StateStopped})
}

// run 是隧道的重试循环。
// run is the tunnel retry loop.
func (t *Tunnel) run(ctx context.Context, client *ssh.Client, res Resolver, pub Publisher) {
	backoff := NewBackoff()

	for {
		if ctx.Err() != nil {
			return
		}

		err := t.serve(ctx, client, res, pub)

		// 用户主动停止不算失败。
		// An explicit user stop is not a failure.
		if ctx.Err() != nil {
			return
		}

		fault := Classify(err)
		if fault == nil {
			// serve 正常返回意味着连接已失效，交由上层重连。
			// A normal serve return means the connection failed; the upper layer reconnects.
			return
		}

		if !fault.Retryable {
			t.log.Errorf(t.Label(), "%s", fault.Reason)
			t.setStatus(Status{State: StateError, Reason: fault.Reason})
			return
		}

		wait := backoff.Next()
		t.log.Warnf(t.Label(), "%s；%s 后重试", fault.Reason, wait.Round(time.Second))
		t.setStatus(Status{
			State:   StateReconnecting,
			Reason:  fault.Reason,
			RetryAt: time.Now().Add(wait),
		})

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (t *Tunnel) serve(ctx context.Context, client *ssh.Client, res Resolver, pub Publisher) error {
	if t.Config().Kind == config.KindExport {
		return t.serveExport(ctx, client, pub)
	}
	return t.serveImport(ctx, client, res)
}

// serveExport 把本地端口推到服务器（-R 远程转发）。
// serveExport publishes a local port through server-side -R forwarding.
func (t *Tunnel) serveExport(ctx context.Context, client *ssh.Client, pub Publisher) error {
	cfg := t.Config()

	// 远端端口写 0，由服务端分配空闲端口。这消除了"远端端口冲突"这一整类
	// 问题，也避免了自行"查询占用→挑选→绑定"的竞态。
	// Request port zero so the server allocates a free port, eliminating remote-port
	// conflicts and the check-select-bind race.
	ln, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("请求远程转发: %w", err)
	}
	defer ln.Close()

	remotePort := ln.Addr().(*net.TCPAddr).Port
	local := net.JoinHostPort(localHost(cfg), fmt.Sprint(cfg.LocalPort))

	t.log.Infof(t.Label(), "已建立：服务端 %d → %s", remotePort, local)
	t.setStatus(Status{State: StateRunning, RemotePort: remotePort})

	// 拿到实际端口后才能上报。
	// Report only after receiving the actual port.
	if pub != nil {
		pub.Track(cfg.ID, localHost(cfg), cfg.LocalPort, remotePort, cfg.Name)
		pub.Publish()
		// 隧道结束时注销并重新上报，使注册表及时反映下线。
		// Remove and republish on exit so the registry promptly reflects the tunnel going offline.
		defer func() {
			pub.Untrack(cfg.ID)
			pub.Publish()
		}()
	}

	go func() {
		<-ctx.Done()
		ln.Close() // 唤醒下方阻塞的 Accept / Wake the blocked Accept below.
	}()

	return t.acceptLoop(ctx, ln, func() (net.Conn, error) {
		return net.DialTimeout("tcp", local, 10*time.Second)
	})
}

// serveImport 在本地监听并把流量送到服务端（-L 本地转发）。
// serveImport listens locally and sends traffic to the server using -L forwarding.
func (t *Tunnel) serveImport(ctx context.Context, client *ssh.Client, res Resolver) error {
	cfg := t.Config()

	// 控制通道客户端尚未实现时 res 为 nil。判为不可自愈——重试无从改变，
	// 明确报错优于 panic 或静默无反应。
	// A nil resolver means control-channel support is unavailable. Retrying cannot fix
	// that, so return a clear fatal error instead of panicking or failing silently.
	if res == nil {
		return fatal(nil, "导入模式需要控制通道，该功能尚未实现")
	}

	remotePort, ok := res.ResolveRemotePort(cfg.PeerID, cfg.PeerTunnelID, cfg.PeerSrcPort)
	if !ok {
		reason := fmt.Sprintf("对端 %s 当前不在线", peerLabel(cfg))
		t.log.Warnf(t.Label(), "%s", reason)

		// 把注册表实际内容打出来：配错对端身份或隧道 ID 与对端确实
		// 未上线的症状完全相同，不列出可选项就无法区分。
		// Log actual registry choices because bad peer IDs and genuinely offline peers
		// otherwise look identical.
		t.log.Infof(t.Label(), "本机查找条件：peer_id=%s peer_tunnel_id=%s peer_src_port=%d",
			cfg.PeerID, cfg.PeerTunnelID, cfg.PeerSrcPort)
		if avail := res.DescribeRegistry(); len(avail) == 0 {
			t.log.Warnf(t.Label(), "注册表为空——尚无任何 Exporter 上报隧道")
		} else {
			t.log.Infof(t.Label(), "注册表当前可选对端（共 %d 条）：", len(avail))
			for _, s := range avail {
				t.log.Infof(t.Label(), "  %s", s)
			}
		}

		t.setStatus(Status{State: StatePeerOffline, Reason: reason})
		// 归为可重试：对端开机即可恢复。
		// This is retryable because recovery occurs when the peer comes online.
		return retryable(nil, "%s", reason)
	}

	// 本地监听是纯本地操作，与 SSH 连接无关——因此"先查询、后转发"的时序
	// 完全不需要第二条连接。
	// 同时绑定 IPv4 与 IPv6 环回。
	// 只绑 127.0.0.1 时，浏览器访问 http://localhost:PORT 若解析到 ::1 会直接
	// 连接被拒——且连接根本到不了本程序，日志里不会有任何记录，极难排查。
	// Windows 上 localhost 常优先解析为 ::1，故两个地址族都要监听。
	// 注意 net.Listen("tcp", "localhost:PORT") 只会绑定其中一个地址族，
	// 不能满足此需求，必须显式各绑一次。
	// Local listening is independent of SSH and needs no second connection. Bind both
	// IPv4 and IPv6 loopback explicitly because localhost often resolves to ::1 on Windows
	// and net.Listen with "localhost" binds only one family.
	lns, err := listenLoopback(cfg.ListenPort)
	if err != nil {
		// 绑定失败时须精确到占用者，否则用户不知道该处理谁。
		// Identify the owning process on bind failure so the user knows what to address.
		return fmt.Errorf("监听本地端口 %d 失败: %w%s",
			cfg.ListenPort, err, occupantHint(cfg.ListenPort))
	}
	ln := lns
	defer ln.Close()

	t.log.Infof(t.Label(), "已建立：本地 %d → 服务端 %d（%s）",
		cfg.ListenPort, remotePort, peerLabel(cfg))
	t.setStatus(Status{State: StateRunning, RemotePort: remotePort})

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	target := fmt.Sprintf("127.0.0.1:%d", remotePort)
	return t.acceptLoop(ctx, ln, func() (net.Conn, error) {
		// 在已有 SSH 连接里开新 channel，不新建连接。
		// Open a new channel on the existing SSH connection rather than creating another connection.
		t.log.Debugf(t.Label(), "正在经 SSH 连接服务端 %s…", target)
		c, err := client.Dial("tcp", target)
		if err != nil {
			return nil, err
		}
		t.log.Debugf(t.Label(), "已连通服务端 %s", target)
		return c, nil
	})
}

// acceptLoop 接受连接并为每个连接建立双向转发。
// acceptLoop accepts connections and establishes bidirectional forwarding for each one.
func (t *Tunnel) acceptLoop(ctx context.Context, ln net.Listener, dial func() (net.Conn, error)) error {
	for {
		in, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // 用户停止导致的 Accept 失败不是错误 / Accept failure caused by a user stop is not an error.
			}
			return fmt.Errorf("接受连接: %w", err)
		}

		t.log.Debugf(t.Label(), "收到本地连接 %s", in.RemoteAddr())

		go func() {
			defer in.Close()

			out, err := dial()
			if err != nil {
				// 单个连接失败不影响隧道整体——目标服务可能只是暂时没起来。
				// One failed connection does not fail the tunnel; the target may be temporarily unavailable.
				t.log.Warnf(t.Label(), "转发失败: %v", err)
				return
			}
			defer out.Close()

			pipe(in, out)
			t.log.Debugf(t.Label(), "连接 %s 已关闭", in.RemoteAddr())
		}()
	}
}

// pipe 在两个连接间双向复制，**两个方向都结束后**才返回。
// 不能在任一方向结束时就返回：调用方的 defer Close() 会立即掐断另一方向，
// 导致仍在传输的数据被截断。典型表现是客户端发完请求后读响应时收到
// "connection forcibly closed"——因为发送方向的 io.Copy 先返回了。
// 正确做法是某一方向读完时只关闭对端的写半边（发出 EOF），让另一方向自然收尾。
// pipe copies bidirectionally and returns only after both directions finish. Returning
// after one direction would let deferred Close truncate the response. Instead, EOF in
// one direction half-closes the peer's write side and lets the other direction drain.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		// 半关闭：通知对端"我不再发送了"，但仍可继续接收。
		// ssh.Channel 与 *net.TCPConn 都实现了 CloseWrite。
		// Half-close the write side while continuing to receive; both SSH channels and
		// TCP connections implement CloseWrite.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}

	go cp(a, b)
	go cp(b, a)

	wg.Wait()
}

func localHost(cfg config.Tunnel) string {
	if cfg.LocalHost == "" {
		return "127.0.0.1"
	}
	return cfg.LocalHost
}

func peerLabel(cfg config.Tunnel) string {
	if cfg.PeerName != "" {
		return fmt.Sprintf("%s:%d", cfg.PeerName, cfg.PeerSrcPort)
	}
	return fmt.Sprintf("%s:%d", cfg.PeerID, cfg.PeerSrcPort)
}
