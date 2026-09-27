package store

import (
	"context"
	"database/sql"
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

func TestAccountCapacityAndAtomicAuditRollback(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	createTestAccount(t, s, "alice", 1)
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
	for _, q := range []string{`DROP TABLE client_update_policy`, `DROP TABLE device_keys`, `DROP TABLE accounts`, `PRAGMA user_version=3`} {
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
