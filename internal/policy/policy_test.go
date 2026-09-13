package policy

import (
	"sync"
	"testing"
	"time"
)

func TestExpiryAndAdmissionGate(t *testing.T) {
	now := time.Unix(100, 0)
	exp := now
	p := New([]Block{{Fingerprint: "expired", ExpiresAt: &exp}, {Fingerprint: "live"}})
	p.now = func() time.Time { return now }
	if p.Blocked("expired") || !p.Blocked("live") {
		t.Fatal("expiry mismatch")
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		p.Change(func(s *Snapshot) error { close(entered); <-release; s.Set(Block{Fingerprint: "new"}); return nil })
		close(done)
	}()
	<-entered
	var wg sync.WaitGroup
	wg.Add(1)
	var admitted bool
	go func() { defer wg.Done(); admitted, _ = p.Admit("new", nil) }()
	close(release)
	<-done
	wg.Wait()
	if admitted {
		t.Fatal("admission escaped concurrent block")
	}
}
