package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/proto"
	"tunnelx/internal/store"
)

func managementConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	host := filepath.Join(dir, "host_key")
	key := filepath.Join(dir, "client_key")
	auth := filepath.Join(dir, "authorized_keys")
	token := filepath.Join(dir, "admin.token")
	genKey(t, host)
	pub := genKey(t, key)
	if err := os.WriteFile(auth, pub, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{Addr: "127.0.0.1:0", HostKeyPath: host, AuthorizedKeys: auth, AdminAddr: "127.0.0.1:0", AdminTokenFile: token, DataDir: filepath.Join(dir, "data"), Version: "test"}
}

func TestManagementAddressMustBeExplicitLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		cfg := managementConfig(t)
		cfg.AdminAddr = addr
		s, e := New(cfg, nil)
		if e != nil {
			t.Fatalf("loopback %s rejected: %v", addr, e)
		}
		_ = s.Close()
	}
	for _, addr := range []string{":2223", "0.0.0.0:2223", "[::]:2223", "localhost:2223", "192.168.1.2:2223"} {
		cfg := managementConfig(t)
		cfg.AdminAddr = addr
		if s, e := New(cfg, nil); e == nil {
			_ = s.Close()
			t.Errorf("unsafe admin address %q accepted", addr)
		}
	}
}

func TestAdminTokenStrictValidation(t *testing.T) {
	cfg := managementConfig(t)
	if err := os.WriteFile(cfg.AdminTokenFile, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, e := New(cfg, nil); e == nil {
		_ = s.Close()
		t.Fatal("invalid token accepted")
	}
}

func TestManagementAPIAndShutdown(t *testing.T) {
	cfg := managementConfig(t)
	tunnelProbe, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg.Addr = tunnelProbe.Addr().String()
	tunnelProbe.Close()
	adminProbe, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg.AdminAddr = adminProbe.Addr().String()
	adminProbe.Close()
	s, e := New(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, "http://"+cfg.AdminAddr+"/api/v1/overview", nil)
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		resp, e := http.DefaultClient.Do(req)
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("admin status %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("admin not ready: %v", e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestOnlyOneControlChannel(t *testing.T) {
	client := connectTestServer(t)
	first, reqs, e := client.OpenChannel(proto.ChannelType, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	go ssh.DiscardRequests(reqs)
	if second, _, e := client.OpenChannel(proto.ChannelType, nil); e == nil {
		second.Close()
		t.Fatal("second control channel accepted")
	}
}

func TestCancelForwardClosesListener(t *testing.T) {
	client := connectTestServer(t)
	ln, e := client.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := ln.Addr().String()
	if e = ln.Close(); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, e := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if e != nil {
			return
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("cancel-forward left listener reachable")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRejectedAuthenticationAndFailedAdminActionAreAudited(t *testing.T) {
	cfg := managementConfig(t)
	s, e := New(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	fp := "SHA256:" + strings.Repeat("A", 43)
	s.recordRejected(fp, "127.0.0.1:1", "rejected")
	events, e := s.store.ListConnections(ctx, store.AuditFilter{Fingerprint: fp, Limit: 10})
	if e != nil || len(events) != 1 || events[0].Result != "rejected" {
		t.Fatalf("rejection audit=%+v err=%v", events, e)
	}
	b := (*adminBackend)(s)
	missing := "SHA256:" + strings.Repeat("B", 43)
	if e = b.UpdateClientNote(ctx, missing, "note", "reason"); !errors.Is(e, adminapi.ErrNotFound) {
		t.Fatalf("missing note error=%v", e)
	}
	actions, e := b.ListAdminActions(ctx, adminapi.ListQuery{Limit: 10, Fingerprint: missing})
	if e != nil || len(actions.Items) != 1 || actions.Items[0].Result != "failed" {
		t.Fatalf("filtered admin actions=%+v err=%v", actions, e)
	}
}

func TestHelloTimeoutClosesAuthenticatedConnection(t *testing.T) {
	cfg := managementConfig(t)
	cfg.AdminAddr = ""
	cfg.AdminTokenFile = ""
	cfg.HelloTimeout = 100 * time.Millisecond
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Addr = probe.Addr().String()
	_ = probe.Close()
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.ListenAndServe() }()
	t.Cleanup(func() { _ = s.Close(); <-serveDone })

	client := dialWith(t, cfg.Addr, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key"))
	waitDone := make(chan error, 1)
	go func() { waitDone <- client.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated connection without Hello was not closed")
	}
}

func TestMixedRolePublishAndCancelUpdatesRegistry(t *testing.T) {
	cfg := managementConfig(t)
	cfg.AdminAddr = ""
	cfg.AdminTokenFile = ""
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Addr = probe.Addr().String()
	_ = probe.Close()
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.ListenAndServe() }()
	t.Cleanup(func() { _ = s.Close(); <-serveDone })

	client := dialWith(t, cfg.Addr, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key"))
	pc, controlChannel := openTestControl(t, client, proto.RoleImporter, "mixed-role")
	defer controlChannel.Close()
	remote, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := remote.Addr().(*net.TCPAddr).Port
	publishTestPort(t, pc, port)
	if got := len(s.reg.Snapshot()); got != 1 {
		t.Fatalf("mixed-role publish produced %d registry entries, want 1", got)
	}
	overview, err := (*adminBackend)(s).Overview(context.Background())
	if err != nil || overview.Importers != 1 || overview.ActiveExporters != 1 {
		t.Fatalf("mixed-role overview=%+v err=%v", overview, err)
	}
	if err = remote.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(s.reg.Snapshot()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("cancel-forward left the published port in the registry")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuditCursorMergesConnectionAndAccessEvents(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if _, err = s.store.StartConnection(ctx, store.ConnectionAudit{Fingerprint: "SHA256:test", ClientID: "client", AuthenticatedAt: at, Result: "failed"}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.store.StartAccess(ctx, store.AccessAudit{VisitorFingerprint: "SHA256:test", VisitorClientID: "client", StartedAt: at, Result: "failed"}); err != nil {
			t.Fatal(err)
		}
	}
	backend := (*adminBackend)(s)
	first, err := backend.ListAuditEvents(ctx, adminapi.ListQuery{Limit: 2, Fingerprint: "SHA256:test", ClientID: "client", Result: "failed"})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	cursor, err := adminapi.DecodeCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backend.ListAuditEvents(ctx, adminapi.ListQuery{Limit: 2, Fingerprint: "SHA256:test", ClientID: "client", Result: "failed", Cursor: &cursor})
	if err != nil || len(second.Items) != 2 {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	seen := map[int64]bool{}
	for _, event := range append(first.Items, second.Items...) {
		if seen[event.ID] {
			t.Fatalf("duplicate event id %d across cursor pages", event.ID)
		}
		seen[event.ID] = true
	}
}
