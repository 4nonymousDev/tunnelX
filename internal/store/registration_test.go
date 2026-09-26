package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func registrationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func registrationOperation(t *testing.T, s *Store, id string) AdminOperation {
	t.Helper()
	operations, err := s.ListAdminOperations(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation.ID == id {
			return operation
		}
	}
	t.Fatalf("operation %s missing", id)
	return AdminOperation{}
}

func TestClientRegistrationPreservesExistingDataAndIsIdempotent(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	fp := securityFingerprint("registered")
	firstSeen := time.Now().Add(-24 * time.Hour).Truncate(time.Microsecond)
	if err := s.ImportClient(ctx, fp, "original user", "original@example.invalid", "original computer", firstSeen); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateClientNote(ctx, fp, "retain my note"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartConnection(ctx, ConnectionAudit{Fingerprint: fp, Result: "rejected"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Block(ctx, BlockRequest{Entry: BlacklistEntry{Fingerprint: fp, Reason: "retain block"}}); err != nil {
		t.Fatal(err)
	}
	a := AdminAction{Operator: "admin", Reason: "verified supplied ID and public key", TransportPeer: "127.0.0.1:1234"}
	id, err := s.BeginClientRegistration(ctx, "office-device", fp, a)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateIdentity(ctx, fp, "office-device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("intent granted identity: %v", err)
	}
	operation := registrationOperation(t, s, id)
	clientID, targetFingerprint, err := ParseClientRegistrationTarget(operation.TargetID)
	if err != nil || clientID != "office-device" || targetFingerprint != fp || operation.Action != "register_client" || operation.State != "pending" {
		t.Fatalf("bad recovery target: %+v %v", operation, err)
	}
	if err = s.CompleteClientRegistration(ctx, id, "office-device", fp, "", "", ""); err != nil {
		t.Fatal(err)
	}
	binding, err := s.GetIdentity(ctx, "office-device")
	if err != nil || binding.Generation != 1 || binding.Revoked {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	// A retry of an already completed intent has no further metadata effects.
	if err = s.CompleteClientRegistration(ctx, id, "office-device", fp, "unexpected retry", "", ""); err != nil {
		t.Fatal(err)
	}
	id, err = s.BeginClientRegistration(ctx, "office-device", fp, a)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteClientRegistration(ctx, id, "office-device", fp, "", "", ""); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetIdentity(ctx, "office-device")
	if err != nil || again != binding {
		t.Fatalf("duplicate registration changed binding: %+v -> %+v (%v)", binding, again, err)
	}
	c, err := s.GetClient(ctx, fp)
	if err != nil || c.Username != "original user" || c.Email != "original@example.invalid" || c.ComputerName != "original computer" || c.Note != "retain my note" || !c.FirstSeenAt.Equal(firstSeen) || !c.LastSeenAt.Equal(firstSeen) {
		t.Fatalf("existing metadata changed: %+v %v", c, err)
	}
	if blocked, err := s.IsBlocked(ctx, fp, time.Now()); err != nil || !blocked {
		t.Fatalf("registration unblocked key: %v %v", blocked, err)
	}
	if rows, err := s.ListConnections(ctx, AuditFilter{Fingerprint: fp}); err != nil || len(rows) != 1 {
		t.Fatalf("history changed: %+v %v", rows, err)
	}
	actions, err := s.ListAdminActionsFiltered(ctx, AdminActionFilter{TargetID: fp})
	if err != nil || len(actions) != 2 {
		t.Fatalf("client audit missing: %+v %v", actions, err)
	}
	for _, action := range actions {
		if action.Action != "register_client" || action.TargetType != "client" || action.Result != "success" || action.Operator != a.Operator {
			t.Fatalf("bad audit: %+v", action)
		}
	}
}

func TestClientRegistrationRejectsConflictRevocationAndIntentSubstitution(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	fp, other := securityFingerprint("owner"), securityFingerprint("other")
	if _, err := s.BindIdentity(ctx, "occupied", fp, 0, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginClientRegistration(ctx, "occupied", other, AdminAction{}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("conflicting binding accepted: %v", err)
	}
	if err := s.RevokeIdentity(ctx, "occupied", 1, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginClientRegistration(ctx, "occupied", fp, AdminAction{}); !errors.Is(err, ErrIdentityRevoked) {
		t.Fatalf("revoked binding accepted: %v", err)
	}
	operations, err := s.ListAdminOperations(ctx, 10)
	if err != nil || len(operations) != 0 {
		t.Fatalf("failed validation wrote intent: %+v %v", operations, err)
	}
	id, err := s.BeginClientRegistration(ctx, "new-device", fp, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteClientRegistration(ctx, id, "substituted-device", fp, "", "", ""); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("substituted ID accepted: %v", err)
	}
	if err = s.CompleteClientRegistration(ctx, id, "new-device", other, "", "", ""); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("substituted key accepted: %v", err)
	}
	if _, err = s.BindIdentity(ctx, "new-device", other, 0, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteClientRegistration(ctx, id, "new-device", fp, "unexpected", "", ""); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("late conflict accepted: %v", err)
	}
	if _, err = s.GetClient(ctx, fp); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed completion wrote metadata: %v", err)
	}
	if operation := registrationOperation(t, s, id); operation.State != "pending" {
		t.Fatalf("failed completion changed operation: %+v", operation)
	}
	if err = s.ValidateIdentity(ctx, other, "new-device"); err != nil {
		t.Fatalf("conflicting identity modified: %v", err)
	}
}

func TestClientRegistrationAuditFailureRollsBackGrantAndMetadata(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	fp := securityFingerprint("audit-failure")
	id, err := s.BeginClientRegistration(ctx, "device", fp, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_registration_audit BEFORE UPDATE ON admin_actions BEGIN SELECT RAISE(ABORT,'audit write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteClientRegistration(ctx, id, "device", fp, "person", "", "computer"); err == nil {
		t.Fatal("registration ignored failed audit")
	}
	if _, err = s.GetIdentity(ctx, "device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("failed audit left trusted identity: %v", err)
	}
	if _, err = s.GetClient(ctx, fp); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed audit left metadata: %v", err)
	}
	if operation := registrationOperation(t, s, id); operation.State != "pending" {
		t.Fatalf("failed audit changed intent: %+v", operation)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_registration_audit`); err != nil {
		t.Fatal(err)
	}
	s.MarkAdminOperationUncertain(id)
	if err = s.ReconcileClientRegistration(ctx, id, true, AdminAction{Operator: "reconciler"}); err != nil {
		t.Fatal(err)
	}
	if operation := registrationOperation(t, s, id); operation.State != "failed" {
		t.Fatalf("authorized key without binding treated as registered: %+v", operation)
	}
	if _, err = s.GetIdentity(ctx, "device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("reconciliation replayed binding: %v", err)
	}
}

func TestClientRegistrationReconcileChecksBothSidesAndAuditsAtomically(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		authorized bool
		binding    bool
		revoked    bool
		want       string
	}{
		{name: "both-match", authorized: true, binding: true, want: "applied"},
		{name: "key-missing", binding: true, want: "failed"},
		{name: "binding-missing", authorized: true, want: "failed"},
		{name: "revoked", authorized: true, binding: true, revoked: true, want: "failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := registrationStore(t)
			ctx := context.Background()
			fp := securityFingerprint(scenario.name)
			id, err := s.BeginClientRegistration(ctx, "device", fp, AdminAction{})
			if err != nil {
				t.Fatal(err)
			}
			if scenario.binding {
				if _, err = s.BindIdentity(ctx, "device", fp, 0, AdminAction{}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.revoked {
				if err = s.RevokeIdentity(ctx, "device", 1, AdminAction{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.db.Exec(`CREATE TRIGGER fail_reconcile_audit BEFORE INSERT ON admin_actions WHEN NEW.action='reconcile_operation' BEGIN SELECT RAISE(ABORT,'reconcile audit failed'); END`); err != nil {
				t.Fatal(err)
			}
			a := AdminAction{Operator: "reconciler", Reason: "inspected current registration", TransportPeer: "127.0.0.1:3333"}
			if err = s.ReconcileClientRegistration(ctx, id, scenario.authorized, a); err == nil {
				t.Fatal("reconciliation ignored audit failure")
			}
			if operation := registrationOperation(t, s, id); operation.State != "pending" {
				t.Fatalf("failed reconciliation changed state: %+v", operation)
			}
			if _, err = s.db.Exec(`DROP TRIGGER fail_reconcile_audit`); err != nil {
				t.Fatal(err)
			}
			if err = s.ReconcileClientRegistration(ctx, id, scenario.authorized, a); err != nil {
				t.Fatal(err)
			}
			if operation := registrationOperation(t, s, id); operation.State != scenario.want {
				t.Fatalf("reconciled state=%s want=%s", operation.State, scenario.want)
			}
			actions, err := s.ListAdminActionsFiltered(ctx, AdminActionFilter{TargetID: id})
			if err != nil || len(actions) != 1 || actions[0].Action != "reconcile_operation" || actions[0].Operator != a.Operator || actions[0].TransportPeer != a.TransportPeer {
				t.Fatalf("reconciliation audit missing: %+v %v", actions, err)
			}
			if scenario.revoked {
				if err = s.ValidateIdentity(ctx, fp, "device"); !errors.Is(err, ErrIdentityRevoked) {
					t.Fatalf("reconciliation restored revoked identity: %v", err)
				}
			}
		})
	}
}

func TestClientRegistrationRestartDoesNotReplayMissingBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registration.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fp := securityFingerprint("restart")
	id, err := s.BeginClientRegistration(ctx, "device", fp, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if operation := registrationOperation(t, s, id); operation.State != "needs_reconcile" {
		t.Fatalf("restart lost unfinished registration: %+v", operation)
	}
	if err = s.CompleteClientRegistration(ctx, id, "device", fp, "", "", ""); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("uncertain operation replayed: %v", err)
	}
	if err = s.ReconcileClientRegistration(ctx, id, true, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetIdentity(ctx, "device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("restart created a trusted binding: %v", err)
	}
}

func TestClientRegistrationCommitFailurePausesAndRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registration-commit.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fp := securityFingerprint("registration-commit")
	id, err := s.BeginClientRegistration(ctx, "device", fp, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	// SQLite accepts the transaction's statements, then fails COMMIT on the
	// deferred foreign key. This exercises the real unknown-outcome path.
	for _, query := range []string{
		`CREATE TABLE registration_commit_fault(fingerprint TEXT REFERENCES clients(fingerprint) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER fail_registration_commit BEFORE UPDATE ON admin_actions BEGIN INSERT INTO registration_commit_fault VALUES('missing-client'); END`,
	} {
		if _, err = s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CompleteClientRegistration(ctx, id, "device", fp, "person", "", ""); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("COMMIT failure lost fail-closed semantics: %v", err)
	}
	if !s.CommitUncertain() || !errors.Is(s.CheckAdmission(ctx), ErrCommitOutcomeUnknown) {
		t.Fatal("uncertain registration allowed further admission")
	}
	s.Close()
	// Remove only the test fault using a fresh connection before normal startup
	// reconciliation performs its own transaction.
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TRIGGER fail_registration_commit`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.GetIdentity(ctx, "device"); !errors.Is(err, ErrIdentityUnbound) {
		t.Fatalf("failed COMMIT retained identity: %v", err)
	}
	if _, err = s.GetClient(ctx, fp); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed COMMIT retained metadata: %v", err)
	}
	if operation := registrationOperation(t, s, id); operation.State != "needs_reconcile" {
		t.Fatalf("original intent lost after COMMIT failure: %+v", operation)
	}
	if err = s.ReconcileClientRegistration(ctx, id, true, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	if operation := registrationOperation(t, s, id); operation.State != "failed" {
		t.Fatalf("COMMIT recovery replayed registration: %+v", operation)
	}
}

func TestParseClientRegistrationTargetRejectsAmbiguity(t *testing.T) {
	fp := securityFingerprint("parse")
	raw, err := json.Marshal(clientRegistrationTarget{ClientID: "office-设备", Fingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	id, gotFingerprint, err := ParseClientRegistrationTarget(" \n" + string(raw) + " \t")
	if err != nil || id != "office-设备" || gotFingerprint != fp {
		t.Fatalf("valid target rejected: %q %q %v", id, gotFingerprint, err)
	}
	for _, invalid := range []string{
		`null`, `[]`, `{}`, string(raw) + `{}`, string(raw) + ` trailing`,
		`{"client_id":"a","client_id":"b","fingerprint":"` + fp + `"}`,
		`{"client_id":"a","fingerprint":"` + fp + `","unknown":true}`,
		`{"client_id":"a","fingerprint":"invalid"}`,
		`{"client_id":" a","fingerprint":"` + fp + `"}`,
		`{"client_id":null,"fingerprint":"` + fp + `"}`,
		`{"client_id":"a\n","fingerprint":"` + fp + `"}`,
		`{"client_id":"a","fingerprint":"` + fp + `","fingerprint":"` + fp + `"}`,
		strings.Repeat(" ", 4097),
	} {
		if _, _, err = ParseClientRegistrationTarget(invalid); err == nil {
			t.Errorf("invalid target accepted: %q", invalid)
		}
	}
}
