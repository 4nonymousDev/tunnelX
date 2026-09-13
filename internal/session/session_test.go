package session

import (
	"sync"
	"sync/atomic"
	"testing"
)

type countCloser struct{ n atomic.Int32 }

func (c *countCloser) Close() error { c.n.Add(1); return nil }
func TestLifecycleAndPublishedOwnership(t *testing.T) {
	var audits atomic.Int32
	m := New(func(Session, string) { audits.Add(1) })
	c := &countCloser{}
	s, err := m.AddAuthenticated(Authenticated{Fingerprint: "SHA256:x", Closer: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ID) != 32 || s.State != Handshaking {
		t.Fatalf("bad session: %#v", s)
	}
	if err = m.BeginControl(s.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.CompleteHello(s.ID, Hello{ClientID: "c", Role: "exporter"}); err != nil {
		t.Fatal(err)
	}
	l := &countCloser{}
	if err = m.RegisterForward(s.ID, 1234, l); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.LookupPublishedPort(1234); ok {
		t.Fatal("unpublished port visible")
	}
	_ = m.Publish(s.ID, []Tunnel{{ID: "t", RemotePort: 1234}})
	if _, ok := m.LookupPublishedPort(1234); !ok {
		t.Fatal("published port absent")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Disconnect(s.ID, "test") }()
	}
	wg.Wait()
	if audits.Load() != 1 || c.n.Load() != 1 || l.n.Load() != 1 {
		t.Fatalf("audits=%d conn=%d listener=%d", audits.Load(), c.n.Load(), l.n.Load())
	}
}
func TestDuplicateClient(t *testing.T) {
	m := New(nil)
	a, _ := m.AddAuthenticated(Authenticated{Fingerprint: "a"})
	b, _ := m.AddAuthenticated(Authenticated{Fingerprint: "b"})
	if err := m.CompleteHello(a.ID, Hello{ClientID: "same"}); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteHello(b.ID, Hello{ClientID: "same"}); err != ErrDuplicateClient {
		t.Fatalf("got %v", err)
	}
}

func TestForwardRegisteredBeforeHelloGetsCurrentIdentity(t *testing.T) {
	m := New(nil)
	s, _ := m.AddAuthenticated(Authenticated{Fingerprint: "SHA256:owner"})
	if err := m.RegisterForward(s.ID, 1234, &countCloser{}); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteHello(s.ID, Hello{ClientID: "client-after-forward"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(s.ID, []Tunnel{{ID: "t", RemotePort: 1234}}); err != nil {
		t.Fatal(err)
	}
	f, ok := m.LookupPublishedPort(1234)
	if !ok || f.ClientID != "client-after-forward" || f.Fingerprint != "SHA256:owner" {
		t.Fatalf("forward=%#v ok=%v", f, ok)
	}
}

func TestRemoveFingerprintDetachesBeforeClosing(t *testing.T) {
	var audits atomic.Int32
	m := New(func(Session, string) { audits.Add(1) })
	conn1, conn2 := &countCloser{}, &countCloser{}
	s1, _ := m.AddAuthenticated(Authenticated{Fingerprint: "SHA256:same", Closer: conn1})
	s2, _ := m.AddAuthenticated(Authenticated{Fingerprint: "SHA256:same", Closer: conn2})
	listener := &countCloser{}
	if err := m.RegisterForward(s1.ID, 2345, listener); err != nil {
		t.Fatal(err)
	}
	removals := m.RemoveFingerprint("SHA256:same", "blocked")
	if len(removals) != 2 || len(m.Snapshot()) != 0 || audits.Load() != 2 {
		t.Fatalf("removals=%d sessions=%d audits=%d", len(removals), len(m.Snapshot()), audits.Load())
	}
	if conn1.n.Load() != 0 || conn2.n.Load() != 0 || listener.n.Load() != 0 {
		t.Fatal("resources closed during detach")
	}
	for _, r := range removals {
		r.Close()
		r.Close()
	}
	if conn1.n.Load() != 1 || conn2.n.Load() != 1 || listener.n.Load() != 1 {
		t.Fatal("resources not closed exactly once")
	}
	if again := m.RemoveFingerprint("SHA256:same", "again"); len(again) != 0 || audits.Load() != 2 {
		t.Fatalf("second removals=%d audits=%d", len(again), audits.Load())
	}
	_ = s2
}
