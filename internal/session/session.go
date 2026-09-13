// Package session owns the in-memory lifecycle of authenticated TunnelX SSH
// connections. It deliberately has no persistence dependency.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"sync"
	"time"
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
	ErrNotFound        = errors.New("session not found")
	ErrDuplicateClient = errors.New("client id is already online")
	ErrControlExists   = errors.New("control channel already exists")
	ErrPortOwned       = errors.New("remote port is already owned")
	ErrPortNotOwned    = errors.New("remote port is not owned by session")
)

type Tunnel struct {
	ID, Name, SrcHost   string
	SrcPort, RemotePort int
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

type Forward struct {
	Port                             int
	SessionID, Fingerprint, ClientID string
	Listener                         io.Closer
	Tunnel                           *Tunnel
}

type Change struct{ Revision uint64 }

type Removal struct {
	Session   Session
	Reason    string
	Listeners []io.Closer
	Closer    io.Closer
	once      sync.Once
}

// Close closes all resources detached by Remove. It is safe to call repeatedly.
func (r *Removal) Close() {
	if r != nil {
		r.once.Do(func() {
			for _, l := range r.Listeners {
				_ = l.Close()
			}
			if r.Closer != nil {
				_ = r.Closer.Close()
			}
		})
	}
}

type Manager struct {
	mu                sync.RWMutex
	sessions          map[string]*Session
	byClient          map[string]string
	byFingerprint     map[string]map[string]struct{}
	ports             map[int]Forward
	listeners         map[string]map[int]io.Closer
	subs              map[uint64]chan Change
	nextSub, revision uint64
	onRemove          func(Session, string)
}

func New(onRemove func(Session, string)) *Manager {
	return &Manager{sessions: map[string]*Session{}, byClient: map[string]string{}, byFingerprint: map[string]map[string]struct{}{}, ports: map[int]Forward{}, listeners: map[string]map[int]io.Closer{}, subs: map[uint64]chan Change{}, onRemove: onRemove}
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
	s := &Session{ID: id, Fingerprint: a.Fingerprint, RemoteIP: a.RemoteIP, ConnectedAt: a.ConnectedAt.UTC(), State: Handshaking, closer: a.Closer}
	m.mu.Lock()
	m.sessions[id] = s
	set := m.byFingerprint[a.Fingerprint]
	if set == nil {
		set = map[string]struct{}{}
		m.byFingerprint[a.Fingerprint] = set
	}
	set[id] = struct{}{}
	m.changedLocked()
	out := cloneSession(s)
	m.mu.Unlock()
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
	if owner, ok := m.byClient[h.ClientID]; ok && owner != id {
		return ErrDuplicateClient
	}
	if s.ClientID != "" {
		delete(m.byClient, s.ClientID)
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

func (m *Manager) Publish(id string, tunnels []Tunnel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return ErrNotFound
	}
	s.Tunnels = append([]Tunnel(nil), tunnels...)
	m.changedLocked()
	return nil
}

func (m *Manager) RegisterForward(id string, port int, listener io.Closer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return ErrNotFound
	}
	if _, ok := m.ports[port]; ok {
		return ErrPortOwned
	}
	f := Forward{Port: port, SessionID: id, Fingerprint: s.Fingerprint, ClientID: s.ClientID, Listener: listener}
	m.ports[port] = f
	ls := m.listeners[id]
	if ls == nil {
		ls = map[int]io.Closer{}
		m.listeners[id] = ls
	}
	ls[port] = listener
	m.changedLocked()
	return nil
}

func (m *Manager) CancelForward(id string, port int) (io.Closer, error) {
	m.mu.Lock()
	f, ok := m.ports[port]
	if !ok || f.SessionID != id {
		m.mu.Unlock()
		return nil, ErrPortNotOwned
	}
	delete(m.ports, port)
	delete(m.listeners[id], port)
	m.changedLocked()
	m.mu.Unlock()
	if f.Listener != nil {
		_ = f.Listener.Close()
	}
	return f.Listener, nil
}

// LookupPublishedPort returns an owner only when the port is both registered and published.
func (m *Manager) LookupPublishedPort(port int) (Forward, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.ports[port]
	if !ok {
		return Forward{}, false
	}
	s := m.sessions[f.SessionID]
	if s == nil || s.State != Online {
		return Forward{}, false
	}
	// A forward request may arrive before Hello. Resolve identity from the
	// current owner instead of the registration-time snapshot.
	f.Fingerprint = s.Fingerprint
	f.ClientID = s.ClientID
	for i := range s.Tunnels {
		if s.Tunnels[i].RemotePort == port {
			t := s.Tunnels[i]
			f.Tunnel = &t
			return f, true
		}
	}
	return Forward{}, false
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
	copyS := cloneSession(s)
	delete(m.sessions, id)
	if s.ClientID != "" {
		delete(m.byClient, s.ClientID)
	}
	set := m.byFingerprint[s.Fingerprint]
	delete(set, id)
	if len(set) == 0 {
		delete(m.byFingerprint, s.Fingerprint)
	}
	var ls []io.Closer
	for p, l := range m.listeners[id] {
		delete(m.ports, p)
		ls = append(ls, l)
	}
	delete(m.listeners, id)
	m.changedLocked()
	return &Removal{Session: copyS, Reason: reason, Listeners: ls, Closer: s.closer}
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
	removals := m.RemoveFingerprint(fp, reason)
	for _, r := range removals {
		r.Close()
	}
	return len(removals)
}

// RemoveFingerprint atomically detaches every session and listener owned by a
// fingerprint without closing resources. Callers can hold an admission gate,
// collect the removals, release the gate, then close them without blocking new
// policy operations.
func (m *Manager) RemoveFingerprint(fp, reason string) []*Removal {
	m.mu.Lock()
	set := m.byFingerprint[fp]
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	removals := make([]*Removal, 0, len(ids))
	for _, id := range ids {
		if r := m.removeLocked(id, reason); r != nil {
			removals = append(removals, r)
		}
	}
	hook := m.onRemove
	m.mu.Unlock()
	if hook != nil {
		for _, r := range removals {
			hook(r.Session, reason)
		}
	}
	return removals
}
func (m *Manager) CloseAll(reason string) int {
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	n := 0
	for _, id := range ids {
		if m.Disconnect(id, reason) {
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
	o := *s
	o.Tunnels = append([]Tunnel(nil), s.Tunnels...)
	o.closer = nil
	return o
}
