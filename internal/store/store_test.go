package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistenceAndAtomicBlock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "management.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 1, 2, 3, 4, 5, 6000, time.FixedZone("x", 8*3600))
	if err = s.UpsertClient(ctx, Seen{Fingerprint: "SHA256:abc", IP: "127.0.0.1", ClientID: "c1", At: now}); err != nil {
		t.Fatal(err)
	}
	exp := now.Add(time.Hour)
	if err = s.Block(ctx, BlockRequest{Entry: BlacklistEntry{Fingerprint: "SHA256:abc", Reason: "test", Operator: "admin-token", CreatedAt: now, ExpiresAt: &exp}, Action: AdminAction{Action: "block", TargetType: "client", TargetID: "SHA256:abc", Reason: "test", Result: "success", Operator: "admin-token", TransportPeer: "127.0.0.1", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.GetClient(ctx, "SHA256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if !c.LastSeenAt.Equal(now.UTC()) {
		t.Fatalf("time=%s want %s", c.LastSeenAt, now.UTC())
	}
	blocked, err := s.IsBlocked(ctx, "SHA256:abc", now)
	if err != nil || !blocked {
		t.Fatalf("blocked=%v err=%v", blocked, err)
	}
	actions, err := s.ListAdminActions(ctx, 0, 10)
	if err != nil || len(actions) != 1 {
		t.Fatalf("actions=%d err=%v", len(actions), err)
	}
}

func TestConnectionCursorAndIdempotentFinish(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	sid := "session"
	id, err := s.StartConnection(ctx, ConnectionAudit{SessionID: &sid, Fingerprint: "SHA256:a", AuthenticatedAt: time.Unix(1, 123000), Result: "authenticated"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishConnection(ctx, id, "disconnected", "natural", time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishConnection(ctx, id, "changed", "duplicate", time.Unix(3, 0)); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListConnections(ctx, AuditFilter{Fingerprint: "SHA256:a", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Result != "disconnected" || rows[0].DisconnectedAt == nil || !rows[0].DisconnectedAt.Equal(time.Unix(2, 0).UTC()) {
		t.Fatalf("row=%#v", rows)
	}
	if _, err = s.GetClient(ctx, "missing"); err != sql.ErrNoRows {
		t.Fatalf("got %v", err)
	}
}

func TestExpiredBlacklist(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Microsecond)
	if err = s.Block(ctx, BlockRequest{Entry: BlacklistEntry{Fingerprint: "SHA256:a", ExpiresAt: &past}, Action: AdminAction{Action: "block", TargetType: "client", TargetID: "SHA256:a", Reason: "r", Result: "success", Operator: "admin-token", TransportPeer: "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ActiveBlacklist(ctx, now)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestUpsertClientEmptyReportedFieldsDoNotEraseHello(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	first := time.Unix(10, 0)
	second := first.Add(time.Minute)
	if err = s.UpsertClient(ctx, Seen{Fingerprint: "SHA256:a", IP: "10.0.0.1", ClientID: "client", ReportedName: "name", Role: "exporter", Version: "v1", At: first}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertClient(ctx, Seen{Fingerprint: "SHA256:a", IP: "10.0.0.2", At: second}); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetClient(ctx, "SHA256:a")
	if err != nil {
		t.Fatal(err)
	}
	if c.LastIP != "10.0.0.2" || !c.LastSeenAt.Equal(second.UTC()) || c.LastClientID != "client" || c.LastReportedName != "name" || c.LastRole != "exporter" || c.LastVersion != "v1" {
		t.Fatalf("client=%#v", c)
	}
}

func TestStableTimeIDCursorAcrossAuditPages(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	at := time.Unix(100, 123000)
	for i := 0; i < 3; i++ {
		if _, err = s.StartConnection(ctx, ConnectionAudit{Fingerprint: "SHA256:a", AuthenticatedAt: at, Result: "ok"}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.StartAccess(ctx, AccessAudit{VisitorFingerprint: "SHA256:a", StartedAt: at, Result: "ok"}); err != nil {
			t.Fatal(err)
		}
		if err = s.RecordAdminAction(ctx, AdminAction{Action: "test", TargetType: "client", TargetID: "target", Reason: "r", Result: "success", Operator: "admin-token", TransportPeer: "127.0.0.1", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	connections, err := s.ListConnections(ctx, AuditFilter{Limit: 2})
	if err != nil || len(connections) != 2 {
		t.Fatalf("connections=%v err=%v", connections, err)
	}
	cursorTime := connections[1].AuthenticatedAt
	nextConnections, err := s.ListConnections(ctx, AuditFilter{CursorTime: &cursorTime, CursorID: connections[1].ID, Limit: 2})
	if err != nil || len(nextConnections) != 1 || nextConnections[0].ID >= connections[1].ID {
		t.Fatalf("next connections=%v err=%v", nextConnections, err)
	}
	accesses, err := s.ListAccesses(ctx, AccessFilter{Limit: 2})
	if err != nil || len(accesses) != 2 {
		t.Fatalf("accesses=%v err=%v", accesses, err)
	}
	accessCursor := accesses[1].StartedAt
	nextAccesses, err := s.ListAccesses(ctx, AccessFilter{CursorTime: &accessCursor, CursorID: accesses[1].ID, Limit: 2})
	if err != nil || len(nextAccesses) != 1 || nextAccesses[0].ID >= accesses[1].ID {
		t.Fatalf("next accesses=%v err=%v", nextAccesses, err)
	}
	from, to := at.Add(-time.Microsecond), at.Add(time.Microsecond)
	actions, err := s.ListAdminActionsFiltered(ctx, AdminActionFilter{TargetID: "target", Result: "success", Since: &from, Until: &to, Limit: 2})
	if err != nil || len(actions) != 2 {
		t.Fatalf("actions=%v err=%v", actions, err)
	}
	actionCursor := actions[1].CreatedAt
	nextActions, err := s.ListAdminActionsFiltered(ctx, AdminActionFilter{TargetID: "target", Result: "success", Since: &from, Until: &to, CursorTime: &actionCursor, CursorID: actions[1].ID, Limit: 2})
	if err != nil || len(nextActions) != 1 || nextActions[0].ID >= actions[1].ID {
		t.Fatalf("next actions=%v err=%v", nextActions, err)
	}
}
