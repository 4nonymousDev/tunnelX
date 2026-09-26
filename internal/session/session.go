// Package session owns authenticated sessions, publications and transfer leases.
// Network and database I/O must happen outside the manager lock.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"tunnelx/internal/proto"
)

type State string
type SessionState = State

const (
	Handshaking      State = "handshaking"
	Online           State = "online"
	Closing          State = "closing"
	StateHandshaking       = Handshaking
	StateOnline            = Online
	StateClosing           = Closing
)

var (
	ErrNotFound           = errors.New("session not found")
	ErrDuplicateClient    = errors.New("client id is already online")
	ErrControlExists      = errors.New("control channel already exists")
	ErrCapacity           = errors.New("session or publication capacity reached")
	ErrInvalidTarget      = errors.New("target is no longer published with this identity")
	ErrInvalidPublication = errors.New("invalid publication")
)

type Limits struct {
	Sessions, PerFingerprint, ExportsPerSession, ExportsTotal int
	ChannelsPerSession, ChannelsTotal, RegistryBytes          int
}

func DefaultLimits() Limits {
	return Limits{64, 8, 64, 1024, 32, 256, 1 << 20}
}

type Tunnel struct {
	ID, Name, SrcHost   string
	SrcPort, RemotePort int // RemotePort is retained only for historical audit display.
	Generation          uint64
}
type Session struct {
	ID, Fingerprint, ClientID, Name, Role, Version, RemoteIP string
	ConnectedAt                                              time.Time
	State                                                    State
	Tunnels                                                  []Tunnel
	control                                                  bool
	closer                                                   io.Closer
}
type Authenticated struct {
	Fingerprint, RemoteIP string
	ConnectedAt           time.Time
	Closer                io.Closer
}
type Hello struct{ ClientID, Name, Role, Version string }
type Change struct{ Revision uint64 }
type Removal struct {
	Session Session
	Reason  string
	Closer  io.Closer
	leases  []*Lease
	once    sync.Once
}

func (r *Removal) Close() {
	if r != nil {
		r.once.Do(func() {
			if r.Closer != nil {
				_ = r.Closer.Close()
			}
			for _, l := range r.leases {
				l.Cancel()
			}
		})
	}
}

// Lease is registered before any channel-open I/O. Cancelled leases reject late
// channels as well as closing already attached channels.
type Lease struct {
	Visitor     Session
	Owner       Session
	Tunnel      Tunnel
	Target      proto.Target
	Connection  io.Closer
	done        chan struct{}
	mu          sync.Mutex
	closed      bool
	resources   []io.Closer
	release     func()
	releaseOnce sync.Once
}

func (l *Lease) Done() <-chan struct{} { return l.done }
func (l *Lease) Attach(c io.Closer) bool {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = c.Close()
		return false
	}
	l.resources = append(l.resources, c)
	l.mu.Unlock()
	return true
}
func (l *Lease) Close() {
	l.Cancel()
	l.releaseOnce.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
}

// Cancel makes the target unusable immediately, but the worker retains its
// resource budget until pending channel-open I/O has actually returned.
func (l *Lease) Cancel() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	close(l.done)
	resources := l.resources
	l.resources = nil
	l.mu.Unlock()
	for _, c := range resources {
		_ = c.Close()
	}
}

type Manager struct {
	mu                                       sync.RWMutex
	sessions                                 map[string]*Session
	byClient                                 map[string]string
	byFingerprint                            map[string]map[string]struct{}
	subs                                     map[uint64]chan Change
	nextSub, revision, generation, nextLease uint64
	leases                                   map[uint64]*Lease
	channels                                 map[string]int
	limits                                   Limits
	onRemove                                 func(Session, string)
}

func New(onRemove func(Session, string)) *Manager { return NewWithLimits(onRemove, DefaultLimits()) }
func NewWithLimits(onRemove func(Session, string), limits Limits) *Manager {
	return &Manager{sessions: map[string]*Session{}, byClient: map[string]string{}, byFingerprint: map[string]map[string]struct{}{}, subs: map[uint64]chan Change{}, leases: map[uint64]*Lease{}, channels: map[string]int{}, limits: limits, onRemove: onRemove}
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (m *Manager) AddAuthenticated(a Authenticated) (*Session, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if a.ConnectedAt.IsZero() {
		a.ConnectedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= m.limits.Sessions || len(m.byFingerprint[a.Fingerprint]) >= m.limits.PerFingerprint {
		return nil, ErrCapacity
	}
	s := &Session{ID: id, Fingerprint: a.Fingerprint, RemoteIP: a.RemoteIP, ConnectedAt: a.ConnectedAt.UTC(), State: Handshaking, closer: a.Closer}
	m.sessions[id] = s
	if m.byFingerprint[a.Fingerprint] == nil {
		m.byFingerprint[a.Fingerprint] = map[string]struct{}{}
	}
	m.byFingerprint[a.Fingerprint][id] = struct{}{}
	m.changedLocked()
	out := cloneSession(s)
	return &out, nil
}
func (m *Manager) BeginControl(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return ErrNotFound
	}
	if s.control {
		return ErrControlExists
	}
	s.control = true
	return nil
}
func (m *Manager) CompleteHello(id string, h Hello) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return ErrNotFound
	}
	if s.State != Handshaking {
		return ErrInvalidPublication
	}
	if owner, ok := m.byClient[h.ClientID]; ok && owner != id {
		return ErrDuplicateClient
	}
	s.ClientID = h.ClientID
	s.Name = h.Name
	s.Role = h.Role
	s.Version = h.Version
	s.State = Online
	m.byClient[h.ClientID] = id
	m.changedLocked()
	return nil
}

// Publish validates the complete candidate registry before atomically replacing
// a session's publications. Rejected updates leave the prior generation intact.
func (m *Manager) Publish(id string, tunnels []Tunnel) error {
	closeStale, err := m.PublishDeferred(id, tunnels)
	if closeStale != nil {
		closeStale()
	}
	return err
}

// PublishDeferred commits only memory. The caller closes stale channels after
// releasing any policy gate, so slow peers cannot block authorization changes.
func (m *Manager) PublishDeferred(id string, tunnels []Tunnel) (func(), error) {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil || s.State != Online {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	if len(tunnels) > m.limits.ExportsPerSession {
		m.mu.Unlock()
		return nil, ErrCapacity
	}
	candidate := append([]Tunnel(nil), tunnels...)
	seen := map[string]bool{}
	old := map[string]Tunnel{}
	for _, t := range s.Tunnels {
		old[t.ID] = t
	}
	next := m.generation
	for i, t := range candidate {
		if t.ID == "" || seen[t.ID] || t.SrcPort < 1 || t.SrcPort > 65535 || t.RemotePort != 0 {
			m.mu.Unlock()
			return nil, ErrInvalidPublication
		}
		seen[t.ID] = true
		previous, ok := old[t.ID]
		if ok && previous.Name == t.Name && previous.SrcHost == t.SrcHost && previous.SrcPort == t.SrcPort {
			candidate[i].Generation = previous.Generation
		} else {
			next++
			if next == 0 {
				m.mu.Unlock()
				return nil, ErrCapacity
			}
			candidate[i].Generation = next
		}
	}
	entries := m.registryLocked(id, candidate)
	if len(entries) > m.limits.ExportsTotal {
		m.mu.Unlock()
		return nil, ErrCapacity
	}
	encoded, err := json.Marshal(proto.Registry{V: proto.Version, Type: proto.TypeRegistry, Entries: entries})
	if err != nil || len(encoded)+1 > m.limits.RegistryBytes {
		m.mu.Unlock()
		return nil, ErrCapacity
	}
	s.Tunnels = candidate
	m.generation = next
	var cancelled []*Lease
	for _, l := range m.leases {
		if l.Owner.ID == id && !hasTarget(s, l.Target) {
			cancelled = append(cancelled, l)
		}
	}
	m.changedLocked()
	m.mu.Unlock()
	return func() {
		for _, l := range cancelled {
			l.Cancel()
		}
	}, nil
}
func hasTarget(s *Session, target proto.Target) bool {
	if s == nil || s.State != Online || s.ID != target.SessionID || s.ClientID != target.ClientID || s.Fingerprint != target.Fingerprint {
		return false
	}
	for _, t := range s.Tunnels {
		if t.ID == target.TunnelID && t.Generation == target.Generation {
			return true
		}
	}
	return false
}
func (m *Manager) Targets(id string) []proto.Target {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sessions[id]
	if s == nil {
		return nil
	}
	out := make([]proto.Target, 0, len(s.Tunnels))
	for _, t := range s.Tunnels {
		out = append(out, targetFor(s, t))
	}
	return out
}
func targetFor(s *Session, t Tunnel) proto.Target {
	return proto.Target{ClientID: s.ClientID, Fingerprint: s.Fingerprint, TunnelID: t.ID, SessionID: s.ID, Generation: t.Generation}
}
func (m *Manager) RegistrySnapshot() []proto.RegistryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registryLocked("", nil)
}
func (m *Manager) registryLocked(replaceID string, candidate []Tunnel) []proto.RegistryEntry {
	out := make([]proto.RegistryEntry, 0)
	for _, s := range m.sessions {
		if s.State != Online {
			continue
		}
		tunnels := s.Tunnels
		if s.ID == replaceID {
			tunnels = candidate
		}
		for _, t := range tunnels {
			out = append(out, proto.RegistryEntry{ID: s.ClientID, Name: s.Name, ClientVersion: s.Version, TunnelID: t.ID, SrcHost: t.SrcHost, SrcPort: t.SrcPort, TunnelName: t.Name, Since: s.ConnectedAt, Fingerprint: s.Fingerprint, SessionID: s.ID, Generation: t.Generation, IdentityVerified: true})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].TunnelID < out[j].TunnelID
		}
		return out[i].ID < out[j].ID
	})
	return out
}
func (m *Manager) Acquire(visitorID string, target proto.Target) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	visitor, owner := m.sessions[visitorID], m.sessions[target.SessionID]
	if visitor == nil || visitor.State != Online || !hasTarget(owner, target) {
		return nil, ErrInvalidTarget
	}
	if len(m.leases)*2+2 > m.limits.ChannelsTotal {
		return nil, ErrCapacity
	}
	needed := 1
	if visitorID == owner.ID {
		needed = 2
	}
	if m.channels[visitorID]+needed > m.limits.ChannelsPerSession || m.channels[owner.ID]+needed > m.limits.ChannelsPerSession {
		return nil, ErrCapacity
	}
	var tunnel Tunnel
	for _, t := range owner.Tunnels {
		if t.ID == target.TunnelID {
			tunnel = t
			break
		}
	}
	m.nextLease++
	id := m.nextLease
	l := &Lease{Visitor: cloneSession(visitor), Owner: cloneSession(owner), Tunnel: tunnel, Target: target, Connection: owner.closer, done: make(chan struct{})}
	m.channels[visitorID]++
	m.channels[owner.ID]++
	l.release = func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.leases[id]; !ok {
			return
		}
		delete(m.leases, id)
		for _, sid := range []string{visitorID, owner.ID} {
			m.channels[sid]--
			if m.channels[sid] == 0 {
				delete(m.channels, sid)
			}
		}
	}
	m.leases[id] = l
	return l, nil
}
func (m *Manager) Validate(l *Lease) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	visitor := m.sessions[l.Visitor.ID]
	if visitor == nil || visitor.State != Online || !hasTarget(m.sessions[l.Owner.ID], l.Target) {
		return false
	}
	select {
	case <-l.done:
		return false
	default:
		return true
	}
}
func (m *Manager) Get(id string) (Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, false
	}
	return cloneSession(s), true
}
func (m *Manager) Snapshot() []Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, cloneSession(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}
func (m *Manager) Remove(id, reason string) *Removal {
	m.mu.Lock()
	r := m.removeLocked(id, reason)
	hook := m.onRemove
	m.mu.Unlock()
	if r != nil && hook != nil {
		hook(r.Session, reason)
	}
	return r
}
func (m *Manager) removeLocked(id, reason string) *Removal {
	s := m.sessions[id]
	if s == nil {
		return nil
	}
	s.State = Closing
	r := &Removal{Session: cloneSession(s), Reason: reason, Closer: s.closer}
	delete(m.sessions, id)
	if s.ClientID != "" {
		delete(m.byClient, s.ClientID)
	}
	delete(m.byFingerprint[s.Fingerprint], id)
	if len(m.byFingerprint[s.Fingerprint]) == 0 {
		delete(m.byFingerprint, s.Fingerprint)
	}
	for _, l := range m.leases {
		if l.Owner.ID == id || l.Visitor.ID == id {
			r.leases = append(r.leases, l)
		}
	}
	m.changedLocked()
	return r
}
func (m *Manager) Disconnect(id, reason string) bool {
	r := m.Remove(id, reason)
	if r == nil {
		return false
	}
	r.Close()
	return true
}
func (m *Manager) DisconnectFingerprint(fp, reason string) int {
	rs := m.RemoveFingerprint(fp, reason)
	for _, r := range rs {
		r.Close()
	}
	return len(rs)
}
func (m *Manager) RemoveFingerprint(fp, reason string) []*Removal {
	m.mu.Lock()
	ids := make([]string, 0, len(m.byFingerprint[fp]))
	for id := range m.byFingerprint[fp] {
		ids = append(ids, id)
	}
	rs := make([]*Removal, 0, len(ids))
	for _, id := range ids {
		if r := m.removeLocked(id, reason); r != nil {
			rs = append(rs, r)
		}
	}
	hook := m.onRemove
	m.mu.Unlock()
	if hook != nil {
		for _, r := range rs {
			hook(r.Session, reason)
		}
	}
	return rs
}
func (m *Manager) CloseAll(reason string) int {
	xs := m.Snapshot()
	n := 0
	for _, s := range xs {
		if m.Disconnect(s.ID, reason) {
			n++
		}
	}
	return n
}
func (m *Manager) Subscribe() (<-chan Change, func()) {
	m.mu.Lock()
	m.nextSub++
	id := m.nextSub
	ch := make(chan Change, 1)
	m.subs[id] = ch
	m.mu.Unlock()
	var once sync.Once
	return ch, func() { once.Do(func() { m.mu.Lock(); delete(m.subs, id); m.mu.Unlock() }) }
}
func (m *Manager) changedLocked() {
	m.revision++
	c := Change{Revision: m.revision}
	for _, ch := range m.subs {
		select {
		case ch <- c:
		default:
		}
	}
}
func cloneSession(s *Session) Session {
	out := *s
	out.Tunnels = append([]Tunnel(nil), s.Tunnels...)
	out.closer = nil
	return out
}
