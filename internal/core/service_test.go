package core

import (
	"path/filepath"
	"testing"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
)

func testService(t *testing.T) *Service {
	t.Helper()
	cfg := &config.Config{ID: "test-client", Name: "test", Tunnels: []config.Tunnel{}}
	cfg.SetPathForTest(filepath.Join(t.TempDir(), "config.json"))
	s, err := New(cfg, Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestServicePublishesLogAndTunnelEvents(t *testing.T) {
	s := testService(t)
	events, unsubscribe := s.Subscribe(8)
	defer unsubscribe()
	s.Log().Infof("test", "hello")
	if err := s.AddTunnel(config.Tunnel{Kind: config.KindExport, Name: "web", Enabled: false, LocalPort: 8080}); err != nil {
		t.Fatal(err)
	}
	want := map[EventKind]bool{EventLog: false, EventTunnels: false}
	deadline := time.After(2 * time.Second)
	for !want[EventLog] || !want[EventTunnels] {
		select {
		case event := <-events:
			want[event.Kind] = true
		case <-deadline:
			t.Fatalf("missing events: %#v", want)
		}
	}
	if got := s.Snapshot(logbuf.Debug); len(got.Tunnels) != 1 || got.Tunnels[0].Config.Name != "web" {
		t.Fatalf("unexpected snapshot: %#v", got.Tunnels)
	}
}

func TestServiceConfirmationCanBeResolvedExternally(t *testing.T) {
	s := testService(t)
	result := make(chan bool, 1)
	go func() { result <- s.confirmHostKey("example:2222", "SHA256:test") }()

	var pending Confirmation
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := s.Snapshot(logbuf.Debug)
		if len(snapshot.Pending) == 1 {
			pending = snapshot.Pending[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending.ID == 0 || pending.Fingerprint != "SHA256:test" {
		t.Fatalf("confirmation not exposed: %#v", pending)
	}
	if err := s.Confirm(pending.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case accepted := <-result:
		if !accepted {
			t.Fatal("confirmation was not accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("confirmation did not unblock")
	}
}

func TestTunnelIDSurvivesIndexChanges(t *testing.T) {
	s := testService(t)
	if err := s.AddTunnels([]config.Tunnel{
		{Kind: config.KindExport, Name: "first", LocalPort: 8081},
		{Kind: config.KindExport, Name: "second", LocalPort: 8082},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot(logbuf.Debug)
	firstID, secondID := snapshot.Tunnels[0].ID, snapshot.Tunnels[1].ID
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("invalid tunnel ids: %q %q", firstID, secondID)
	}
	if err := s.RemoveTunnelByID(firstID); err != nil {
		t.Fatal(err)
	}
	updated := snapshot.Tunnels[1].Config
	updated.Name = "renamed"
	if err := s.UpdateTunnelByID(secondID, updated); err != nil {
		t.Fatal(err)
	}
	final := s.Snapshot(logbuf.Debug)
	if len(final.Tunnels) != 1 || final.Tunnels[0].ID != secondID || final.Tunnels[0].Config.Name != "renamed" {
		t.Fatalf("stable id targeted wrong tunnel: %#v", final.Tunnels)
	}
	if err := s.RemoveTunnelByID(firstID); err == nil {
		t.Fatal("stale id unexpectedly removed a tunnel")
	}
}
