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

func TestReplaceIdentityKey(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		t.Run(map[bool]string{false: "file_authorized", true: "migrated_device"}[migrated], func(t *testing.T) {
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
			registered, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "same-device", PublicKey: string(original), Reason: "initial registration"})
			if err != nil {
				t.Fatal(err)
			}
			if migrated {
				if _, err = s.store.DB().Exec(`INSERT INTO device_keys(fingerprint,client_id,enabled) VALUES(?,?,1)`, registered.Fingerprint, "same-device"); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.store.UpdateClientNote(ctx, registered.Fingerprint, "retain history"); err != nil {
				t.Fatal(err)
			}
			active, err := s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: registered.Fingerprint})
			if err != nil {
				t.Fatal(err)
			}
			pub := genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "replacement"))
			key, _, _, _, err := ssh.ParseAuthorizedKey(pub)
			if err != nil {
				t.Fatal(err)
			}
			fp := ssh.FingerprintSHA256(key)
			r := adminapi.BindIdentityRequest{ClientID: "same-device", PublicKey: string(pub), ExpectedGeneration: 1, Reason: "replace lost key"}
			binding, err := b.BindIdentity(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			if binding.ClientID != r.ClientID || binding.Fingerprint != fp || binding.Generation != 2 || binding.Revoked {
				t.Fatalf("binding=%+v", binding)
			}
			if !s.fingerprintAuthorized(fp) || s.validateIdentity(fp, r.ClientID) != nil {
				t.Fatal("replacement cannot connect")
			}
			if err = s.validateIdentity(registered.Fingerprint, r.ClientID); !errors.Is(err, store.ErrIdentityMismatch) {
				t.Fatalf("old key accepted: %v", err)
			}
			if _, ok := s.sessions.Get(active.ID); ok {
				t.Fatal("old session retained")
			}
			old, err := s.store.GetClient(ctx, registered.Fingerprint)
			if err != nil || old.Note != "retain history" {
				t.Fatalf("old history lost: %+v %v", old, err)
			}
			if _, err = s.store.GetClient(ctx, fp); err != nil {
				t.Fatal("replacement missing from clients", err)
			}
			if _, err = b.BindIdentity(ctx, r); !errors.Is(err, adminapi.ErrConflict) {
				t.Fatalf("stale generation accepted: %v", err)
			}
			if err = b.RevokeIdentity(ctx, r.ClientID, 2, "revoke"); err != nil {
				t.Fatal(err)
			}
			if err = s.store.ImportClient(ctx, fp, "keep user", "keep@example.com", "keep computer", time.Now()); err != nil {
				t.Fatal(err)
			}
			if err = b.BlockClient(ctx, fp, adminapi.BlockRequest{Reason: "retain block during recovery"}); err != nil {
				t.Fatal(err)
			}
			r.ExpectedGeneration = 3
			if _, err = b.BindIdentity(ctx, r); err != nil {
				t.Fatal("explicit recovery failed", err)
			}
			if err = s.validateIdentity(fp, r.ClientID); err != nil {
				t.Fatal(err)
			}
			recovered, err := s.store.GetClient(ctx, fp)
			if err != nil || recovered.Username != "keep user" || recovered.Email != "keep@example.com" || recovered.ComputerName != "keep computer" || !s.policy.Blocked(fp) {
				t.Fatalf("recovery lost metadata or block: %+v %v", recovered, err)
			}
			s.Close()
			restarted, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if !restarted.fingerprintAuthorized(fp) || restarted.validateIdentity(fp, r.ClientID) != nil {
				t.Fatal("replacement did not survive restart")
			}
		})
	}
}

func TestReplacementRejectsInvalidInputBeforeAuthorization(t *testing.T) {
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
	registered, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "same-device", PublicKey: string(original), Reason: "register"})
	if err != nil {
		t.Fatal(err)
	}
	pub := string(genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "replacement")))
	base := adminapi.BindIdentityRequest{ClientID: "same-device", PublicKey: pub, ExpectedGeneration: 1, Reason: "replace"}
	for _, tc := range []struct {
		name   string
		change func(*adminapi.BindIdentityRequest)
	}{
		{"stale", func(r *adminapi.BindIdentityRequest) { r.ExpectedGeneration = 2 }},
		{"unbound", func(r *adminapi.BindIdentityRequest) { r.ClientID = "unregistered" }},
		{"create", func(r *adminapi.BindIdentityRequest) { r.ExpectedGeneration = 0 }},
		{"fingerprint_mismatch", func(r *adminapi.BindIdentityRequest) { r.Fingerprint = registered.Fingerprint }},
		{"multiple", func(r *adminapi.BindIdentityRequest) { r.PublicKey += pub }},
		{"options", func(r *adminapi.BindIdentityRequest) { r.PublicKey = "no-pty " + pub }},
		{"metadata", func(r *adminapi.BindIdentityRequest) { r.PublicKey = strings.TrimSpace(pub) + " tunnelx:{broken}" }},
		{"oversized", func(r *adminapi.BindIdentityRequest) { r.PublicKey = strings.Repeat("x", 16385) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.change(&r)
			if _, err := b.BindIdentity(ctx, r); err == nil {
				t.Fatal("invalid replacement accepted")
			}
			after, err := os.ReadFile(cfg.AuthorizedKeys)
			if err != nil || string(after) != string(original) {
				t.Fatal("invalid replacement changed authorization file")
			}
			if err = s.validateIdentity(registered.Fingerprint, base.ClientID); err != nil {
				t.Fatal("old binding changed", err)
			}
		})
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.DB().Exec(`INSERT INTO device_keys(fingerprint,client_id,enabled) VALUES(?,?,0)`, ssh.FingerprintSHA256(key), "other-device"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.BindIdentity(ctx, base); !errors.Is(err, adminapi.ErrConflict) {
		t.Fatalf("migrated ownership changed: %v", err)
	}
	after, err := os.ReadFile(cfg.AuthorizedKeys)
	if err != nil || string(after) != string(original) {
		t.Fatal("ownership conflict authorized new key")
	}
}

func TestReplacementBindingFailurePreservesOldIdentityAndCanRetry(t *testing.T) {
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
	registered, err := b.ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "same-device", PublicKey: string(original), Reason: "register"})
	if err != nil {
		t.Fatal(err)
	}
	pub := string(genKey(t, filepath.Join(filepath.Dir(cfg.HostKeyPath), "replacement")))
	r := adminapi.BindIdentityRequest{ClientID: "same-device", PublicKey: pub, ExpectedGeneration: 1, Reason: "replace"}
	if _, err = s.store.DB().Exec(`CREATE TRIGGER fail_replacement BEFORE UPDATE ON identity_bindings BEGIN SELECT RAISE(ABORT,'test binding failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = b.BindIdentity(ctx, r); err == nil {
		t.Fatal("failed binding reported success")
	}
	if err = s.validateIdentity(registered.Fingerprint, r.ClientID); err != nil {
		t.Fatal("old identity lost", err)
	}
	if _, err = s.store.DB().Exec(`DROP TRIGGER fail_replacement`); err != nil {
		t.Fatal(err)
	}
	if _, err = b.BindIdentity(ctx, r); err != nil {
		t.Fatal("retry failed", err)
	}
}
