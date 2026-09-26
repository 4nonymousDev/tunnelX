package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const accountTestPassword = "correct horse battery staple"

func createTestAccount(t *testing.T, s *Store, username string, limit int) Account {
	t.Helper()
	a, err := s.CreateAccount(context.Background(), username, accountTestPassword, limit, AdminAction{Operator: "test-admin", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func assertAccountKey(t *testing.T, s *Store, fp string, owned, allowed bool) {
	t.Helper()
	gotOwned, gotAllowed, err := s.AccountKeyStatus(context.Background(), fp)
	if err != nil || gotOwned != owned || gotAllowed != allowed {
		t.Fatalf("key status owned=%v allowed=%v err=%v; want %v %v", gotOwned, gotAllowed, err, owned, allowed)
	}
}

func TestAccountAuthenticationAndPasswordGeneration(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	a := createTestAccount(t, s, "alice", 10)
	if _, err := s.AuthenticateAccount(ctx, "alice", accountTestPassword); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ user, password string }{{"alice", "wrong password input"}, {"missing", accountTestPassword}, {" Alice ", accountTestPassword}, {"alice", "short"}} {
		if _, err := s.AuthenticateAccount(ctx, test.user, test.password); !errors.Is(err, ErrAccountAuth) {
			t.Fatalf("auth error=%v", err)
		}
	}
	fp := securityFingerprint("account-generation")
	if err := s.EnrollAccountDevice(ctx, "alice", a.Generation, "device-a", fp, "workstation", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	assertAccountKey(t, s, fp, true, true)
	if err := s.ValidateAccountDevice(ctx, fp, "wrong-id"); !errors.Is(err, ErrAccountAuth) {
		t.Fatalf("wrong device ID accepted: %v", err)
	}
	assertAccountKey(t, s, securityFingerprint("legacy-key"), false, false)
	if err := s.ValidateAccountDevice(ctx, securityFingerprint("legacy-key"), "legacy-device"); err != nil {
		t.Fatalf("legacy key changed: %v", err)
	}
	updated, err := s.UpdateAccount(ctx, "alice", "new password after reset", nil, a.Generation, AdminAction{})
	if err != nil || updated.Generation != 2 {
		t.Fatalf("reset=%+v %v", updated, err)
	}
	assertAccountKey(t, s, fp, true, false)
	if err := s.ValidateAccountDevice(ctx, fp, "device-a"); !errors.Is(err, ErrAccountAuth) {
		t.Fatalf("old epoch accepted: %v", err)
	}
	if err := s.EnrollAccountDevice(ctx, "alice", 1, "device-a", fp, "", AdminAction{}); !errors.Is(err, ErrAccountAuth) {
		t.Fatalf("stale password auth admitted: %v", err)
	}
	if _, err := s.AuthenticateAccount(ctx, "alice", accountTestPassword); !errors.Is(err, ErrAccountAuth) {
		t.Fatal("old password retained")
	}
	if _, err := s.AuthenticateAccount(ctx, "alice", "new password after reset"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollAccountDevice(ctx, "alice", updated.Generation, "device-a", fp, "", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	assertAccountKey(t, s, fp, true, true)
	disabled := false
	updated, err = s.UpdateAccount(ctx, "alice", "", &disabled, updated.Generation, AdminAction{})
	if err != nil || updated.Generation != 3 {
		t.Fatalf("disable=%+v %v", updated, err)
	}
	assertAccountKey(t, s, fp, true, false)
	if _, err := s.AuthenticateAccount(ctx, "alice", "new password after reset"); !errors.Is(err, ErrAccountAuth) {
		t.Fatalf("disabled auth=%v", err)
	}
	enabled := true
	updated, err = s.UpdateAccount(ctx, "alice", "", &enabled, updated.Generation, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	assertAccountKey(t, s, fp, true, false)
	if err := s.EnrollAccountDevice(ctx, "alice", updated.Generation, "device-a", fp, "", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	assertAccountKey(t, s, fp, true, true)
	if _, err := s.UpdateAccount(ctx, "alice", "", &disabled, 1, AdminAction{}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("stale admin update=%v", err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%v %v", accounts, err)
	}
	publicJSON, err := json.Marshal(accounts)
	if err != nil || strings.Contains(string(publicJSON), "password") || strings.Contains(string(publicJSON), "argon2") {
		t.Fatal("account API exposes credentials")
	}
	devices, err := s.ListAccountDevices(ctx, "alice")
	if err != nil || len(devices) != 1 || !devices[0].Active || devices[0].Name != "workstation" || devices[0].Generation != updated.Generation {
		t.Fatalf("devices=%+v %v", devices, err)
	}
}

func TestAccountEnrollmentRetainsIdentityAndMetadataAndRejectsConflicts(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	createTestAccount(t, s, "alice", 10)
	createTestAccount(t, s, "other", 10)
	fp := securityFingerprint("existing-account-key")
	if err := s.ImportClient(ctx, fp, "keep name", "keep@example.invalid", "keep computer", time.Unix(50, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateClientNote(ctx, fp, "keep note"); err != nil {
		t.Fatal(err)
	}
	binding, err := s.BindIdentity(ctx, "device-a", fp, 0, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	binding, err = s.GetIdentity(ctx, "device-a")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.EnrollAccountDevice(ctx, "alice", 1, "device-a", fp, "new friendly name", AdminAction{}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.GetIdentity(ctx, "device-a")
	if err != nil || after != binding {
		t.Fatalf("identity mutated: %+v -> %+v (%v)", binding, after, err)
	}
	client, err := s.GetClient(ctx, fp)
	if err != nil || client.Note != "keep note" || client.Username != "keep name" || client.Email != "keep@example.invalid" || client.ComputerName != "keep computer" || !client.FirstSeenAt.Equal(time.Unix(50, 0)) {
		t.Fatalf("metadata mutated: %+v %v", client, err)
	}
	for _, x := range []struct{ user, id, fp string }{{"other", "device-a", fp}, {"other", "device-b", fp}, {"alice", "device-b", fp}, {"alice", "device-a", securityFingerprint("different-key")}} {
		if err := s.EnrollAccountDevice(ctx, x.user, 1, x.id, x.fp, "", AdminAction{}); !errors.Is(err, ErrAccountConflict) {
			t.Fatalf("ownership conflict accepted: %v", err)
		}
	}
	if err := s.RevokeIdentity(ctx, "device-a", binding.Generation, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	assertAccountKey(t, s, fp, true, false)
	if err := s.EnrollAccountDevice(ctx, "alice", 1, "device-a", fp, "", AdminAction{}); !errors.Is(err, ErrIdentityRevoked) {
		t.Fatalf("revoked identity restored: %v", err)
	}
	if err := s.EnrollAccountDevice(ctx, "other", 1, "device-a", fp, "", AdminAction{}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("revoked ownership reassigned: %v", err)
	}
	blockedFP := securityFingerprint("blocked-new-key")
	if err := s.Block(ctx, BlockRequest{Entry: BlacklistEntry{Fingerprint: blockedFP, Reason: "retain block"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollAccountDevice(ctx, "alice", 1, "blocked-device", blockedFP, "", AdminAction{}); !errors.Is(err, ErrAccountAuth) {
		t.Fatalf("blacklist bypassed: %v", err)
	}
	if blocked, err := s.IsBlocked(ctx, blockedFP, time.Now()); err != nil || !blocked {
		t.Fatal("blacklist changed")
	}
	assertAccountKey(t, s, blockedFP, false, false)
	legacyFP := securityFingerprint("legacy-mismatch")
	if _, err := s.BindIdentity(ctx, "legacy-id", legacyFP, 0, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollAccountDevice(ctx, "alice", 1, "legacy-id", securityFingerprint("claim-key"), "", AdminAction{}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("legacy ID stolen: %v", err)
	}
}

func TestAccountCapacityAndAtomicAuditRollback(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	createTestAccount(t, s, "alice", 1)
	if err := s.EnrollAccountDevice(ctx, "alice", 1, "first", securityFingerprint("first"), "", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollAccountDevice(ctx, "alice", 1, "second", securityFingerprint("second"), "", AdminAction{}); !errors.Is(err, ErrAccountLimit) {
		t.Fatalf("device limit: %v", err)
	}
	if _, err := s.GetIdentity(ctx, "second"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatal("over-limit device got binding")
	}
	createTestAccount(t, s, "rollback", 10)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_account_audit BEFORE INSERT ON admin_actions BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, "failed-create", accountTestPassword, 10, AdminAction{}); err == nil {
		t.Fatal("create succeeded without audit")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM accounts WHERE username='failed-create'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("create partial commit")
	}
	disabled := false
	if _, err := s.UpdateAccount(ctx, "alice", "", &disabled, 1, AdminAction{}); err == nil {
		t.Fatal("update succeeded without audit")
	}
	assertAccountKey(t, s, securityFingerprint("first"), true, true)
	fp := securityFingerprint("rollback-device")
	if err := s.EnrollAccountDevice(ctx, "rollback", 1, "rollback-device", fp, "", AdminAction{}); err == nil {
		t.Fatal("enrollment succeeded without audit")
	}
	assertAccountKey(t, s, fp, false, false)
	if _, err := s.GetIdentity(ctx, "rollback-device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatal("partial identity committed")
	}
	if _, err := s.GetClient(ctx, fp); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("partial client metadata committed")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_account_audit`); err != nil {
		t.Fatal(err)
	}
	// Fill fixture data with an existing hash: capacity should not require 1000
	// expensive password derivations to test the storage invariant.
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		for i := 2; i < MaxAccounts; i++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(username,password_hash,enabled,generation,max_devices,created_at,updated_at) SELECT ?,password_hash,1,1,10,created_at,updated_at FROM accounts WHERE username='alice'`, fmt.Sprintf("fixture-%04d", i)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, "over-capacity", accountTestPassword, 10, AdminAction{}); !errors.Is(err, ErrAccountLimit) {
		t.Fatalf("account capacity=%v", err)
	}
}

func TestAccountMigrationPreservesV3AndCreatesConsistentBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fp := securityFingerprint("v3-identity")
	if _, err = s.BindIdentity(ctx, "original-id", fp, 0, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportClient(ctx, fp, "original user", "", "", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err = s.Block(ctx, BlockRequest{Entry: BlacklistEntry{Fingerprint: fp, Reason: "keep"}}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE client_update_policy`, `DROP TABLE account_devices`, `DROP TABLE accounts`, `PRAGMA user_version=3`} {
		if _, err = s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.BackupPath() == "" {
		t.Fatal("no v3 recovery backup")
	}
	backup, err := sql.Open("sqlite", s.BackupPath())
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	if err = backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("backup=%d %v", version, err)
	}
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated=%d %v", version, err)
	}
	if err = s.ValidateIdentity(ctx, fp, "original-id"); err != nil {
		t.Fatal(err)
	}
	if blocked, err := s.IsBlocked(ctx, fp, time.Now()); err != nil || !blocked {
		t.Fatal("block lost")
	}
	if c, err := s.GetClient(ctx, fp); err != nil || c.Username != "original user" {
		t.Fatal("metadata lost")
	}
	assertAccountKey(t, s, fp, false, false)
	createTestAccount(t, s, "after-upgrade", 10)
}

func TestAccountUsernameValidation(t *testing.T) {
	for _, v := range []string{"alice", "a_1", "0admin", strings.Repeat("a", 64)} {
		if !ValidAccountUsername(v) {
			t.Fatalf("valid username rejected: %q", v)
		}
	}
	for _, v := range []string{"", "ab", "Alice", "_alice", "a/b", "用户名", " alice", strings.Repeat("a", 65)} {
		if ValidAccountUsername(v) {
			t.Fatalf("invalid username accepted: %q", v)
		}
	}
}

func TestAccountDeviceOwnershipLookupIncludesInactiveWithoutAuthorizing(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	a := createTestAccount(t, s, "alice", 10)
	fp := securityFingerprint("lookup-device")
	if _, err := s.AccountDeviceForKey(ctx, fp); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown key ownership: %v", err)
	}
	if err := s.EnrollAccountDevice(ctx, a.Username, a.Generation, "kept-device", fp, "kept name", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	initial, err := s.AccountDeviceForKey(ctx, fp)
	if err != nil || initial.Username != a.Username || initial.ClientID != "kept-device" || initial.Fingerprint != fp || initial.Generation != a.Generation || initial.Name != "kept name" || !initial.Active {
		t.Fatalf("initial ownership: %+v %v", initial, err)
	}
	if err := s.RevokeIdentity(ctx, initial.ClientID, 1, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	checkInactive := func() {
		t.Helper()
		owned, err := s.AccountDeviceForKey(ctx, fp)
		want := initial
		want.Active = false
		if err != nil || owned != want {
			t.Fatalf("inactive ownership lost: %+v %v", owned, err)
		}
		assertAccountKey(t, s, fp, true, false)
		if err := s.ValidateAccountDevice(ctx, fp, initial.ClientID); !errors.Is(err, ErrAccountAuth) {
			t.Fatalf("ownership lookup authorized inactive device: %v", err)
		}
	}
	checkInactive()
	disabled := false
	a, err = s.UpdateAccount(ctx, a.Username, "", &disabled, a.Generation, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	checkInactive()
	// Explicit administrator restoration does not bypass disabled/stale epoch.
	if _, err = s.BindIdentity(ctx, initial.ClientID, fp, 2, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	checkInactive()
	enabled := true
	if _, err = s.UpdateAccount(ctx, a.Username, "", &enabled, a.Generation, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	checkInactive()
	s.commitUncertain.Store(true)
	if _, err = s.AccountDeviceForKey(ctx, fp); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("uncertain ownership read allowed: %v", err)
	}
	// Do not interfere with test cleanup; no actual uncertain COMMIT was made.
	s.commitUncertain.Store(false)
}
