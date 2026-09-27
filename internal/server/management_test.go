package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
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

func TestLegacyAdminTokenIsIgnoredAndCannotAuthenticate(t *testing.T) {
	for _, present := range []bool{false, true} {
		cfg := managementConfig(t)
		if present {
			if err := os.WriteFile(cfg.AdminTokenFile, []byte("short"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(cfg.AdminTokenFile); err != nil {
			t.Fatal(err)
		}
		s, err := New(cfg, nil)
		if err != nil {
			t.Fatalf("legacy token must not affect startup: %v", err)
		}
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:2223/api/v1/overview", nil)
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		w := httptest.NewRecorder()
		s.adminAPI.ServeHTTP(w, r)
		_ = s.Close()
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("legacy Bearer authenticated: %d", w.Code)
		}
	}
}

func TestImportPublicKeyPersistsAuthorizationAndMetadata(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	publicKey := genKey(t, filepath.Join(filepath.Dir(cfg.AuthorizedKeys), "imported_key"))
	result, err := (*adminBackend)(s).ImportPublicKey(context.Background(), adminapi.ImportPublicKeyRequest{
		ClientID:     "original-device",
		PublicKey:    strings.TrimSpace(string(publicKey)),
		Username:     "alice",
		Email:        "alice@example.com",
		ComputerName: "DEV-PC",
		Reason:       "onboarding",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.auth.AuthorizedFingerprint(result.Fingerprint) {
		t.Fatal("imported key is not authorized")
	}
	if result.ClientID != "original-device" {
		t.Fatalf("registered wrong identity: %#v", result)
	}
	if err = s.store.ValidateIdentity(context.Background(), result.Fingerprint, result.ClientID); err != nil {
		t.Fatalf("registration still needs a separate binding step: %v", err)
	}
	client, err := s.store.GetClient(context.Background(), result.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if client.Username != "alice" || client.Email != "alice@example.com" || client.ComputerName != "DEV-PC" {
		t.Fatalf("client=%#v", client)
	}
	actions, err := s.store.ListAdminActions(context.Background(), 0, 10)
	if err != nil || len(actions) != 1 || actions[0].Action != "register_client" || actions[0].Result != "success" {
		t.Fatalf("actions=%#v err=%v", actions, err)
	}
}

func TestManagementExplicitIdentityRotationDisconnectsBothKeysAndRevokes(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	b := (*adminBackend)(s)
	oldFP := ssh.FingerprintSHA256(loadSigner(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")).PublicKey())
	newKey := genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "replacement_key"))
	newClient, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "replacement-device", PublicKey: string(newKey), Reason: "approved replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.RecordIdentityClaim(ctx, oldFP, "device-verified", "untrusted claim", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err = s.store.ValidateIdentity(ctx, oldFP, "device-verified"); !errors.Is(err, store.ErrIdentityUnbound) {
		t.Fatal("candidate automatically trusted")
	}
	first, err := b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: "device-verified", Fingerprint: oldFP, Reason: "verified existing public key"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{oldFP, newClient.Fingerprint} {
		if _, err = s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: fp}); err != nil {
			t.Fatal(err)
		}
	}
	stale := s.policy.Version()
	rotated, err := b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: first.ClientID, Fingerprint: newClient.Fingerprint, ExpectedGeneration: first.Generation, Reason: "verified replacement key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.sessions.Snapshot()) != 0 {
		t.Fatal("rotation retained a session from old or replacement fingerprint")
	}
	if ok, _ := s.policy.AdmitVersion(oldFP, stale, nil); ok {
		t.Fatal("pre-rotation validation was reusable")
	}
	if err = s.store.ValidateIdentity(ctx, oldFP, first.ClientID); !errors.Is(err, store.ErrIdentityMismatch) {
		t.Fatalf("old key remained trusted: %v", err)
	}
	if err = s.store.ValidateIdentity(ctx, newClient.Fingerprint, first.ClientID); err != nil {
		t.Fatal(err)
	}
	if err = b.RevokeIdentity(ctx, rotated.ClientID, rotated.Generation, "device retired"); err != nil {
		t.Fatal(err)
	}
	if err = s.store.ValidateIdentity(ctx, newClient.Fingerprint, first.ClientID); !errors.Is(err, store.ErrIdentityRevoked) {
		t.Fatal("revoked identity accepted")
	}
	if _, err = b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: first.ClientID, Fingerprint: oldFP, ExpectedGeneration: first.Generation, Reason: "stale change"}); !errors.Is(err, adminapi.ErrConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
}

func TestManagementRejectsAuthorizedKeyOptionsBeforeMutation(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, err := os.ReadFile(cfg.AuthorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	pub := genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "option_key"))
	_, err = (*adminBackend)(s).ImportPublicKey(context.Background(), adminapi.ImportPublicKeyRequest{ClientID: "device", PublicKey: `from="192.0.2.1" ` + string(pub), Reason: "reject unsupported semantics"})
	if err == nil {
		t.Fatal("unsupported key restrictions silently ignored")
	}
	after, err := os.ReadFile(cfg.AuthorizedKeys)
	if err != nil || string(before) != string(after) {
		t.Fatal("invalid input changed authorization file")
	}
	ops, err := s.store.ListAdminOperations(context.Background(), 10)
	if err != nil || len(ops) != 0 {
		t.Fatal("invalid input created mutation intent")
	}
}

func TestManagementUncertainCommitDisconnectsAffectedSessionsAndPauses(t *testing.T) {
	for _, change := range []string{"block", "unblock", "bind", "revoke"} {
		t.Run(change, func(t *testing.T) {
			cfg := managementConfig(t)
			s, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			b := (*adminBackend)(s)
			fp := ssh.FingerprintSHA256(loadSigner(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")).PublicKey())
			binding, err := b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: "verified", Fingerprint: fp, Reason: "verified existing key"})
			if err != nil {
				t.Fatal(err)
			}
			newKey := genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "replacement_key"))
			other, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "replacement-device", PublicKey: string(newKey), Reason: "verified replacement"})
			if err != nil {
				t.Fatal(err)
			}
			if change == "unblock" {
				if err = b.BlockClient(ctx, fp, adminapi.BlockRequest{Reason: "previous block"}); err != nil {
					t.Fatal(err)
				}
			}
			for _, key := range []string{fp, other.Fingerprint} {
				if _, err = s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: key}); err != nil {
					t.Fatal(err)
				}
			}
			for _, q := range []string{`CREATE TABLE commit_fault(fingerprint TEXT REFERENCES clients(fingerprint) DEFERRABLE INITIALLY DEFERRED)`, `CREATE TRIGGER fail_commit BEFORE INSERT ON admin_actions BEGIN INSERT INTO commit_fault VALUES('missing-client'); END`} {
				if _, err = s.store.DB().Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "block":
				err = b.BlockClient(ctx, fp, adminapi.BlockRequest{Reason: "new block"})
			case "unblock":
				err = b.UnblockClient(ctx, fp, "verified unblock")
			case "bind":
				_, err = b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: "verified", Fingerprint: other.Fingerprint, ExpectedGeneration: binding.Generation, Reason: "verified rotation"})
			case "revoke":
				err = b.RevokeIdentity(ctx, "verified", binding.Generation, "retired")
			}
			if !errors.Is(err, store.ErrCommitOutcomeUnknown) {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, active := range s.sessions.Snapshot() {
				if active.Fingerprint == fp || change == "bind" {
					t.Fatalf("affected session survived uncertain commit: %+v", active)
				}
			}
			if ok, _ := s.policy.Admit("unrelated", nil); ok {
				t.Fatal("new admissions continued")
			}
			overview, e := b.Overview(ctx)
			if e != nil || overview.StorageHealthy || overview.StorageStatus != "commit_uncertain" {
				t.Fatalf("missing management alert: %+v %v", overview, e)
			}
		})
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
	defer s.Close()
	if _, e = s.store.ProvisionAdminAccount(context.Background(), "admin", "test administrator password", store.AdminAction{Operator: "local-admin", Reason: "test initialization"}); e != nil {
		t.Fatal(e)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		origin := "http://" + cfg.AdminAddr
		req, _ := http.NewRequest(http.MethodPost, origin+"/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"test administrator password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		resp, e := client.Do(req)
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("admin login status %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("admin not ready: %v", e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, endpoint := range []string{"/api/v1/auth/session", "/api/v1/overview"} {
		resp, err := client.Get("http://" + cfg.AdminAddr + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("cookie session %s status %d", endpoint, resp.StatusCode)
		}
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

func TestLegacyForwardListenerRejected(t *testing.T) {
	client := connectTestServer(t)
	if ln, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		ln.Close()
		t.Fatal("legacy TCP forwarding must not allocate a listener in v2")
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
	if e != nil || len(events) != 0 {
		t.Fatalf("anonymous rejection created per-attempt records: %+v err=%v", events, e)
	}
	if n, _, err := s.store.TodayRejected(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("rejection aggregate=%d err=%v", n, err)
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
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := startTestListener(t, s)

	client := dialWith(t, addr, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key"))
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
	cfg.AdminAddr, cfg.AdminTokenFile = "", ""
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")
	fp := ssh.FingerprintSHA256(loadSigner(t, keyPath).PublicKey())
	if _, err = s.store.BindIdentity(context.Background(), "mixed-role", fp, 0, store.AdminAction{Operator: "test-admin", Reason: "preverified device"}); err != nil {
		t.Fatal(err)
	}
	addr, _ := startTestListener(t, s)
	client := dialWith(t, addr, keyPath)
	pc, channel := openTestControl(t, client, proto.RoleImporter, "mixed-role")
	defer channel.Close()
	ack := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "test", SrcHost: "127.0.0.1", SrcPort: 8080, Name: "mixed export"}})
	if len(ack.Targets) != 1 || !ack.Targets[0].Valid() || ack.Targets[0].Fingerprint != fp {
		t.Fatalf("invalid trusted targets: %+v", ack.Targets)
	}
	if got := len(s.sessions.RegistrySnapshot()); got != 1 {
		t.Fatalf("mixed-role registry entries=%d", got)
	}
	overview, err := (*adminBackend)(s).Overview(context.Background())
	if err != nil || overview.Importers != 1 || overview.ActiveExporters != 1 {
		t.Fatalf("overview=%+v err=%v", overview, err)
	}
	publishManagementTunnels(t, pc, nil)
	if got := len(s.sessions.RegistrySnapshot()); got != 0 {
		t.Fatalf("cancel left registry entries=%d", got)
	}
}

func publishManagementTunnels(t *testing.T, pc *proto.Conn, tunnels []proto.TunnelSpec) proto.PublishOK {
	t.Helper()
	if err := pc.Send(proto.Publish{V: proto.Version, Type: proto.TypePublish, Tunnels: tunnels}); err != nil {
		t.Fatal(err)
	}
	for {
		env, raw, err := pc.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if env.Type == proto.TypeRegistry {
			continue
		}
		if env.Type != proto.TypePublishOK {
			t.Fatalf("publish rejected: %s %s", env.Type, raw)
		}
		var ack proto.PublishOK
		if err = proto.Decode(raw, &ack); err != nil {
			t.Fatal(err)
		}
		return ack
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
