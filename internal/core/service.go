// Package core exposes TunnelX's headless application service.
// Frontends depend on this package instead of constructing manager.Manager directly.
package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/enrollment"
	"tunnelx/internal/keygen"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/manager"
	"tunnelx/internal/proto"
	"tunnelx/internal/sshconn"
	"tunnelx/internal/tunnel"
)

// ConfirmationHandler owns all interactions that cannot be decided by a daemon.
// A nil handler is fail-closed for unknown host keys and conservative for file writes.
type ConfirmationHandler interface {
	ConfirmHostKey(host, fingerprint string) bool
	FixKeyPermissions(path string, readers []string) bool
	ConfirmConfigOverwrite(path string) bool
}

type EventKind string

const (
	EventConnection   EventKind = "connection.changed"
	EventTunnels      EventKind = "tunnels.changed"
	EventRegistry     EventKind = "registry.changed"
	EventLog          EventKind = "log.appended"
	EventConfirmation EventKind = "confirmation.required"
)

type Event struct {
	Seq          uint64
	Time         time.Time
	Kind         EventKind
	Log          *logbuf.Entry
	Confirmation *Confirmation
}

type Confirmation struct {
	ID          uint64   `json:"id"`
	Kind        string   `json:"kind"`
	Host        string   `json:"host,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Path        string   `json:"path,omitempty"`
	Readers     []string `json:"readers,omitempty"`
	Message     string   `json:"message"`
	response    chan bool
}

type TunnelSnapshot struct {
	Index  int
	ID     string
	Config config.Tunnel
	Status tunnel.Status
}

type Snapshot struct {
	Version    string
	Config     config.Config
	Connection manager.ConnStatus
	Tunnels    []TunnelSnapshot
	Registry   []proto.RegistryEntry
	Logs       []logbuf.Entry
	Pending    []Confirmation
}

type Options struct {
	Version      string
	Log          *logbuf.Buffer
	Confirmation ConfirmationHandler
}

// Service is the single owner of client runtime state.
type Service struct {
	cfg     *config.Config
	log     *logbuf.Buffer
	mgr     *manager.Manager
	version string
	confirm ConfirmationHandler

	mu            sync.RWMutex
	ops           sync.Mutex
	seq           uint64
	nextSubID     uint64
	subs          map[uint64]chan Event
	pending       map[uint64]*Confirmation
	nextConfirmID uint64
	unsubLog      func()
	loginCancel   context.CancelFunc
	loginContext  context.Context
	loginSnapshot *Snapshot
}

func New(cfg *config.Config, opts Options) (*Service, error) {
	if cfg == nil {
		return nil, errors.New("配置不能为空")
	}
	log := opts.Log
	if log == nil {
		log = logbuf.New(nil)
	}
	s := &Service{
		cfg: cfg, log: log, version: opts.Version, confirm: opts.Confirmation,
		subs: make(map[uint64]chan Event), pending: make(map[uint64]*Confirmation),
	}
	s.mgr = manager.New(cfg, log, opts.Version, func() { s.publish(Event{Kind: EventTunnels}) })
	s.mgr.SetPrompts(s.confirmHostKey, s.fixKeyPermissions,
		func() { s.publish(Event{Kind: EventConnection}) },
		func() { s.publish(Event{Kind: EventRegistry}) })
	cfg.SetClobberPrompt(s.confirmOverwrite)
	s.unsubLog = log.Subscribe(func(entry logbuf.Entry) {
		e := entry
		s.publish(Event{Kind: EventLog, Log: &e})
	})
	return s, nil
}

func (s *Service) Start() {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mgr.Start()
}
func (s *Service) Stop() {
	s.mu.RLock()
	loginCancel := s.loginCancel
	s.mu.RUnlock()
	if loginCancel != nil {
		loginCancel()
	}
	s.cancelConfirmations()
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mgr.Stop()
}

// Close releases service subscriptions after stopping the runtime.
func (s *Service) Close() {
	s.Stop()
	if s.unsubLog != nil {
		s.unsubLog()
		s.unsubLog = nil
	}
	s.mu.Lock()
	for id, ch := range s.subs {
		close(ch)
		delete(s.subs, id)
	}
	s.mu.Unlock()
}

func (s *Service) Log() *logbuf.Buffer { return s.log }

// Persist saves generated identity/defaults before the first connection.
func (s *Service) Persist() error {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.cfg.Save()
}

func (s *Service) Snapshot(minLogLevel logbuf.Level) Snapshot {
	for {
		s.mu.RLock()
		if s.loginSnapshot != nil {
			snap := *s.loginSnapshot
			snap.Config = cloneConfig(&snap.Config)
			snap.Tunnels = append([]TunnelSnapshot(nil), snap.Tunnels...)
			snap.Registry = append([]proto.RegistryEntry(nil), snap.Registry...)
			snap.Pending = s.pendingLocked()
			s.mu.RUnlock()
			snap.Connection = s.mgr.ConnStatus()
			snap.Logs = s.log.Snapshot(minLogLevel)
			return snap
		}
		s.mu.RUnlock()
		if s.ops.TryLock() {
			break
		}
		// Do not enter an uninterruptible lock wait just before a login starts;
		// its confirmation must remain visible through the snapshot endpoint.
		time.Sleep(time.Millisecond)
	}
	defer s.ops.Unlock()
	return s.snapshotLocked(minLogLevel)
}

func (s *Service) snapshotLocked(minLogLevel logbuf.Level) Snapshot {
	snap := Snapshot{
		Version:    s.version,
		Config:     cloneConfig(s.cfg),
		Connection: s.mgr.ConnStatus(),
		Registry:   s.mgr.Registry(),
		Logs:       s.log.Snapshot(minLogLevel),
	}
	for i, t := range s.mgr.Tunnels() {
		snap.Tunnels = append(snap.Tunnels, TunnelSnapshot{
			Index: i, ID: t.Config().ID, Config: t.Config(), Status: t.Status(),
		})
	}
	s.mu.RLock()
	snap.Pending = s.pendingLocked()
	s.mu.RUnlock()
	return snap
}

func (s *Service) pendingLocked() []Confirmation {
	var out []Confirmation
	for _, pending := range s.pending {
		item := *pending
		item.response = nil
		out = append(out, item)
	}
	return out
}

// Login keeps interactive confirmations and snapshots available while serializing
// credential enrollment against settings changes and connection lifecycle actions.
func (s *Service) Login(ctx context.Context, username, password string) error {
	if !enrollment.ValidUsername(enrollment.NormalizeUsername(username)) || !enrollment.ValidLoginPassword(password) {
		return errors.New("请输入有效账号和密码")
	}
	s.cancelConfirmations()
	s.ops.Lock()
	defer s.ops.Unlock()
	ctx, cancel := context.WithTimeout(ctx, sshconn.EnrollmentTimeout)
	defer cancel()
	snapshot := s.snapshotLocked(logbuf.Info)
	s.mu.Lock()
	s.loginContext, s.loginCancel, s.loginSnapshot = ctx, cancel, &snapshot
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.loginContext, s.loginCancel, s.loginSnapshot = nil, nil, nil
		s.mu.Unlock()
	}()
	return s.mgr.Login(ctx, username, password)
}

func cloneConfig(cfg *config.Config) config.Config {
	out := *cfg
	out.Tunnels = append([]config.Tunnel(nil), cfg.Tunnels...)
	return out
}

func (s *Service) AddTunnel(tc config.Tunnel) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.mgr.AddTunnel(tc)
}
func (s *Service) AddTunnels(tc []config.Tunnel) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.mgr.AddTunnels(tc)
}
func (s *Service) UpdateTunnel(i int, tc config.Tunnel) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.mgr.UpdateTunnel(i, tc)
}
func (s *Service) RemoveTunnel(i int) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.mgr.RemoveTunnel(i)
}
func (s *Service) RemoveTunnels(i []int) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.mgr.RemoveTunnels(i)
}

func (s *Service) UpdateTunnelByID(id string, tc config.Tunnel) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	i := s.indexByID(id)
	if i < 0 {
		return errors.New("隧道不存在")
	}
	tc.ID = id
	return s.mgr.UpdateTunnel(i, tc)
}

func (s *Service) RemoveTunnelByID(id string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	i := s.indexByID(id)
	if i < 0 {
		return errors.New("隧道不存在")
	}
	return s.mgr.RemoveTunnel(i)
}

func (s *Service) RemoveTunnelsByID(ids []string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	indices := make([]int, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		i := s.indexByID(id)
		if i < 0 {
			return errors.New("一条或多条隧道已不存在")
		}
		indices = append(indices, i)
	}
	return s.mgr.RemoveTunnels(indices)
}

func (s *Service) indexByID(id string) int {
	for i, item := range s.mgr.Tunnels() {
		if item.Config().ID == id {
			return i
		}
	}
	return -1
}
func (s *Service) ClearLogs() {
	s.log.Clear()
	s.publish(Event{Kind: EventLog})
}
func (s *Service) UsedListenPorts() map[int]bool {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.mgr.UsedListenPorts()
}

// UpdateSettings persists connection settings and rebuilds an active connection so
// runtime state never diverges from the saved configuration.
func (s *Service) UpdateSettings(name, addr, keyPath string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	wasRunning := s.mgr.ConnStatus().State != manager.ConnIdle
	if wasRunning {
		s.cancelConfirmations()
		s.mgr.Stop()
	}
	oldName, oldAddr, oldKeyPath := s.cfg.Name, s.cfg.ServerAddr, s.cfg.KeyPath
	if name != "" {
		s.cfg.Name = name
	}
	s.cfg.ServerAddr = addr
	s.cfg.KeyPath = keyPath
	if err := s.cfg.Save(); err != nil {
		s.cfg.Name, s.cfg.ServerAddr, s.cfg.KeyPath = oldName, oldAddr, oldKeyPath
		if wasRunning {
			s.mgr.Start()
		}
		return err
	}
	if wasRunning {
		s.mgr.Start()
	}
	s.publish(Event{Kind: EventTunnels})
	return nil
}

// GenerateKey creates a key at the path currently entered in connection settings.
// Relative paths are resolved from the configuration directory.
func (s *Service) GenerateKey(keyPath, username, email string) (keygen.Result, keygen.Metadata, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	if keyPath == "" {
		keyPath = config.DefaultKeyName
	}
	resolved := keyPath
	if !filepath.IsAbs(resolved) {
		dir, err := s.cfg.BaseDir()
		if err != nil {
			return keygen.Result{}, keygen.Metadata{}, err
		}
		resolved = filepath.Join(dir, resolved)
	}
	metadata, err := keygen.NewMetadata(username, email)
	if err != nil {
		return keygen.Result{}, keygen.Metadata{}, err
	}
	result, err := keygen.GenerateWithMetadata(filepath.Dir(resolved), filepath.Base(resolved), metadata)
	return result, metadata, err
}

// Subscribe returns an ordered, best-effort event stream. Events are dropped for a
// slow frontend; sequence gaps tell it to recover by requesting Snapshot.
func (s *Service) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 64
	}
	s.mu.Lock()
	s.nextSubID++
	id := s.nextSubID
	ch := make(chan Event, buffer)
	s.subs[id] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if current, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(current)
		}
		s.mu.Unlock()
	}
}

func (s *Service) publish(event Event) {
	s.mu.Lock()
	s.publishLocked(event)
	s.mu.Unlock()
}

func (s *Service) publishLocked(event Event) {
	s.seq++
	event.Seq = s.seq
	event.Time = time.Now()
	for _, ch := range s.subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Service) confirmHostKey(host, fingerprint string) bool {
	if s.confirm != nil {
		return s.confirm.ConfirmHostKey(host, fingerprint)
	}
	return s.awaitConfirmation(Confirmation{Kind: "host_key", Host: host,
		Fingerprint: fingerprint, Message: "确认是否信任该 SSH 主机密钥"})
}

func (s *Service) fixKeyPermissions(path string, readers []string) bool {
	if s.confirm != nil {
		s.mu.RLock()
		ctx := s.loginContext
		s.mu.RUnlock()
		if ctx == nil {
			return s.confirm.FixKeyPermissions(path, readers)
		}
		answer := make(chan bool, 1)
		go func() { answer <- s.confirm.FixKeyPermissions(path, readers) }()
		select {
		case accept := <-answer:
			return accept
		case <-ctx.Done():
			return false
		}
	}
	return s.awaitConfirmation(Confirmation{Kind: "key_permissions", Path: path,
		Readers: append([]string(nil), readers...), Message: "私钥权限过宽，是否自动修复"})
}

func (s *Service) confirmOverwrite(path string) bool {
	if s.confirm != nil {
		return s.confirm.ConfirmConfigOverwrite(path)
	}
	return s.awaitConfirmation(Confirmation{Kind: "config_overwrite", Path: path,
		Message: "配置文件在运行期间出现，是否允许覆盖"})
}

func (s *Service) awaitConfirmation(item Confirmation) bool {
	s.mu.Lock()
	ctx := s.loginContext
	if ctx == nil {
		ctx = context.Background()
	}
	s.nextConfirmID++
	item.ID = s.nextConfirmID
	item.response = make(chan bool, 1)
	s.pending[item.ID] = &item
	public := item
	public.response = nil
	s.publishLocked(Event{Kind: EventConfirmation, Confirmation: &public})
	s.mu.Unlock()
	var answer bool
	select {
	case answer = <-item.response:
	case <-ctx.Done():
	}
	s.mu.Lock()
	delete(s.pending, item.ID)
	s.mu.Unlock()
	return answer
}

func (s *Service) Confirm(id uint64, accept bool) error {
	s.mu.RLock()
	item := s.pending[id]
	s.mu.RUnlock()
	if item == nil {
		return errors.New("确认请求不存在或已处理")
	}
	select {
	case item.response <- accept:
		return nil
	default:
		return errors.New("确认请求已处理")
	}
}

func (s *Service) cancelConfirmations() {
	s.mu.RLock()
	items := make([]*Confirmation, 0, len(s.pending))
	for _, item := range s.pending {
		items = append(items, item)
	}
	s.mu.RUnlock()
	for _, item := range items {
		select {
		case item.response <- false:
		default:
		}
	}
}
