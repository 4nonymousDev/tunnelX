// Package control 实现控制通道客户端。
// 控制通道是 SSH 连接上的一条自定义 channel，与端口转发的 channel 并行不悖，
// 共用同一次认证与同一层加密。因此无需额外监听端口、无需自写认证。
// Package control implements the control-channel client. The custom SSH channel
// shares authentication and encryption with forwarding, avoiding another listener or auth scheme.
package control

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/proto"
	"tunnelx/internal/tunnel"
)

// Client 是控制通道的客户端一侧。
// 同时实现 tunnel.Resolver（供 Import 查询对端端口）与 tunnel.Publisher
// （供 Export 上报隧道列表）。
// Client is the client side of the control channel and implements both tunnel.Resolver and tunnel.Publisher.
type Client struct {
	ch  ssh.Channel
	pc  *proto.Conn
	log *logbuf.Buffer

	role string

	mu       sync.RWMutex
	registry []proto.RegistryEntry
	// published 记录已建立的 Export 隧道，键为稳定隧道 ID。
	// published tracks established Export tunnels by stable tunnel ID.
	published map[string]proto.TunnelSpec

	// respCh 把服务端的应答交给等待中的请求方。
	// 协议是严格请求-应答的，同一时刻只允许一个在途请求，
	// 因此单个 channel 足够，无需请求 ID 匹配。
	// respCh delivers replies; strict single-flight request/response semantics need no request IDs.
	respCh chan respond
	sendMu sync.Mutex

	onRegistry func()
	closeOnce  sync.Once
	done       chan struct{}
	cause      error
}

type respond struct {
	env proto.Envelope
	raw []byte
}

// respTimeout 限制等待服务端应答的时长。
// 无超时的话，服务端若因 bug 不回应，客户端会永久挂起且无任何迹象——
// 这正是本项目要避免的"故障不可辨别"。
// respTimeout bounds response waits so a server bug cannot hang silently forever.
const respTimeout = 15 * time.Second

// Dial 在既有 SSH 连接上打开控制通道并完成握手。
// Dial opens the control channel on an existing SSH connection and completes the handshake.
func Dial(client *ssh.Client, cfg *config.Config, role, version string, log *logbuf.Buffer) (*Client, error) {
	ch, reqs, err := client.OpenChannel(proto.ChannelType, nil)
	if err != nil {
		// 服务端不认识该 channel 类型，通常意味着对端不是 tunnel-server
		// （如误连到系统 sshd）。明确指出优于让用户对着超时猜。
		// An unknown channel usually means the endpoint is not tunnel-server, such as a system sshd.
		return nil, fmt.Errorf("打开控制通道失败（对端可能不是 tunnel-server）: %w", err)
	}
	go ssh.DiscardRequests(reqs)

	c := &Client{
		ch:        ch,
		pc:        proto.NewConn(ch),
		log:       log,
		role:      role,
		published: map[string]proto.TunnelSpec{},
		respCh:    make(chan respond, 1),
		done:      make(chan struct{}),
	}

	go c.readLoop()

	if err := c.hello(cfg, version); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// SetOnRegistry 注册注册表更新回调，供 UI 刷新在线列表。
// SetOnRegistry installs the callback used to refresh the UI's online registry.
func (c *Client) SetOnRegistry(fn func()) {
	c.mu.Lock()
	c.onRegistry = fn
	c.mu.Unlock()
}

// Done 在控制通道失效时关闭。
// Done closes when the control channel fails.
func (c *Client) Done() <-chan struct{} { return c.done }

// Cause 返回控制通道失效的原因。
// Cause returns why the control channel failed.
func (c *Client) Cause() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cause
}

// Close 关闭控制通道。可重复调用。
// Close idempotently closes the control channel.
func (c *Client) Close() error {
	c.fail(nil)
	return nil
}

func (c *Client) fail(cause error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.cause = cause
		c.mu.Unlock()
		close(c.done)
		c.ch.Close()
	})
}

// hello 完成握手与版本协商。
// hello performs the handshake and version negotiation.
func (c *Client) hello(cfg *config.Config, version string) error {
	err := c.request(proto.Hello{
		V:             proto.Version,
		Type:          proto.TypeHello,
		Role:          c.role,
		ID:            cfg.ID,
		Name:          cfg.Name,
		ClientVersion: version,
	}, proto.TypeHelloOK, nil)
	if err != nil {
		return err
	}
	// 打印完整 id：Importer 的 peer_id 需要填写此值，让用户无需翻 config.json。
	// Log the full ID because Importer peer_id uses it, sparing users from opening config.json.
	c.log.Infof("ctrl", "控制通道已建立 — 本机名称=%s 本机id=%s 角色=%s",
		cfg.Name, cfg.ID, c.role)
	return nil
}

// request 发送一条消息并等待指定类型的应答。
// 协议是严格请求-应答的：SSH 保证字节送达，但送达 ≠ 服务端接受。
// 例如 UUID 冲突时服务端会拒绝，无回执则客户端会误以为成功。
// request sends a message and waits for the expected reply. Serialized single-flight
// operation makes respCh unambiguous and ensures server-side rejection is observed.
func (c *Client) request(msg any, wantType string, out any) error {
	// 串行化：同一时刻只允许一个在途请求，respCh 才能无歧义地匹配应答。
	// Serialize requests so respCh can match the single in-flight response unambiguously.
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if err := c.pc.Send(msg); err != nil {
		return err
	}

	select {
	case <-c.done:
		if err := c.Cause(); err != nil {
			return err
		}
		return errors.New("控制通道已关闭")

	case <-time.After(respTimeout):
		return fmt.Errorf("等待服务端应答超时（%s）", respTimeout)

	case r := <-c.respCh:
		if r.env.Type == proto.TypeError {
			var e proto.Error
			if err := proto.Decode(r.raw, &e); err != nil {
				return err
			}
			// 返回 *proto.Error，由 tunnel.Classify 按 Code 判定可否重试。
			// Return *proto.Error so tunnel.Classify can decide retryability from Code.
			return &e
		}
		if r.env.Type != wantType {
			return fmt.Errorf("服务端应答类型不符：期望 %s，收到 %s", wantType, r.env.Type)
		}
		if out != nil {
			return proto.Decode(r.raw, out)
		}
		return nil
	}
}

// readLoop 持续读取服务端消息。
// 注册表推送由服务端主动发起（广播给所有 Importer），与请求-应答无关，因此必须由
// 独立的读循环处理，而非在 request 中同步读取。
// readLoop handles server-pushed registry snapshots independently of request/response traffic.
func (c *Client) readLoop() {
	for {
		env, raw, err := c.pc.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("控制通道已被对端关闭")
			}
			c.fail(err)
			return
		}

		// 版本不符直接终止：继续通信只会产生难以理解的错误。
		// Terminate immediately on a version mismatch to avoid misleading protocol errors.
		if env.V != proto.Version {
			c.fail(fmt.Errorf("协议版本不兼容：本机 v%d，服务端 v%d，请升级客户端",
				proto.Version, env.V))
			return
		}

		if env.Type == proto.TypeRegistry {
			c.applyRegistry(raw)
			continue
		}

		// 其余均为应答，交给等待中的请求方。
		// 缓冲区已满说明收到了无人等待的应答（服务端 bug 或协议错乱），
		// 丢弃并记录，不阻塞读循环——阻塞会连带使推送也停止。
		// Other messages are replies. Drop and log an unsolicited reply rather than blocking pushes.
		select {
		case c.respCh <- respond{env: env, raw: append([]byte(nil), raw...)}:
		default:
			c.log.Warnf("ctrl", "收到无人等待的应答，已丢弃: %s", env.Type)
		}
	}
}

// applyRegistry 处理服务端推送的注册表全量快照。
// applyRegistry handles a full registry snapshot pushed by the server.
func (c *Client) applyRegistry(raw []byte) {
	var reg proto.Registry
	if err := proto.Decode(raw, &reg); err != nil {
		c.log.Warnf("ctrl", "解析注册表失败: %v", err)
		return
	}

	c.mu.Lock()
	c.registry = reg.Entries
	fn := c.onRegistry
	c.mu.Unlock()

	c.log.Infof("ctrl", "注册表已更新：%d 条在线记录", len(reg.Entries))
	if fn != nil {
		fn()
	}
}

// Registry 返回当前注册表快照，供 UI 展示可选的对端列表。
// Registry returns the current snapshot for the UI's peer list.
func (c *Client) Registry() []proto.RegistryEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]proto.RegistryEntry(nil), c.registry...)
}

// ---- tunnel.Resolver ----

// ResolveRemotePort 优先按「对端身份 + 隧道 ID」精确查找；旧配置没有
// peer_tunnel_id 时回退到历史的「对端身份 + 源端口」语义。
// ResolveRemotePort prefers peer identity plus tunnel ID, falling back to the legacy source-port key.
func (c *Client) ResolveRemotePort(peerID, peerTunnelID string, srcPort int) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, e := range c.registry {
		if e.ID != peerID {
			continue
		}
		if (peerTunnelID != "" && e.TunnelID == peerTunnelID) ||
			(peerTunnelID == "" && e.SrcPort == srcPort) {
			return e.RemotePort, true
		}
	}
	return 0, false
}

// DescribeRegistry 列出注册表中可选的对端，供解析失败时排查配置。
// DescribeRegistry lists available peers for diagnosing lookup failures.
func (c *Client) DescribeRegistry() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]string, 0, len(c.registry))
	for _, e := range c.registry {
		name := e.TunnelName
		if name == "" {
			name = "(未命名)"
		}
		source := fmt.Sprint(e.SrcPort)
		if e.SrcHost != "" {
			source = fmt.Sprintf("%s:%d", e.SrcHost, e.SrcPort)
		}
		out = append(out, fmt.Sprintf(
			"%s / %s — peer_id=%s peer_tunnel_id=%s source=%s（服务端端口 %d）",
			e.Name, name, e.ID, e.TunnelID, source, e.RemotePort))
	}
	return out
}

// ---- tunnel.Publisher ----

// Track 记录一条已建立的 Export 隧道，待下次 Publish 时一并上报。
// Track records an established Export tunnel for the next Publish.
func (c *Client) Track(tunnelID, srcHost string, srcPort, remotePort int, name string) {
	c.mu.Lock()
	c.published[tunnelID] = proto.TunnelSpec{
		TunnelID:   tunnelID,
		SrcHost:    srcHost,
		SrcPort:    srcPort,
		RemotePort: remotePort,
		Name:       name,
	}
	c.mu.Unlock()
}

// Untrack 移除一条隧道（停止或失败时）。
// Untrack removes a stopped or failed tunnel.
func (c *Client) Untrack(tunnelID string) {
	c.mu.Lock()
	delete(c.published, tunnelID)
	c.mu.Unlock()
}

// Publish 向服务端上报当前全部 Export 隧道。
// 全量快照而非增量：增量一旦丢失一条消息，两端将永久不一致且难以察觉。
// Publish reports the full Export snapshot so one lost incremental message cannot cause permanent drift.
func (c *Client) Publish() {
	c.mu.RLock()
	list := make([]proto.TunnelSpec, 0, len(c.published))
	for _, spec := range c.published {
		list = append(list, spec)
	}
	c.mu.RUnlock()

	err := c.request(proto.Publish{
		V:       proto.Version,
		Type:    proto.TypePublish,
		Tunnels: list,
	}, proto.TypePublishOK, nil)
	if err != nil {
		// 上报失败不影响已建立的转发本身——隧道照常工作，只是别人看不到它。
		// 故记录警告而非中断隧道。
		// A publish failure affects discovery, not the established forward, so warn without stopping it.
		c.log.Warnf("ctrl", "上报隧道列表失败: %v", err)
		return
	}
	c.log.Infof("ctrl", "已上报 %d 条隧道", len(list))
}

// 编译期断言：Client 必须满足 tunnel 包的两个接口。
// Compile-time assertions that Client implements both tunnel interfaces.
var (
	_ tunnel.Resolver  = (*Client)(nil)
	_ tunnel.Publisher = (*Client)(nil)
)
