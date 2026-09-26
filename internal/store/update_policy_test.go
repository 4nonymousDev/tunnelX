package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"tunnelx/internal/updatepolicy"
)

func TestUpdatePolicyMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	account := createTestAccount(t, s, "preserved", 10)
	fp := securityFingerprint("preserved-device")
	if err = s.EnrollAccountDevice(ctx, account.Username, account.Generation, "original-id", fp, "original name", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE client_update_policy`, `PRAGMA user_version=5`} {
		if _, err = s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.GetClientUpdatePolicy(ctx)
	if err != nil || p.MinimumVersion != "" || p.Generation != 1 {
		t.Fatalf("migration enabled a policy: %+v %v", p, err)
	}
	backup, err := sql.Open("sqlite", s.BackupPath())
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err = backup.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 5 {
		t.Fatalf("backup version=%d %v", v, err)
	}
	backup.Close()
	if err = s.ValidateAccountDevice(ctx, fp, "original-id"); err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateIdentity(ctx, fp, "original-id"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(updatepolicy.DefaultGracePeriod).Truncate(time.Second)
	p, err = s.SetClientUpdatePolicy(ctx, updatepolicy.Policy{MinimumVersion: "0.2.1", EnforceAfter: &deadline, Message: "security update"}, 1, AdminAction{Reason: "test"})
	if err != nil || p.Generation != 2 {
		t.Fatalf("save=%+v %v", p, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetClientUpdatePolicy(ctx)
	if err != nil || got.Generation != 2 || got.EnforceAfter == nil || !got.EnforceAfter.Equal(deadline) {
		t.Fatalf("deadline changed on reopen: %+v %v", got, err)
	}
}

func TestUpdatePolicyConflictAndAuditRollback(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	deadline := time.Now().Add(time.Hour)
	p := updatepolicy.Policy{MinimumVersion: "0.2.1", EnforceAfter: &deadline}
	if _, err := s.SetClientUpdatePolicy(ctx, p, 1, AdminAction{Reason: "require update"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetClientUpdatePolicy(ctx, updatepolicy.Policy{}, 1, AdminAction{}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_policy_audit BEFORE INSERT ON admin_actions BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetClientUpdatePolicy(ctx, updatepolicy.Policy{}, 2, AdminAction{}); err == nil {
		t.Fatal("policy saved without audit")
	}
	got, err := s.GetClientUpdatePolicy(ctx)
	if err != nil || got.MinimumVersion != p.MinimumVersion || got.Generation != 2 {
		t.Fatalf("partial change: %+v %v", got, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_policy_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetClientUpdatePolicy(ctx, updatepolicy.Policy{}, 2, AdminAction{Reason: "cancel"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetClientUpdatePolicy(ctx)
	if err != nil || got.MinimumVersion != "" || got.Generation != 3 {
		t.Fatalf("disable=%+v %v", got, err)
	}
	var actions int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM admin_actions WHERE action='set_client_update_policy' AND target_type='update_policy'`).Scan(&actions); err != nil || actions != 2 {
		t.Fatalf("audit count=%d %v", actions, err)
	}
}
