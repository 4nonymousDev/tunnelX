package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

func TestRegistrationExistingKeyPreservesDataAndSession(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	b := (*adminBackend)(s)
	pub, err := os.ReadFile(cfg.AuthorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	fp := ssh.FingerprintSHA256(loadSigner(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")).PublicKey())
	firstSeen := time.Now().Add(-24 * time.Hour)
	if err = s.store.ImportClient(ctx, fp, "original user", "original@example.com", "original computer", firstSeen); err != nil {
		t.Fatal(err)
	}
	if err = s.store.UpdateClientNote(ctx, fp, "keep note"); err != nil {
		t.Fatal(err)
	}
	r := adminapi.ImportPublicKeyRequest{ClientID: "original-id", PublicKey: string(pub), Reason: "register existing device"}
	s.auth.markUnhealthy() // A repaired file must also refresh on duplicate import.
	if _, err = b.ImportPublicKey(ctx, r); err != nil {
		t.Fatal(err)
	}
	if !s.auth.AuthorizedFingerprint(fp) {
		t.Fatal("known key registration did not refresh authorization health")
	}
	before, err := s.store.GetIdentity(ctx, r.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.ImportPublicKey(ctx, r); err != nil {
		t.Fatal(err)
	}
	after, err := s.store.GetIdentity(ctx, r.ClientID)
	if err != nil || before != after {
		t.Fatalf("repeat changed binding: before=%+v after=%+v err=%v", before, after, err)
	}
	if _, ok := s.sessions.Get(active.ID); !ok {
		t.Fatal("repeat disconnected existing session")
	}
	data, err := os.ReadFile(cfg.AuthorizedKeys)
	if err != nil || string(data) != string(pub) {
		t.Fatal("repeat rewrote original authorization file")
	}
	client, err := s.store.GetClient(ctx, fp)
	if err != nil || client.Username != "original user" || client.Email != "original@example.com" || client.ComputerName != "original computer" || client.Note != "keep note" || !client.FirstSeenAt.Equal(firstSeen.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("lost original data: %+v err=%v", client, err)
	}
	if err = b.BlockClient(ctx, fp, adminapi.BlockRequest{Reason: "must stay blocked"}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.ImportPublicKey(ctx, r); err != nil {
		t.Fatal(err)
	}
	if !s.policy.Blocked(fp) {
		t.Fatal("registration silently unblocked key")
	}
}

func TestRegistrationConflictDoesNotAuthorizeReplacement(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "different_key", true: "revoked"}[revoked], func(t *testing.T) {
			cfg := managementConfig(t)
			s, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			b := (*adminBackend)(s)
			original, err := os.ReadFile(cfg.AuthorizedKeys)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "original-id", PublicKey: string(original), Reason: "original registration"}); err != nil {
				t.Fatal(err)
			}
			if revoked {
				if err = b.RevokeIdentity(ctx, "original-id", 1, "retired"); err != nil {
					t.Fatal(err)
				}
			}
			replacement := genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "replacement"))
			for _, pub := range [][]byte{replacement, original} {
				if !revoked && string(pub) == string(original) {
					continue
				}
				_, err = b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "original-id", PublicKey: string(pub), Reason: "must not overwrite"})
				if !errors.Is(err, adminapi.ErrConflict) {
					t.Fatalf("conflict not rejected: %v", err)
				}
			}
			_, err = b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{PublicKey: string(replacement), Reason: "missing ID"})
			if err == nil {
				t.Fatal("missing ID accepted")
			}
			after, err := os.ReadFile(cfg.AuthorizedKeys)
			if err != nil || string(after) != string(original) {
				t.Fatal("conflict modified authorization file")
			}
			binding, err := s.store.GetIdentity(ctx, "original-id")
			if err != nil || binding.Revoked != revoked {
				t.Fatalf("conflict altered revocation: %+v %v", binding, err)
			}
		})
	}
}

func TestRegistrationPartialFailureAndRecovery(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "reconcile_without_replay", true: "retry_same_form"}[retry], func(t *testing.T) {
			cfg := managementConfig(t)
			s, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			b := (*adminBackend)(s)
			pub := genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "new_key"))
			key, _, _, _, err := ssh.ParseAuthorizedKey(pub)
			if err != nil {
				t.Fatal(err)
			}
			fp := ssh.FingerprintSHA256(key)
			if _, err = s.store.DB().Exec(`CREATE TRIGGER fail_registration BEFORE INSERT ON identity_bindings BEGIN SELECT RAISE(ABORT,'test binding write failure'); END`); err != nil {
				t.Fatal(err)
			}
			r := adminapi.ImportPublicKeyRequest{ClientID: "new-id", PublicKey: string(pub), Reason: "approved registration"}
			_, err = b.ImportPublicKey(ctx, r)
			var pending *adminapi.PendingOperationError
			if !errors.As(err, &pending) {
				t.Fatalf("partial failure reported incorrectly: %v", err)
			}
			if !s.auth.AuthorizedFingerprint(fp) {
				t.Fatal("test did not reach file mutation")
			}
			if err = s.store.ValidateIdentity(ctx, fp, r.ClientID); !errors.Is(err, store.ErrIdentityUnbound) {
				t.Fatalf("failed registration became trusted: %v", err)
			}
			ops, err := s.store.ListAdminOperations(ctx, 10)
			if err != nil || len(ops) != 1 || ops[0].State != "needs_reconcile" {
				t.Fatalf("missing recovery record: %+v %v", ops, err)
			}
			if _, err = s.store.DB().Exec(`DROP TRIGGER fail_registration`); err != nil {
				t.Fatal(err)
			}
			if retry {
				if _, err = b.ImportPublicKey(ctx, r); err != nil {
					t.Fatalf("same form cannot finish registration: %v", err)
				}
			}
			if err = b.ReconcileAdminOperation(ctx, pending.ID, "checked current authorization and binding"); err != nil {
				t.Fatal(err)
			}
			ops, err = s.store.ListAdminOperations(ctx, 10)
			if err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if retry {
				want = "applied"
			}
			for _, op := range ops {
				if op.ID == pending.ID && op.State != want {
					t.Fatalf("reconcile trusted file alone: %+v", op)
				}
			}
			if !retry {
				if err = s.store.ValidateIdentity(ctx, fp, r.ClientID); !errors.Is(err, store.ErrIdentityUnbound) {
					t.Fatalf("reconciliation created identity: %v", err)
				}
			}
		})
	}
}

func TestRegistrationConcurrentSameIDAuthorizesOnlyWinner(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	keys := [][]byte{genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "first")), genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "second"))}
	type outcome struct {
		fp  string
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, pub := range keys {
		key, _, _, _, err := ssh.ParseAuthorizedKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			<-start
			_, err := (*adminBackend)(s).ImportPublicKey(context.Background(), adminapi.ImportPublicKeyRequest{ClientID: "shared-id", PublicKey: string(pub), Reason: "concurrent registration"})
			results <- outcome{ssh.FingerprintSHA256(key), err}
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err == nil {
			successes++
			if !s.auth.AuthorizedFingerprint(r.fp) {
				t.Fatal("winner not authorized")
			}
		} else {
			if !errors.Is(r.err, adminapi.ErrConflict) || s.auth.AuthorizedFingerprint(r.fp) {
				t.Fatalf("loser authorized or unexpected error: %v", r.err)
			}
		}
	}
	if successes != 1 {
		t.Fatalf("successful registrations=%d", successes)
	}
}

func TestRegistrationAllowsSSHWithoutSeparateApproval(t *testing.T) {
	s, addr, keyPath := startServerInstance(t, func(c *Config) {
		c.IdentityBindings = nil
		c.DataDir = t.TempDir()
	})
	key := loadSigner(t, keyPath)
	if _, err := (*adminBackend)(s).ImportPublicKey(context.Background(), adminapi.ImportPublicKeyRequest{ClientID: "original-id", PublicKey: string(ssh.MarshalAuthorizedKey(key.PublicKey())), Reason: "existing client"}); err != nil {
		t.Fatal(err)
	}
	client := dialWith(t, addr, keyPath)
	defer client.Close()
	pc, ch := openTestControl(t, client, proto.RoleExporter, "original-id")
	defer ch.Close()
	ack := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "original-tunnel", SrcHost: "127.0.0.1", SrcPort: 8080, Name: "service"}})
	if len(ack.Targets) != 1 || ack.Targets[0].Fingerprint != ssh.FingerprintSHA256(key.PublicKey()) || ack.Targets[0].ClientID != "original-id" {
		t.Fatalf("trusted publication failed: %+v", ack)
	}
}

func TestRegistrationUncertainCommitClosesAffectedSessions(t *testing.T) {
	cfg := managementConfig(t)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := loadSigner(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key"))
	fp := ssh.FingerprintSHA256(key.PublicKey())
	active, err := s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`CREATE TABLE registration_commit_fault(fingerprint TEXT REFERENCES clients(fingerprint) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER registration_commit_failure BEFORE INSERT ON identity_bindings BEGIN INSERT INTO registration_commit_fault VALUES('missing-client'); END`,
	} {
		if _, err = s.store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	_, err = (*adminBackend)(s).ImportPublicKey(context.Background(), adminapi.ImportPublicKeyRequest{ClientID: "original-id", PublicKey: string(ssh.MarshalAuthorizedKey(key.PublicKey())), Reason: "register original client"})
	if !errors.Is(err, store.ErrCommitOutcomeUnknown) {
		t.Fatalf("commit uncertainty hidden: %v", err)
	}
	if _, ok := s.sessions.Get(active.ID); ok {
		t.Fatal("affected session survived uncertain authorization commit")
	}
	if ok, _ := s.policy.Admit("unrelated", nil); ok {
		t.Fatal("new admissions continued after uncertain commit")
	}
}
