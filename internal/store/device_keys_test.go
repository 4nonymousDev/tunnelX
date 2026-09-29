package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestReopenPreservesDeviceAccessAndAdministrator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = s.ProvisionAdminAccount(ctx, "admin", accountTestPassword, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"active", "disabled", "stale", "revoked", "mismatch", "missing", "blocked"} {
		fp, enabled, revoked := securityFingerprint(name), 1, 0
		if name == "disabled" || name == "stale" {
			enabled = 0
		}
		if name == "revoked" {
			revoked = 1
		}
		if _, err = s.db.Exec(`INSERT INTO device_keys VALUES(?,?,?)`, fp, name, enabled); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(`INSERT INTO clients(fingerprint,first_seen_at,last_seen_at,note) VALUES(?,1,1,'keep note')`, fp); err != nil {
			t.Fatal(err)
		}
		bound := fp
		if name == "mismatch" {
			bound = securityFingerprint("another-key")
		}
		if name != "missing" {
			if _, err = s.db.Exec(`INSERT INTO identity_bindings VALUES(?,?,3,?,1,1)`, name, bound, revoked); err != nil {
				t.Fatal(err)
			}
		}
		if name == "blocked" {
			if _, err = s.db.Exec(`INSERT INTO blacklist VALUES(?,'keep block','test',1,NULL)`, fp); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.db.Exec(`PRAGMA user_version=7`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		var marker int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&marker); err != nil || marker != 7 {
			t.Fatalf("existing marker changed: %d %v", marker, err)
		}
		for _, name := range []string{"active", "disabled", "stale", "revoked", "mismatch", "missing", "blocked"} {
			fp := securityFingerprint(name)
			known, allowed, err := s.DeviceKeyStatus(ctx, fp)
			if err != nil || !known || allowed != (name == "active" || name == "blocked") {
				t.Fatalf("%s: known=%v allowed=%v err=%v", name, known, allowed, err)
			}
			if c, err := s.GetClient(ctx, fp); err != nil || c.Note != "keep note" {
				t.Fatal("client metadata lost")
			}
		}
		if a, err := s.AuthenticateAccount(ctx, "admin", accountTestPassword); err != nil || !a.IsAdmin || a.Generation != 1 {
			t.Fatal("admin login changed", err)
		}
		if blocked, err := s.IsBlocked(ctx, securityFingerprint("blocked"), time.Now()); err != nil || !blocked {
			t.Fatal("blacklist lost")
		}
	}
	check()
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check()
	if _, err = s.ProvisionAdminAccount(ctx, "admin", "new administrator password", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if known, allowed, err := s.DeviceKeyStatus(ctx, securityFingerprint("active")); err != nil || !known || !allowed {
		t.Fatal("admin password reset changed device authorization")
	}
	if _, err = s.AuthenticateAccount(ctx, "admin", accountTestPassword); !errors.Is(err, ErrAccountAuth) {
		t.Fatal("old password accepted")
	}
	if _, err = s.AuthenticateAccount(ctx, "admin", "new administrator password"); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceKeyRecoveryRequiresOriginalIdentityAndAtomicAudit(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	fp := securityFingerprint("disabled-device")
	if _, err := s.db.Exec(`INSERT INTO device_keys VALUES(?,'original-id',0)`, fp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindIdentity(ctx, "wrong-id", fp, 0, AdminAction{}); !errors.Is(err, ErrIdentityConflict) {
		t.Fatal("ownership bypass", err)
	}
	if _, err := s.GetIdentity(ctx, "wrong-id"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatal("partial identity change")
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_device_audit BEFORE INSERT ON admin_actions BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindIdentity(ctx, "original-id", fp, 0, AdminAction{}); err == nil {
		t.Fatal("unaudited restore")
	}
	if _, allowed, _ := s.DeviceKeyStatus(ctx, fp); allowed {
		t.Fatal("partial restore")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_device_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindIdentity(ctx, "original-id", fp, 0, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if _, allowed, err := s.DeviceKeyStatus(ctx, fp); err != nil || !allowed {
		t.Fatal("explicit recovery failed")
	}
	if _, err := s.db.Exec(`INSERT INTO identity_bindings VALUES('historical-alias',?,1,0,1,1)`, fp); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateIdentity(ctx, fp, "historical-alias"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatal("device key used an unrelated historical identity", err)
	}
	if _, err := s.BeginClientRegistration(ctx, "other-id", fp, AdminAction{}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatal("conflicting registration started", err)
	}
	if err := s.RevokeIdentity(ctx, "original-id", 1, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if _, allowed, _ := s.DeviceKeyStatus(ctx, fp); allowed {
		t.Fatal("revoked key allowed")
	}
	s.commitUncertain.Store(true)
	if _, _, err := s.DeviceKeyStatus(ctx, fp); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatal("uncertain commit allowed")
	}
	s.commitUncertain.Store(false)
}
