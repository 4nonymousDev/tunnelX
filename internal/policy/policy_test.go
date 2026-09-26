package policy

import (
	"errors"
	"sync"
	"testing"
	"time"
	"tunnelx/internal/store"
)

func TestPersistenceDoesNotHoldAdmissionLockAndInvalidatesStaleChecks(t *testing.T) {
	p := New(nil)
	before := p.Version()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- p.PersistChange("target", func() error { close(entered); <-release; return nil }, func(s *Snapshot) { s.Set(Block{Fingerprint: "target"}) })
	}()
	<-entered
	checked := make(chan bool, 1)
	go func() { ok, _ := p.Admit("unrelated", nil); checked <- ok }()
	select {
	case ok := <-checked:
		if !ok {
			t.Fatal("unrelated denied")
		}
	case <-time.After(time.Second):
		t.Fatal("persistence held global admission lock")
	}
	if ok, _ := p.Admit("target", nil); ok {
		t.Fatal("pending target admitted")
	}
	if ok, _ := p.AdmitVersion("unrelated", before, nil); ok {
		t.Fatal("stale validation admitted")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !p.Blocked("target") {
		t.Fatal("block not committed")
	}
	err := p.PersistChange("new", func() error { return errors.New("disk failure") }, func(s *Snapshot) { s.Set(Block{Fingerprint: "new"}) })
	if err == nil || p.Blocked("new") {
		t.Fatal("failed persistence altered policy")
	}
}

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

func TestRotationGatesBothFingerprintsAndRejectsChecksMadeDuringPersistence(t *testing.T) {
	p := New(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- p.PersistChanges([]string{"old", "new"}, func() error { close(entered); <-release; return nil }, nil)
	}()
	<-entered
	during := p.Version()
	for _, fp := range []string{"old", "new"} {
		if ok, _ := p.AdmitVersion(fp, during, nil); ok {
			t.Fatalf("rotation admitted %s", fp)
		}
	}
	if ok, _ := p.AdmitVersion("unrelated", during, nil); !ok {
		t.Fatal("unrelated identity was unavailable")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.AdmitVersion("old", during, nil); ok {
		t.Fatal("validation made during persistence remained valid after commit")
	}
	if ok, _ := p.AdmitVersion("new", p.Version(), nil); !ok {
		t.Fatal("new validation denied after successful commit")
	}
}

func TestUncertainCommitStopsAllFurtherAdmissions(t *testing.T) {
	p := New(nil)
	err := p.PersistChanges([]string{"old", "new"}, func() error { return store.ErrCommitOutcomeUnknown }, func(*Snapshot) { t.Fatal("uncertain commit applied success callback") })
	if !errors.Is(err, store.ErrCommitOutcomeUnknown) {
		t.Fatal(err)
	}
	for _, fp := range []string{"old", "new", "unrelated"} {
		if ok, _ := p.AdmitVersion(fp, p.Version(), nil); ok {
			t.Fatalf("uncertain commit admitted %s", fp)
		}
	}
}
