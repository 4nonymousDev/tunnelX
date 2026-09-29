package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

func TestDeleteRotatedClientPreservesSameNameReplacement(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	b := (*adminBackend)(s)
	oldKey, err := os.ReadFile(cfg.AuthorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	oldFP := ssh.FingerprintSHA256(loadSigner(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")).PublicKey())
	if _, err = b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "original-id", PublicKey: string(oldKey), ComputerName: "SAME-PC", Reason: "register"}); err != nil {
		t.Fatal(err)
	}
	replacement := genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "replacement"))
	bound, err := b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: "original-id", PublicKey: string(replacement), ExpectedGeneration: 1, Reason: "rotate"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.ImportClient(ctx, bound.Fingerprint, "", "", "SAME-PC", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.store.RecordIdentityClaim(ctx, oldFP, "original-id", "SAME-PC", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.StartConnection(ctx, store.ConnectionAudit{Fingerprint: oldFP, Result: "disconnected", AuthenticatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Remove duplicate key lines as well, preserving unrelated comments and keys.
	data, err := os.ReadFile(cfg.AuthorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.AuthorizedKeys, append(append(data, oldKey...), []byte("# retain this comment\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = b.DeleteClient(ctx, oldFP, "remove superseded key"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.GetClient(ctx, oldFP); !errors.Is(err, adminapi.ErrNotFound) {
		t.Fatalf("old asset still present: %v", err)
	}
	if _, err = b.GetClient(ctx, bound.Fingerprint); err != nil {
		t.Fatal(err)
	}
	after, err := s.store.GetIdentity(ctx, "original-id")
	if err != nil || after.Fingerprint != bound.Fingerprint || after.Generation != bound.Generation || after.Revoked {
		t.Fatalf("replacement identity changed: %+v %v", after, err)
	}
	if s.fingerprintAuthorized(oldFP) || !s.fingerprintAuthorized(bound.Fingerprint) {
		t.Fatal("wrong key authorization removed")
	}
	data, err = os.ReadFile(cfg.AuthorizedKeys)
	if err != nil || strings.Contains(string(data), strings.TrimSpace(string(oldKey))) || !strings.Contains(string(data), "# retain this comment") {
		t.Fatalf("bad authorization file: %s %v", data, err)
	}
	claims, err := s.store.ListIdentityClaims(ctx, oldFP, 100)
	if err != nil || len(claims) != 0 {
		t.Fatalf("claims remain: %+v %v", claims, err)
	}
	history, err := s.store.ListConnections(ctx, store.AuditFilter{Fingerprint: oldFP, Limit: 100})
	if err != nil || len(history) != 1 {
		t.Fatalf("audit lost: %+v %v", history, err)
	}
	actions, err := s.store.ListAdminActions(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, action := range actions {
		if action.Action == "delete_client" && action.TargetID == oldFP && action.Result == "success" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing deletion audit")
	}
	if err = b.DeleteClient(ctx, oldFP, "repeat"); !errors.Is(err, adminapi.ErrNotFound) {
		t.Fatalf("repeat deletion: %v", err)
	}
}

func TestDeleteClientRevokesStoredAuthorizationAndPersists(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	b := (*adminBackend)(s)
	pub, _ := os.ReadFile(cfg.AuthorizedKeys)
	registered, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "device", PublicKey: string(pub), Reason: "register"})
	if err != nil {
		t.Fatal(err)
	}
	fp := registered.Fingerprint
	if _, err = s.store.DB().Exec(`INSERT INTO device_keys(fingerprint,client_id,enabled) VALUES(?,?,1)`, fp, "device"); err != nil {
		t.Fatal(err)
	}
	if err = b.BlockClient(ctx, fp, adminapi.BlockRequest{Reason: "preserve restriction"}); err != nil {
		t.Fatal(err)
	}
	if err = b.DeleteClient(ctx, fp, "retire"); err != nil {
		t.Fatal(err)
	}
	binding, err := s.store.GetIdentity(ctx, "device")
	if err != nil || !binding.Revoked || binding.Generation != 2 {
		t.Fatalf("binding not revoked: %+v %v", binding, err)
	}
	known, allowed, err := s.store.DeviceKeyStatus(ctx, fp)
	if err != nil || !known || allowed {
		t.Fatalf("stored authorization lost restrictions: %v %v %v", known, allowed, err)
	}
	if !s.policy.Blocked(fp) {
		t.Fatal("deletion removed blacklist")
	}
	s.Close()
	restarted, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.fingerprintAuthorized(fp) {
		t.Fatal("deleted key authorized after restart")
	}
	if _, err = (*adminBackend)(restarted).GetClient(ctx, fp); !errors.Is(err, adminapi.ErrNotFound) {
		t.Fatalf("asset returned after restart: %v", err)
	}
}

func TestDeleteClientRejectsOnlineAndRollsBackOnAuditFailure(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	b := (*adminBackend)(s)
	pub, _ := os.ReadFile(cfg.AuthorizedKeys)
	registered, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "device", PublicKey: string(pub), Reason: "register"})
	if err != nil {
		t.Fatal(err)
	}
	fp := registered.Fingerprint
	active, err := s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	var backendErr *adminapi.BackendError
	if err = b.DeleteClient(ctx, fp, "online"); !errors.As(err, &backendErr) || backendErr.Code != "client_online" {
		t.Fatalf("online deletion: %v", err)
	}
	if !s.auth.AuthorizedFingerprint(fp) {
		t.Fatal("online deletion changed file")
	}
	s.sessions.Disconnect(active.ID, "test")
	wrote := false
	if err := s.persistClientSession(active.ID, func() error { wrote = true; return nil }); err == nil || wrote {
		t.Fatal("disconnected session can recreate asset")
	}
	if _, err = s.store.DB().Exec(`CREATE TRIGGER fail_delete_audit BEFORE UPDATE ON admin_actions WHEN NEW.action='delete_client' AND NEW.result='success' BEGIN SELECT RAISE(ABORT,'test audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	var pending *adminapi.PendingOperationError
	if err = b.DeleteClient(ctx, fp, "audit fails"); !errors.As(err, &pending) {
		t.Fatalf("partial deletion reported incorrectly: %v", err)
	}
	if _, err = b.GetClient(ctx, fp); err != nil {
		t.Fatal("failed transaction lost asset", err)
	}
	binding, err := s.store.GetIdentity(ctx, "device")
	if err != nil || binding.Revoked || binding.Generation != 1 {
		t.Fatalf("partial database changes: %+v %v", binding, err)
	}
	if err = b.ReconcileAdminOperation(ctx, pending.ID, "inspect current state"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.GetClient(ctx, fp); err != nil {
		t.Fatal("reconciliation replayed deletion")
	}
	if _, err = s.store.DB().Exec(`DROP TRIGGER fail_delete_audit`); err != nil {
		t.Fatal(err)
	}
	if err = b.DeleteClient(ctx, fp, "retry after reconciliation"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteClientInvalidAuthorizationFilePreservesRecord(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	b := (*adminBackend)(s)
	pub, _ := os.ReadFile(cfg.AuthorizedKeys)
	registered, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "device", PublicKey: string(pub), Reason: "register"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.AuthorizedKeys, []byte("invalid public key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var pending *adminapi.PendingOperationError
	if err = b.DeleteClient(ctx, registered.Fingerprint, "cleanup"); !errors.As(err, &pending) {
		t.Fatalf("file failure reported incorrectly: %v", err)
	}
	if _, err = b.GetClient(ctx, registered.Fingerprint); err != nil {
		t.Fatal("file failure removed asset", err)
	}
}
