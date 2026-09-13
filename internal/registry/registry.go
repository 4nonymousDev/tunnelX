// Package registry 维护在线 Exporter 注册表。
// 纯内存，不持久化：注册表描述的是"当前谁在线"，天然依附于活动的 SSH 连接。
// 服务端重启后所有连接均已断开，注册表本就应当是空的。落盘反而会留下"看起来
// 在线、实际已断"的僵尸条目，Importer 照着连接必然失败。
// Package registry maintains the online exporter registry.
// It is intentionally in-memory because entries describe active SSH connections. After
// a server restart those connections are gone, and persistence would create stale entries.
package registry

import (
	"sync"
	"time"

	"tunnelx/internal/proto"
)

// Registry 是并发安全的在线注册表。
// Registry is a concurrency-safe online registry.
type Registry struct {
	mu sync.RWMutex
	// exporters 按会话 ID 索引。用会话而非客户端 UUID 做键，是为了让同一 UUID
	// 的重复连接能被检出并以 duplicate_id 拒绝，而非静默覆盖。
	// exporters is indexed by session ID so duplicate client UUIDs can be detected and
	// rejected as duplicate_id rather than silently overwriting an existing connection.
	exporters map[uint64]*session
	// byClientID 用于检出重复注册。
	// byClientID detects duplicate registrations.
	byClientID map[string]uint64

	subs   map[uint64]chan struct{}
	nextID uint64
}

type session struct {
	clientID      string
	name          string
	clientVersion string
	role          string
	since         time.Time
	tunnels       []proto.TunnelSpec
}

func New() *Registry {
	return &Registry{
		exporters:  map[uint64]*session{},
		byClientID: map[string]uint64{},
		subs:       map[uint64]chan struct{}{},
	}
}

// Join 登记一个新会话，返回会话 ID。
// 同一客户端 UUID 已在线时返回 ok=false —— 通常意味着用户复制了 config.json，
// 或同一台机器跑了两个实例。静默覆盖会让先连的那个"莫名其妙消失"，故明确拒绝。
// Join registers a new session and returns its ID. A duplicate UUID returns ok=false,
// usually indicating a copied config or two instances; explicit rejection avoids making
// the first connection disappear silently.
func (r *Registry) Join(clientID, name, version, role string) (id uint64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byClientID[clientID]; exists {
		return 0, false
	}

	r.nextID++
	id = r.nextID
	r.exporters[id] = &session{
		clientID:      clientID,
		name:          name,
		clientVersion: version,
		role:          role,
		since:         time.Now(),
	}
	r.byClientID[clientID] = id
	return id, true
}

// Leave 摘除会话。客户端断开即调用——在线注册表不保留离线条目。
// Leave removes a session immediately on disconnect; an online registry keeps no offline entries.
func (r *Registry) Leave(id uint64) {
	r.mu.Lock()
	s, exists := r.exporters[id]
	if exists {
		delete(r.exporters, id)
		delete(r.byClientID, s.clientID)
	}
	r.mu.Unlock()

	if exists && len(s.tunnels) > 0 {
		// 有隧道的会话下线会改变注册表内容，需通知所有 Importer。
		// A session with tunnels changes registry contents when it leaves; notify all importers.
		r.notify()
	}
}

// Publish 以全量快照替换该会话的隧道列表。
// Publish replaces the session's tunnel list with a full snapshot.
func (r *Registry) Publish(id uint64, tunnels []proto.TunnelSpec) {
	r.mu.Lock()
	s, ok := r.exporters[id]
	if ok {
		s.tunnels = tunnels
	}
	r.mu.Unlock()

	if ok {
		r.notify()
	}
}

// Snapshot 返回当前全部在线隧道，供下发给 Importer。
// Snapshot returns every currently online tunnel for distribution to importers.
func (r *Registry) Snapshot() []proto.RegistryEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []proto.RegistryEntry
	for _, s := range r.exporters {
		for _, t := range s.tunnels {
			out = append(out, proto.RegistryEntry{
				ID:            s.clientID,
				Name:          s.name,
				TunnelID:      t.TunnelID,
				SrcHost:       t.SrcHost,
				SrcPort:       t.SrcPort,
				RemotePort:    t.RemotePort,
				TunnelName:    t.Name,
				Since:         s.since,
				ClientVersion: s.clientVersion,
			})
		}
	}
	return out
}

// Subscribe 订阅注册表变更。返回的 channel 在每次变更时收到通知。
// 采用广播而非精准推送：Importer 不上报"正在连谁"，服务端无从知道
// 谁关心谁。各 Importer 收到全量快照后自行比对。这一简化由全量快照语义支撑。
// Subscribe returns a channel notified on registry changes. Broadcast is necessary
// because importers do not report which peer they use; each compares the full snapshot.
func (r *Registry) Subscribe() (<-chan struct{}, func()) {
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	ch := make(chan struct{}, 1)
	r.subs[id] = ch
	r.mu.Unlock()

	return ch, func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

// notify 唤醒所有订阅者。
// channel 缓冲为 1 且非阻塞发送：订阅者若尚未处理上一次通知，说明它即将读取
// 最新快照，再塞一次通知没有意义。这使 notify 永不阻塞。
// notify wakes all subscribers. A one-element buffer and nonblocking send coalesce
// notifications while a subscriber is still handling the previous one.
func (r *Registry) notify() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
