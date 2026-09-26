package session

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"tunnelx/internal/proto"
)

type countCloser struct{ n atomic.Int32 }

func (c *countCloser) Close() error { c.n.Add(1); return nil }
func online(t *testing.T, m *Manager, fp, id string, c *countCloser) *Session {
	t.Helper()
	s, err := m.AddAuthenticated(Authenticated{Fingerprint: fp, Closer: c})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.CompleteHello(s.ID, Hello{ClientID: id, Role: "exporter"}); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestLifecycleAndPublishedOwnership(t *testing.T) {
	var audits atomic.Int32
	m := New(func(Session, string) { audits.Add(1) })
	c := &countCloser{}
	owner := online(t, m, "fp", "owner", c)
	if err := m.BeginControl(owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.BeginControl(owner.ID); err != ErrControlExists {
		t.Fatal(err)
	}
	if err := m.Publish(owner.ID, []Tunnel{{ID: "t", SrcPort: 80}}); err != nil {
		t.Fatal(err)
	}
	target := m.Targets(owner.ID)[0]
	lease, err := m.Acquire(owner.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	resource := &countCloser{}
	lease.Attach(resource)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Disconnect(owner.ID, "test") }()
	}
	wg.Wait()
	if audits.Load() != 1 || c.n.Load() != 1 || resource.n.Load() != 1 {
		t.Fatalf("audit=%d conn=%d channel=%d", audits.Load(), c.n.Load(), resource.n.Load())
	}
	late := &countCloser{}
	if lease.Attach(late) || late.n.Load() != 1 {
		t.Fatal("late channel accepted")
	}
	if _, err = m.Acquire(owner.ID, target); err == nil {
		t.Fatal("stale target accepted")
	}
}
func TestDuplicateClient(t *testing.T) {
	m := New(nil)
	online(t, m, "a", "same", &countCloser{})
	b, _ := m.AddAuthenticated(Authenticated{Fingerprint: "b"})
	if err := m.CompleteHello(b.ID, Hello{ClientID: "same"}); err != ErrDuplicateClient {
		t.Fatal(err)
	}
}
func TestGenerationRejectsStaleAndForgedTarget(t *testing.T) {
	m := New(nil)
	s := online(t, m, "fp", "owner", &countCloser{})
	tunnels := []Tunnel{{ID: "t", SrcPort: 80}}
	if err := m.Publish(s.ID, tunnels); err != nil {
		t.Fatal(err)
	}
	first := m.Targets(s.ID)[0]
	if err := m.Publish(s.ID, tunnels); err != nil {
		t.Fatal(err)
	}
	if m.Targets(s.ID)[0] != first {
		t.Fatal("unchanged publication changed generation")
	}
	lease, err := m.Acquire(s.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	closeStale, err := m.PublishDeferred(s.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Validate(lease) {
		t.Fatal("detached target still valid")
	}
	closeStale()
	select {
	case <-lease.Done():
	default:
		t.Fatal("stale channel not cancelled")
	}
	if err = m.Publish(s.ID, tunnels); err != nil {
		t.Fatal(err)
	}
	second := m.Targets(s.ID)[0]
	if second.Generation == first.Generation {
		t.Fatal("removed generation reused")
	}
	forged := []proto.Target{first, second, second, second, second}
	forged[1].Fingerprint = "other"
	forged[2].ClientID = "other"
	forged[3].SessionID = "other"
	forged[4].TunnelID = "other"
	for _, target := range forged {
		if _, err = m.Acquire(s.ID, target); !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("forged target accepted: %+v %v", target, err)
		}
	}
}
func TestRegistryEncodedBudgetIsAtomic(t *testing.T) {
	limits := DefaultLimits()
	limits.RegistryBytes = 1000
	m := NewWithLimits(nil, limits)
	s := online(t, m, "fp", "owner", &countCloser{})
	if err := m.Publish(s.ID, []Tunnel{{ID: "t", SrcPort: 80}}); err != nil {
		t.Fatal(err)
	}
	before := m.Targets(s.ID)[0]
	if err := m.Publish(s.ID, []Tunnel{{ID: "t", Name: strings.Repeat("\x00", 200), SrcPort: 80}}); err != ErrCapacity {
		t.Fatalf("got %v", err)
	}
	if m.Targets(s.ID)[0] != before {
		t.Fatal("failed update changed state")
	}
	b, _ := json.Marshal(proto.Registry{V: proto.Version, Type: proto.TypeRegistry, Entries: m.RegistrySnapshot()})
	if len(b)+1 > limits.RegistryBytes {
		t.Fatal("registry exceeds wire budget")
	}
}
func TestChannelCapacityAndRelease(t *testing.T) {
	limits := DefaultLimits()
	limits.ChannelsTotal = 2
	limits.ChannelsPerSession = 2
	m := NewWithLimits(nil, limits)
	s := online(t, m, "fp", "owner", &countCloser{})
	_ = m.Publish(s.ID, []Tunnel{{ID: "t", SrcPort: 80}})
	target := m.Targets(s.ID)[0]
	l, err := m.Acquire(s.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Acquire(s.ID, target); err != ErrCapacity {
		t.Fatal(err)
	}
	l.Close()
	next, err := m.Acquire(s.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
func TestRemoveFingerprintDetachesBeforeClosing(t *testing.T) {
	m := New(nil)
	c1, c2 := &countCloser{}, &countCloser{}
	online(t, m, "same", "one", c1)
	online(t, m, "same", "two", c2)
	rs := m.RemoveFingerprint("same", "blocked")
	if len(rs) != 2 || len(m.Snapshot()) != 0 || c1.n.Load() != 0 || c2.n.Load() != 0 {
		t.Fatal("detach was not atomic or performed I/O")
	}
	for _, r := range rs {
		r.Close()
		r.Close()
	}
	if c1.n.Load() != 1 || c2.n.Load() != 1 {
		t.Fatal("close not idempotent")
	}
}
