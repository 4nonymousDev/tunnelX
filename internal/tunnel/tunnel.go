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
	"tunnelx/internal/proto"
)

// Resolver returns a verified target for each business connection.
type Resolver interface {
	// Legacy source-port matching must identify exactly one verified publication.
	ResolveTarget(peerID, fingerprint, peerTunnelID string, srcPort int) (proto.Target, error)

	// DescribeRegistry 返回注册表中当前可选的对端，用于在解析失败时
	// 告诉用户"实际有什么可用"——否则只报"对端不在线"，无法区分是
	// 对端真没上线，还是 peer_id / peer_tunnel_id / peer_src_port 填错了。
	// DescribeRegistry lists available peers when resolution fails, distinguishing a
	// genuinely offline peer from incorrect peer configuration.
	DescribeRegistry() []string
}

// Publisher owns the export publication and incoming v2 channels.
type Publisher interface {
	ServeExport(context.Context, config.Tunnel, func()) error
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
	config.EnsureTunnelID(&cfg)
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

		wait := backoff.NextFor(err)
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
// serveExport publishes an explicit target on the shared v2 control connection.
func (t *Tunnel) serveExport(ctx context.Context, client *ssh.Client, pub Publisher) error {
	if pub == nil {
		return fatal(nil, "导出模式需要 v2 控制通道")
	}
	return pub.ServeExport(ctx, t.Config(), func() { t.setStatus(Status{State: StateRunning}) })
}

func (t *Tunnel) resolveTarget(res Resolver) (proto.Target, error) {
	cfg := t.Config()
	target, err := res.ResolveTarget(cfg.PeerID, cfg.PeerFingerprint, cfg.PeerTunnelID, cfg.PeerSrcPort)
	if err != nil {
		return proto.Target{}, err
	}
	if cfg.PeerFingerprint == "" || cfg.PeerTunnelID == "" {
		if binder, ok := res.(interface {
			BindTarget(string, proto.Target) error
		}); ok {
			if err := binder.BindTarget(cfg.ID, target); err != nil {
				return proto.Target{}, fatal(err, "保存可信目标失败: %v", err)
			}
		}
		t.mu.Lock()
		t.cfg.PeerFingerprint = target.Fingerprint
		t.cfg.PeerTunnelID = target.TunnelID
		t.mu.Unlock()
	}
	return target, nil
}

// A local listener never caches a remote port. Every business connection resolves
// and transmits the complete trusted target, including its current generation.
func (t *Tunnel) serveImport(ctx context.Context, client *ssh.Client, res Resolver) error {
	cfg := t.Config()
	if res == nil {
		return fatal(nil, "导入模式需要 v2 控制通道")
	}
	if _, err := t.resolveTarget(res); err != nil {
		t.log.Warnf(t.Label(), "目标解析失败 peer_id=%s peer_tunnel_id=%s: %v", cfg.PeerID, cfg.PeerTunnelID, err)
		t.log.Infof(t.Label(), "注册表当前可选对端: %v", res.DescribeRegistry())
		t.setStatus(Status{State: StatePeerOffline, Reason: err.Error()})
		return err
	}
	ln, err := listenLoopback(cfg.ListenPort)
	if err != nil {
		return fmt.Errorf("监听本地端口 %d 失败: %w%s", cfg.ListenPort, err, occupantHint(cfg.ListenPort))
	}
	defer ln.Close()
	t.setStatus(Status{State: StateRunning})
	listenerDone := make(chan struct{})
	defer close(listenerDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-listenerDone:
		}
	}()
	return t.acceptLoop(ctx, ln, func() (io.ReadWriteCloser, error) {
		target, err := t.resolveTarget(res)
		if err != nil {
			return nil, err
		}
		payload, err := proto.EncodeOpen(target)
		if err != nil {
			return nil, err
		}
		// OpenChannel has no context API. Closing this transport on the deadline
		// ensures its pending operation exits rather than leaking a waiter.
		deadline := time.AfterFunc(10*time.Second, func() { _ = client.Close() })
		ch, reqs, err := client.OpenChannel(proto.OpenChannelType, payload)
		deadline.Stop()
		if err != nil {
			return nil, err
		}
		go ssh.DiscardRequests(reqs)
		if ctx.Err() != nil {
			_ = ch.Close()
			return nil, ctx.Err()
		}
		return ch, nil
	})
}

// acceptLoop 接受连接并为每个连接建立双向转发。
// acceptLoop accepts connections and establishes bidirectional forwarding for each one.
func (t *Tunnel) acceptLoop(ctx context.Context, ln net.Listener, dial func() (io.ReadWriteCloser, error)) error {
	ctx, cancel := context.WithCancel(ctx)
	var active sync.WaitGroup
	defer func() { cancel(); active.Wait() }()
	slots := make(chan struct{}, 32)
	for {
		in, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // 用户停止导致的 Accept 失败不是错误 / Accept failure caused by a user stop is not an error.
			}
			return fmt.Errorf("接受连接: %w", err)
		}

		t.log.Debugf(t.Label(), "收到本地连接 %s", in.RemoteAddr())

		select {
		case slots <- struct{}{}:
		default:
			_ = in.Close()
			continue
		}
		active.Add(1)
		go func() {
			defer active.Done()
			defer func() { <-slots }()
			defer in.Close()

			out, err := dial()
			if err != nil {
				// 单个连接失败不影响隧道整体——目标服务可能只是暂时没起来。
				// One failed connection does not fail the tunnel; the target may be temporarily unavailable.
				t.log.Warnf(t.Label(), "转发失败: %v", err)
				return
			}
			defer out.Close()
			finished := make(chan struct{})
			defer close(finished)
			go func() {
				select {
				case <-ctx.Done():
					_ = in.Close()
					_ = out.Close()
				case <-finished:
				}
			}()

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
func pipe(a, b io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst, src io.ReadWriteCloser) {
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
