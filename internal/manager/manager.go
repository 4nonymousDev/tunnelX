// Package manager 驱动全局连接循环，是 sshconn、tunnel、keyperm 三者的粘合层。
// 职责边界：SSH 连接是全局唯一的，因此"断线重连"必须由本包
// 统一负责，而非每条隧道各自重连——隧道只管自己那一条转发的成败。
// Package manager drives the global connection loop and binds sshconn, tunnel, and keyperm together.
// Responsibility boundary: there is exactly one global SSH connection, so this package
// owns reconnection; each tunnel manages only the success or failure of its own forwarding.
package manager

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/control"
	"tunnelx/internal/keyperm"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/proto"
	"tunnelx/internal/sshconn"
	"tunnelx/internal/tunnel"
)

// ConnState 是连接层的状态，供 UI 顶部横幅展示。
// ConnState represents connection-layer state displayed in the UI's top banner.
type ConnState int

const (
	ConnIdle ConnState = iota
	ConnConnecting
	ConnConnected
	ConnRetrying
	ConnFailed
)

// ConnStatus 是连接层状态快照。
// ConnStatus is a snapshot of connection-layer state.
type ConnStatus struct {
	State   ConnState
	Reason  string
	RetryAt time.Time
}

// KeyPermPrompt 在私钥权限过宽时征询用户。
// 返回 fix=true 表示用户选择"自动修复权限"；false 表示"忽略并继续"。
// 采用「提示 + 可忽略」而非直接拒绝——保留"我知道我在干什么"的出口，
// 避免在临时机器上被卡死。
// KeyPermPrompt asks the user what to do when private-key permissions are too broad.
// fix=true means repair automatically; false means ignore and continue. Prompting with
// an override preserves an expert escape hatch and avoids blocking use on temporary machines.
type KeyPermPrompt func(path string, readers []string) (fix bool)

// Manager 管理连接与其下所有隧道。
// Manager manages the connection and all tunnels attached to it.
type Manager struct {
	cfg *config.Config
	log *logbuf.Buffer

	hostKeyPrompt sshconn.HostKeyPrompt
	keyPermPrompt KeyPermPrompt
	onConnChange  func()

	mu      sync.RWMutex
	status  ConnStatus
	conn    *sshconn.Conn
	ctrl    *control.Client
	tunnels []*tunnel.Tunnel

	version        string
	onRegistry     func()
	onTunnelChange func()

	// runCtx 是当前连接循环的上下文，供运行期新增的隧道挂靠。
	// 未连接时为 nil。
	// runCtx is the current connection-loop context used by tunnels added at runtime.
	// It is nil while disconnected.
	runCtx context.Context

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New 创建管理器。隧道按配置建立但尚未启动。
// New creates a manager. Tunnels are constructed from configuration but not started.
func New(cfg *config.Config, log *logbuf.Buffer, version string, onTunnelChange func()) *Manager {
	config.EnsureUniqueTunnelIDs(cfg.Tunnels)
	m := &Manager{
		cfg:            cfg,
		log:            log,
		version:        version,
		onTunnelChange: onTunnelChange,
	}
	for i := range cfg.Tunnels {
		config.EnsureTunnelID(&cfg.Tunnels[i])
		tc := cfg.Tunnels[i]
		m.tunnels = append(m.tunnels, tunnel.New(tc, log, onTunnelChange))
	}
	return m
}

// Registry 返回服务端注册表快照，供 UI 展示可选对端。未连接时为空。
// Registry returns a server-registry snapshot for UI peer selection, or empty while disconnected.
func (m *Manager) Registry() []proto.RegistryEntry {
	m.mu.RLock()
	ctrl := m.ctrl
	m.mu.RUnlock()

	if ctrl == nil {
		return nil
	}
	return ctrl.Registry()
}

// SetPrompts 注入 UI 交互回调。须在 Start 前调用。
// SetPrompts injects UI interaction callbacks and must be called before Start.
func (m *Manager) SetPrompts(hostKey sshconn.HostKeyPrompt, keyPerm KeyPermPrompt, onConnChange, onRegistry func()) {
	m.hostKeyPrompt = hostKey
	m.keyPermPrompt = keyPerm
	m.onConnChange = onConnChange
	m.onRegistry = onRegistry
}

// Tunnels 返回隧道列表，供 UI 渲染。
// Tunnels returns the tunnel list for UI rendering.
func (m *Manager) Tunnels() []*tunnel.Tunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]*tunnel.Tunnel(nil), m.tunnels...)
}

// AddTunnel 新增一条隧道并持久化配置。
// 若当前已连接，新隧道会立即启动——用户刚添加就期望它生效，
// 而不是还要手动断开重连一次。
// AddTunnel adds a tunnel and persists its configuration. When connected, it starts
// immediately so users do not need to disconnect and reconnect after adding it.
func (m *Manager) AddTunnel(tc config.Tunnel) error {
	config.EnsureTunnelID(&tc)
	if err := m.validate(tc, -1); err != nil {
		return err
	}

	t := tunnel.New(tc, m.log, m.onTunnelChange)

	m.mu.Lock()
	m.tunnels = append(m.tunnels, t)
	m.cfg.Tunnels = append(m.cfg.Tunnels, tc)
	ctx, conn, ctrl := m.runCtx, m.conn, m.ctrl
	m.mu.Unlock()

	if err := m.cfg.Save(); err != nil {
		return fmt.Errorf("保存配置: %w", err)
	}
	m.log.Infof("app", "已添加隧道: %s", t.Label())

	if tc.Enabled && ctx != nil && conn != nil && ctrl != nil {
		t.Start(ctx, conn.Client(), ctrl, ctrl)
	}
	if m.onTunnelChange != nil {
		m.onTunnelChange()
	}
	return nil
}

// UpdateTunnel 修改第 i 条隧道。会先停止再按新配置重启。
// UpdateTunnel changes tunnel i, stopping it before restarting with the new configuration.
func (m *Manager) UpdateTunnel(i int, tc config.Tunnel) error {
	if err := m.validate(tc, i); err != nil {
		return err
	}

	m.mu.RLock()
	if i < 0 || i >= len(m.tunnels) {
		m.mu.RUnlock()
		return fmt.Errorf("隧道序号越界: %d", i)
	}
	old := m.tunnels[i]
	oldConfig := old.Config()
	m.mu.RUnlock()
	if tc.ID == "" {
		tc.ID = oldConfig.ID
	}
	if tc.ID != oldConfig.ID {
		return fmt.Errorf("不能修改隧道标识")
	}

	// 先停旧的，释放其占用的本地端口——否则新配置若沿用同一端口会绑定失败。
	// Stop the old tunnel first to release its local port before rebinding the same port.
	old.Stop()

	t := tunnel.New(tc, m.log, m.onTunnelChange)

	m.mu.Lock()
	m.tunnels[i] = t
	m.cfg.Tunnels[i] = tc
	ctx, conn, ctrl := m.runCtx, m.conn, m.ctrl
	m.mu.Unlock()

	if err := m.cfg.Save(); err != nil {
		return fmt.Errorf("保存配置: %w", err)
	}
	m.log.Infof("app", "已更新隧道: %s", t.Label())

	if tc.Enabled && ctx != nil && conn != nil && ctrl != nil {
		t.Start(ctx, conn.Client(), ctrl, ctrl)
	}
	if m.onTunnelChange != nil {
		m.onTunnelChange()
	}
	return nil
}

// RemoveTunnel 删除第 i 条隧道。
// RemoveTunnel removes tunnel i.
func (m *Manager) RemoveTunnel(i int) error {
	m.mu.Lock()
	if i < 0 || i >= len(m.tunnels) {
		m.mu.Unlock()
		return fmt.Errorf("隧道序号越界: %d", i)
	}
	t := m.tunnels[i]
	label := t.Label()
	m.tunnels = append(m.tunnels[:i], m.tunnels[i+1:]...)
	m.cfg.Tunnels = append(m.cfg.Tunnels[:i], m.cfg.Tunnels[i+1:]...)
	m.mu.Unlock()

	t.Stop()

	if err := m.cfg.Save(); err != nil {
		return fmt.Errorf("保存配置: %w", err)
	}
	m.log.Infof("app", "已删除隧道: %s", label)

	if m.onTunnelChange != nil {
		m.onTunnelChange()
	}
	return nil
}

// IndexOf 返回某条隧道在配置中的序号，未找到返回 -1。
// UI 侧持有的是 *tunnel.Tunnel，而增删改按序号操作，需要此映射。
// IndexOf returns a tunnel's configuration index, or -1 when absent. The UI holds
// *tunnel.Tunnel values while mutations use indexes, so this provides the mapping.
func (m *Manager) IndexOf(t *tunnel.Tunnel) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i, x := range m.tunnels {
		if x == t {
			return i
		}
	}
	return -1
}

// validate 校验隧道配置。skip 为正在编辑的序号（新增时传 -1），
// 用于避免与自身比较导致的端口冲突误报。
//
// 规则本身在 validateAgainst（见 batch.go）——批量添加需要在"现有 + 本批已接受"
// 的并集上校验，那份并集只存在于调用方的栈上，与本方法持有的锁无关。
// 本地监听端口不可重复：两条隧道抢同一端口时后启动的会绑定失败，
// 且失败发生在运行期，不如在此直接拒绝。
// validate checks a tunnel configuration. skip is the edited index (-1 for additions)
// and prevents false conflicts with itself. Rules live in validateAgainst (batch.go),
// which can validate against the caller's existing-plus-accepted-batch union. Local
// listening ports must be unique so conflicts are rejected before runtime binding fails.
func (m *Manager) validate(tc config.Tunnel, skip int) error {
	m.mu.RLock()
	existing := append([]config.Tunnel(nil), m.cfg.Tunnels...)
	m.mu.RUnlock()

	return validateAgainst(existing, tc, skip)
}

// ConnStatus 返回连接层状态快照。
// ConnStatus returns a snapshot of connection-layer state.
func (m *Manager) ConnStatus() ConnStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) setConnStatus(s ConnStatus) {
	m.mu.Lock()
	m.status = s
	m.mu.Unlock()
	if m.onConnChange != nil {
		m.onConnChange()
	}
}

// Start 启动连接循环。非阻塞。
// Start launches the connection loop without blocking.
func (m *Manager) Start() {
	m.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.loop(ctx)
	}()
}

// Stop 停止连接与全部隧道，并等待收尾。
// Stop terminates the connection and all tunnels, then waits for cleanup.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()

	for _, t := range m.tunnels {
		t.Stop()
	}
	m.setConnStatus(ConnStatus{State: ConnIdle})
}

// loop 是连接层的重试循环。
// loop is the connection layer's retry loop.
func (m *Manager) loop(ctx context.Context) {
	backoff := tunnel.NewBackoff()

	for {
		if ctx.Err() != nil {
			return
		}

		err := m.connectAndServe(ctx)

		if ctx.Err() != nil {
			return
		}

		fault := tunnel.Classify(err)
		if fault == nil {
			continue // 无错误返回意味着连接正常结束，直接重连 / A nil error means a normal end; reconnect immediately.
		}

		if !fault.Retryable {
			m.log.Errorf("conn", "%s", fault.Reason)
			m.setConnStatus(ConnStatus{State: ConnFailed, Reason: fault.Reason})
			return
		}

		wait := backoff.Next()
		m.log.Warnf("conn", "%s；%s 后重试", fault.Reason, wait.Round(time.Second))
		m.setConnStatus(ConnStatus{
			State:   ConnRetrying,
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

// connectAndServe 建立一次连接并驻留至其失效。
// connectAndServe establishes one connection and serves until it becomes invalid.
func (m *Manager) connectAndServe(ctx context.Context) error {
	keyPath, err := m.cfg.ResolvedKeyPath()
	if err != nil {
		return err
	}
	if err := m.checkKeyPerm(keyPath); err != nil {
		return err
	}

	addr := m.cfg.ServerAddr
	if addr == "" {
		return tunnel.NotConfigured("尚未配置服务器地址，请点「设置」填写")
	}
	// 空用户名不会被 SSH 层拦下——它会完成整个握手，直到服务器拒绝，
	// 而客户端只能把那次拒绝报成"认证被拒绝：私钥未被授权"，
	// 与真正的原因南辕北辙。故在此提前拦截。
	// The SSH layer does not reject an empty username until after a full handshake,
	// producing a misleading unauthorized-key error. Reject it here with the real cause.
	if m.cfg.ServerUser == "" {
		return tunnel.NotConfigured("尚未配置用户名，请点「设置」填写")
	}
	// 未指定端口时补默认 22——用户通常只填主机名。
	// Default to port 22 when omitted, since users commonly enter only a hostname.
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "22")
	}

	knownHosts, err := m.cfg.KnownHostsPath()
	if err != nil {
		return err
	}

	m.log.Infof("conn", "正在连接 %s…", addr)
	m.setConnStatus(ConnStatus{State: ConnConnecting})

	conn, err := sshconn.Dialer{
		Addr:       addr,
		User:       m.cfg.ServerUser,
		KeyPath:    keyPath,
		KnownHosts: knownHosts,
		Prompt:     m.hostKeyPrompt,
		Log:        m.log,
	}.Dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	m.log.Infof("conn", "已连接 %s", addr)

	// 在同一条连接上打开控制通道——不新建连接、不重复认证。
	// Open the control channel on the same connection without reconnecting or reauthenticating.
	ctrl, err := control.Dial(conn.Client(), m.cfg, m.role(), m.version, m.log)
	if err != nil {
		return err
	}
	defer ctrl.Close()
	ctrl.SetOnRegistry(m.onRegistry)

	m.mu.Lock()
	m.conn = conn
	m.ctrl = ctrl
	m.runCtx = ctx
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.ctrl = nil
		m.conn = nil
		m.runCtx = nil
		m.mu.Unlock()
	}()

	m.setConnStatus(ConnStatus{State: ConnConnected})
	// 服务端可能在 Dial 返回后、SetOnRegistry 安装回调前就推送初始注册表。
	// 控制客户端虽会保存该快照，但桌面端收不到变更事件；因此在 m.ctrl
	// 可见后主动补发一次，消除握手阶段的时序竞争。
	// The server may push its initial registry after Dial returns but before SetOnRegistry
	// installs the callback. The control client retains it, but the desktop misses the event;
	// replay it once m.ctrl is visible to eliminate this handshake race.
	if m.onRegistry != nil {
		m.onRegistry()
	}

	m.startTunnels(ctx, conn, ctrl)

	// 驻留至连接或控制通道失效，或用户停止。
	// Remain until the connection or control channel fails, or the user stops it.
	select {
	case <-ctx.Done():
		return nil
	case <-conn.Done():
		m.stopTunnels()
		return conn.Cause()
	case <-ctrl.Done():
		// 控制通道断了但 TCP 连接可能还在——此时注册表已过期、无法上报，
		// 整条连接已不可信，一并重建。
		// TCP may survive a failed control channel, but its registry is stale and reporting
		// is unavailable, so the whole connection is untrustworthy and must be rebuilt.
		m.stopTunnels()
		return ctrl.Cause()
	}
}

// role 判定本机在控制协议中的角色。
// 一个客户端可能同时有导出与导入隧道。角色只影响服务端是否向其推送注册表，
// 故只要存在导入隧道就声明为 importer——导出侧的上报不受角色限制。
// role determines this machine's control-protocol role. A client may both import and
// export. The role only controls registry delivery, so any import makes it an importer;
// export reporting remains available regardless of role.
func (m *Manager) role() string {
	for _, t := range m.tunnels {
		if t.Config().Kind == config.KindImport {
			return proto.RoleImporter
		}
	}
	return proto.RoleExporter
}

// startTunnels 启动所有"用户意图是运行"的隧道。
// 跳过因不可自愈原因停止的隧道——重建只会再次失败并刷日志。用户主动停止的
// 同样跳过，尊重其意图。
// startTunnels starts every tunnel the user intends to run. It skips tunnels stopped
// by non-recoverable errors to avoid repeated failures, and user-stopped tunnels to
// preserve their intent.
func (m *Manager) startTunnels(ctx context.Context, conn *sshconn.Conn, ctrl *control.Client) {
	for _, t := range m.tunnels {
		if !t.Config().Enabled {
			continue
		}
		if t.Status().State == tunnel.StateError {
			m.log.Infof(t.Label(), "上次因不可自愈错误停止，本次不自动重建")
			continue
		}
		t.Start(ctx, conn.Client(), ctrl, ctrl)
	}
}

func (m *Manager) stopTunnels() {
	for _, t := range m.tunnels {
		t.Stop()
	}
}

// checkKeyPerm 检查私钥权限，必要时征询用户并修复。
// keyPath 须为已解析的绝对路径（见 config.ResolvedKeyPath）。
// checkKeyPerm checks private-key permissions and optionally asks the user to repair them.
// keyPath must be a resolved absolute path (see config.ResolvedKeyPath).
func (m *Manager) checkKeyPerm(keyPath string) error {
	if keyPath == "" {
		return tunnel.NotConfigured("尚未配置私钥路径，请点「设置」填写或「生成…」")
	}

	// 提前检查文件是否存在，给出比"读取私钥失败"更明确的提示。
	// Check existence early to produce a clearer message than "failed to read private key."
	if _, err := os.Stat(keyPath); err != nil {
		return tunnel.Classify(fmt.Errorf("私钥文件不可用 %s: %w", keyPath, err))
	}

	res := keyperm.Check(keyPath)
	if res.Err != nil {
		// 检查本身失败不应阻断连接——留痕即可。
		// A failure of the permission check itself should be logged, not block connection.
		m.log.Warnf("conn", "私钥权限检查失败: %v", res.Err)
		return nil
	}
	if res.OK {
		return nil
	}

	m.log.Warnf("conn", "私钥 %s 可被以下主体读取: %v", keyPath, res.Readers)

	if m.keyPermPrompt == nil {
		return nil // 无 UI 可询问时放行，与"忽略并继续"等价 / With no UI to ask, allow it as “ignore and continue.”
		// Without a UI prompt, allow it as the equivalent of "ignore and continue."
	}
	if !m.keyPermPrompt(keyPath, res.Readers) {
		m.log.Warnf("conn", "用户选择忽略私钥权限风险")
		return nil
	}

	if err := keyperm.Fix(keyPath); err != nil {
		m.log.Errorf("conn", "修复私钥权限失败: %v", err)
		return nil // 修复失败不阻断连接，用户已知情 / A failed repair does not block a connection after warning the user.
		// A failed repair does not block connection because the user has acknowledged the risk.
	}
	m.log.Infof("conn", "已收紧私钥权限")
	return nil
}
