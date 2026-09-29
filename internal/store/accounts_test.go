package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
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
