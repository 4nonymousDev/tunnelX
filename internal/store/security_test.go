package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func securityFingerprint(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func TestMigrationBacksUpAndPreservesV2WithoutTrustingHistoricalClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range schemaV1 {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"ALTER TABLE clients ADD COLUMN username TEXT NOT NULL DEFAULT ''", "ALTER TABLE clients ADD COLUMN email TEXT NOT NULL DEFAULT ''", "ALTER TABLE clients ADD COLUMN computer_name TEXT NOT NULL DEFAULT ''", "PRAGMA user_version=2"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	fp := securityFingerprint("existing-key")
	if _, err = db.Exec(`INSERT INTO clients(fingerprint,note,first_seen_at,last_seen_at,last_client_id,username) VALUES(?,'keep note',1,2,'historical-claim','legacy label')`, fp); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO blacklist(fingerprint,reason,operator,created_at) VALUES(?,'keep block','existing-admin',1)`, fp); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO connection_audit(fingerprint,authenticated_at,result) VALUES(?,1,'rejected')`, fp); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	client, err := s.GetClient(ctx, fp)
	if err != nil || client.Note != "keep note" || client.Username != "legacy label" || client.LastClientID != "historical-claim" {
		t.Fatalf("client lost: %+v %v", client, err)
	}
	if blocked, err := s.IsBlocked(ctx, fp, time.Now()); err != nil || !blocked {
		t.Fatalf("block lost: %v %v", blocked, err)
	}
	if rows, err := s.ListConnections(ctx, AuditFilter{Limit: 10}); err != nil || len(rows) != 1 {
		t.Fatalf("audit lost: %+v %v", rows, err)
	}
	if err = s.ValidateIdentity(ctx, fp, "historical-claim"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("historical claim gained trust: %v", err)
	}
	backup := s.BackupPath()
	if backup == "" {
		t.Fatal("migration did not create backup")
	}
	if _, err = os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	backupDB, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err = backupDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("backup version %d err %v", version, err)
	}
	var note string
	if err = backupDB.QueryRow(`SELECT note FROM clients WHERE fingerprint=?`, fp).Scan(&note); err != nil || note != "keep note" {
		t.Fatal("backup missing original data")
	}
	backupDB.Close()
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.BackupPath() != "" {
		t.Fatal("unchanged schema created another migration backup")
	}
}

func TestExplicitIdentityApprovalCASRevocationAndAuditRollback(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	fp := securityFingerprint("owner")
	other := securityFingerprint("other")
	if err = s.RecordIdentityClaim(ctx, fp, "device-a", "untrusted name", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateIdentity(ctx, fp, "device-a"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatal("claim granted access")
	}
	a := AdminAction{Reason: "verified out of band", Operator: "test-admin"}
	b, err := s.BindIdentity(ctx, "device-a", fp, 0, a)
	if err != nil || b.Generation != 1 {
		t.Fatalf("bind: %+v %v", b, err)
	}
	if err = s.ValidateIdentity(ctx, fp, "device-a"); err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateIdentity(ctx, other, "device-a"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatal("wrong key accepted")
	}
	if _, err = s.BindIdentity(ctx, "device-a", other, 0, a); !errors.Is(err, ErrIdentityConflict) {
		t.Fatal("stale change accepted")
	}
	if err = s.RevokeIdentity(ctx, "device-a", 1, a); err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateIdentity(ctx, fp, "device-a"); !errors.Is(err, ErrIdentityRevoked) {
		t.Fatal("revocation ignored")
	}
	if _, err = s.BindIdentity(ctx, "device-a", other, 1, a); !errors.Is(err, ErrIdentityConflict) {
		t.Fatal("stale reactivation accepted")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_identity_audit BEFORE INSERT ON admin_actions BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindIdentity(ctx, "new-device", other, 0, a); err == nil {
		t.Fatal("binding committed without audit")
	}
	if _, err = s.GetIdentity(ctx, "new-device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatal("failed audit left binding")
	}
}

func TestClaimsAndAnonymousRejectionsHaveBoundedCardinality(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	fp := securityFingerprint("claimant")
	for i := 0; i < 1040; i++ {
		if err = s.RecordIdentityClaim(ctx, fp, fmt.Sprintf("candidate-%04d", i), "name", "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM identity_claims`).Scan(&count); err != nil || count != 1024 {
		t.Fatalf("claims=%d err=%v", count, err)
	}
	for i := 0; i < 20000; i++ {
		s.RecordRejection(fmt.Sprintf("untrusted-%d", i))
	}
	s.RecordRejection("rejected")
	s.RecordRejection("blocked")
	if err = s.flushRejections(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM rejection_counts`).Scan(&count); err != nil || count > 3 {
		t.Fatalf("anonymous cardinality=%d %v", count, err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM connection_audit`).Scan(&count); err != nil || count != 0 {
		t.Fatal("anonymous attempts created per-event history")
	}
	n, _, err := s.TodayRejected(ctx, time.Now())
	if err != nil || n != 20002 {
		t.Fatalf("aggregate count=%d err=%v", n, err)
	}
}

func TestPendingOperationSurvivesRestartWithoutReplayingSideEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.BeginAdminOperation(context.Background(), AdminAction{Action: "import_public_key", TargetID: securityFingerprint("pending"), Reason: "test intent"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ops, err := s.ListAdminOperations(context.Background(), 10)
	if err != nil || len(ops) != 1 || ops[0].ID != id || ops[0].State != "needs_reconcile" {
		t.Fatalf("ops=%+v err=%v", ops, err)
	}
	if err = s.CompleteAdminOperation(context.Background(), id, "applied", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAdminOperation(context.Background(), id, "applied", ""); err != nil {
		t.Fatal("idempotent completion failed")
	}
	if err = s.CompleteAdminOperation(context.Background(), id, "failed", ""); err == nil {
		t.Fatal("terminal result overwritten")
	}
}

func TestRetentionPreservesOpenAuditAndUnresolvedIntents(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	old := time.Now().Add(-365 * 24 * time.Hour)
	if _, err = s.StartConnection(ctx, ConnectionAudit{AuthenticatedAt: old, Result: "authenticated"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartConnection(ctx, ConnectionAudit{AuthenticatedAt: old, Result: "rejected"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginAdminOperation(ctx, AdminAction{Action: "disconnect", TargetID: "old-session", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err = s.Maintain(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListConnections(ctx, AuditFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Result != "authenticated" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	ops, err := s.ListAdminOperations(ctx, 10)
	if err != nil || len(ops) != 1 {
		t.Fatal("pending intent pruned")
	}
}

func TestSQLiteFullRollsBackIdentityAndPreservesExistingData(t *testing.T) {
	opts := DefaultOptions()
	opts.MaxDatabaseBytes = 1 << 20
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "bounded.db"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	fp := securityFingerprint("existing")
	if _, err = s.BindIdentity(ctx, "existing-device", fp, 0, AdminAction{Reason: "verified"}); err != nil {
		t.Fatal(err)
	}
	// An oversized transaction reliably exercises SQLite's real SQLITE_FULL
	// rollback without filling the host disk or relying on filesystem permissions.
	if _, err = s.BindIdentity(ctx, "new-device", fp, 0, AdminAction{Reason: strings.Repeat("x", 2<<20)}); err == nil {
		t.Fatal("SQLite hard budget did not reject oversized audit")
	}
	if _, err = s.GetIdentity(ctx, "new-device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("partial identity survived full database: %v", err)
	}
	if err = s.ValidateIdentity(ctx, fp, "existing-device"); err != nil {
		t.Fatalf("existing binding changed: %v", err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM admin_actions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial audit survived: %d %v", count, err)
	}
}

func TestOutcomeWriteFailureCanBeReconciledWithoutRestart(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	id, err := s.BeginAdminOperation(ctx, AdminAction{Action: "disconnect", TargetID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE ON admin_actions BEGIN SELECT RAISE(ABORT,'injected outcome failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAdminOperation(ctx, id, "applied", ""); err == nil {
		t.Fatal("completion ignored failed audit")
	}
	s.MarkAdminOperationUncertain(id)
	ops, err := s.ListAdminOperations(ctx, 10)
	if err != nil || len(ops) != 1 || ops[0].State != "needs_reconcile" {
		t.Fatalf("not recoverable: %+v %v", ops, err)
	}
	var state string
	if err = s.db.QueryRow(`SELECT state FROM admin_operations WHERE id=?`, id).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("outcome partially committed: %q %v", state, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_completion`); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileAdminOperation(ctx, id, "applied", "confirmed absent", AdminAction{Operator: "admin-test", TransportPeer: "127.0.0.1:4321", Reason: "manually verified"}); err != nil {
		t.Fatal(err)
	}
	actions, err := s.ListAdminActions(ctx, 0, 10)
	if err != nil || len(actions) != 2 || actions[0].Action != "reconcile_operation" || actions[0].TransportPeer != "127.0.0.1:4321" {
		t.Fatalf("reconciliation audit missing: %+v %v", actions, err)
	}
}

func TestCommitFailureDiscardsActiveTransactionAndPausesUntilRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commit-failure.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{`CREATE TABLE commit_fault(fingerprint TEXT REFERENCES clients(fingerprint) DEFERRABLE INITIALLY DEFERRED)`, `CREATE TRIGGER fail_commit BEFORE INSERT ON admin_actions BEGIN INSERT INTO commit_fault VALUES('missing-client'); END`} {
		if _, err = s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	fp := securityFingerprint("commit-failure")
	if _, err = s.BindIdentity(ctx, "device", fp, 0, AdminAction{Reason: "verified"}); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("commit error not distinguished: %v", err)
	}
	if !s.CommitUncertain() || !errors.Is(s.AdmissionError(ctx), ErrCommitOutcomeUnknown) {
		t.Fatal("uncertain store continued admitting work")
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.GetIdentity(ctx, "device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("failed commit left a durable binding: %v", err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM commit_fault`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("active failed transaction escaped cleanup: %d %v", n, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_commit`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindIdentity(ctx, "device", fp, 0, AdminAction{Reason: "verified after restart"}); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationBeforeDriverCommitRollsBackWithoutUncertainOutcome(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cancelled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO identity_bindings(client_id,fingerprint,generation,created_at,updated_at) VALUES('cancelled',?,1,1,1)`, securityFingerprint("cancelled")); e != nil {
			return e
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	if s.CommitUncertain() {
		t.Fatal("pre-commit cancellation treated as unknown commit")
	}
	if _, err = s.GetIdentity(context.Background(), "cancelled"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("cancelled binding survived: %v", err)
	}
}
