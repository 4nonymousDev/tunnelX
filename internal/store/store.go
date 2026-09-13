// Package store persists management data and audit history in SQLite. Online
// state intentionally does not belong here.
package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 2
const microsPerSecond = int64(time.Second / time.Microsecond)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA busy_timeout = 5000"} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite initialization %q: %w", q, err)
		}
	}
	s := &Store{db: db}
	if err = s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported %d", v, schemaVersion)
	}
	if v < 1 {
		for _, q := range schemaV1 {
			if _, err = tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("migrate schema v1: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return err
		}
	}
	if v < 2 {
		for _, q := range []string{
			"ALTER TABLE clients ADD COLUMN username TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE clients ADD COLUMN email TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE clients ADD COLUMN computer_name TEXT NOT NULL DEFAULT ''",
		} {
			if _, err = tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("migrate schema v2: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var schemaV1 = []string{
	`CREATE TABLE clients(fingerprint TEXT PRIMARY KEY,note TEXT NOT NULL DEFAULT '',first_seen_at INTEGER NOT NULL,last_seen_at INTEGER NOT NULL,last_ip TEXT NOT NULL DEFAULT '',last_client_id TEXT NOT NULL DEFAULT '',last_reported_name TEXT NOT NULL DEFAULT '',last_role TEXT NOT NULL DEFAULT '',last_version TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE blacklist(fingerprint TEXT PRIMARY KEY,reason TEXT NOT NULL,operator TEXT NOT NULL,created_at INTEGER NOT NULL,expires_at INTEGER NULL)`,
	`CREATE TABLE connection_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,session_id TEXT NULL,fingerprint TEXT NOT NULL DEFAULT '',client_id TEXT NOT NULL DEFAULT '',remote_ip TEXT NOT NULL DEFAULT '',authenticated_at INTEGER NOT NULL,hello_at INTEGER NULL,disconnected_at INTEGER NULL,result TEXT NOT NULL,disconnect_reason TEXT NOT NULL DEFAULT '',reported_name TEXT NOT NULL DEFAULT '',role TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE access_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,visitor_session_id TEXT NOT NULL DEFAULT '',visitor_fingerprint TEXT NOT NULL DEFAULT '',visitor_client_id TEXT NOT NULL DEFAULT '',target_session_id TEXT NOT NULL DEFAULT '',target_fingerprint TEXT NOT NULL DEFAULT '',target_client_id TEXT NOT NULL DEFAULT '',target_tunnel_id TEXT NOT NULL DEFAULT '',target_tunnel_name TEXT NOT NULL DEFAULT '',target_remote_port INTEGER NOT NULL DEFAULT 0,started_at INTEGER NOT NULL,ended_at INTEGER NULL,result TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',bytes_sent INTEGER NOT NULL DEFAULT 0,bytes_received INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE admin_actions(id INTEGER PRIMARY KEY AUTOINCREMENT,action TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,reason TEXT NOT NULL,result TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',operator TEXT NOT NULL,transport_peer TEXT NOT NULL,source_ip TEXT NULL,created_at INTEGER NOT NULL)`,
	`CREATE INDEX connection_audit_time ON connection_audit(authenticated_at DESC,id DESC)`, `CREATE INDEX connection_audit_fingerprint_time ON connection_audit(fingerprint,authenticated_at DESC)`, `CREATE INDEX connection_audit_session ON connection_audit(session_id)`, `CREATE INDEX connection_audit_client_time ON connection_audit(client_id,authenticated_at DESC)`,
	`CREATE INDEX access_audit_time ON access_audit(started_at DESC,id DESC)`, `CREATE INDEX access_audit_target_fingerprint_time ON access_audit(target_fingerprint,started_at DESC)`, `CREATE INDEX access_audit_visitor_fingerprint_time ON access_audit(visitor_fingerprint,started_at DESC)`,
	`CREATE INDEX admin_actions_time ON admin_actions(created_at DESC,id DESC)`, `CREATE INDEX blacklist_expires_at ON blacklist(expires_at)`,
}

func dbtime(t time.Time) int64   { return t.UTC().UnixMicro() }
func scantime(v int64) time.Time { return time.Unix(0, v*int64(time.Microsecond)).UTC() }
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return dbtime(*t)
}

type Client struct {
	Fingerprint, Note, Username, Email, ComputerName, LastIP, LastClientID, LastReportedName, LastRole, LastVersion string
	FirstSeenAt, LastSeenAt                                                                                         time.Time
}

func (s *Store) ImportClient(ctx context.Context, fp, username, email, computerName string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO clients(fingerprint,first_seen_at,last_seen_at,username,email,computer_name,last_reported_name) VALUES(?,?,?,?,?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET username=excluded.username,email=excluded.email,computer_name=excluded.computer_name,last_reported_name=CASE WHEN clients.last_reported_name='' THEN excluded.computer_name ELSE clients.last_reported_name END`, fp, dbtime(at), dbtime(at), username, email, computerName, computerName)
	return err
}

type Seen struct {
	Fingerprint, IP, ClientID, ReportedName, Role, Version string
	At                                                     time.Time
}

func (s *Store) UpsertClient(ctx context.Context, x Seen) error {
	if x.At.IsZero() {
		x.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO clients(fingerprint,first_seen_at,last_seen_at,last_ip,last_client_id,last_reported_name,last_role,last_version) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET last_seen_at=excluded.last_seen_at,last_ip=excluded.last_ip,last_client_id=CASE WHEN excluded.last_client_id<>'' THEN excluded.last_client_id ELSE clients.last_client_id END,last_reported_name=CASE WHEN excluded.last_reported_name<>'' THEN excluded.last_reported_name ELSE clients.last_reported_name END,last_role=CASE WHEN excluded.last_role<>'' THEN excluded.last_role ELSE clients.last_role END,last_version=CASE WHEN excluded.last_version<>'' THEN excluded.last_version ELSE clients.last_version END`, x.Fingerprint, dbtime(x.At), dbtime(x.At), x.IP, x.ClientID, x.ReportedName, x.Role, x.Version)
	return err
}
func (s *Store) UpdateClientNote(ctx context.Context, fp, note string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE clients SET note=? WHERE fingerprint=?", note, fp)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) GetClient(ctx context.Context, fp string) (Client, error) {
	var c Client
	var f, l int64
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint,note,username,email,computer_name,first_seen_at,last_seen_at,last_ip,last_client_id,last_reported_name,last_role,last_version FROM clients WHERE fingerprint=?`, fp).Scan(&c.Fingerprint, &c.Note, &c.Username, &c.Email, &c.ComputerName, &f, &l, &c.LastIP, &c.LastClientID, &c.LastReportedName, &c.LastRole, &c.LastVersion)
	c.FirstSeenAt = scantime(f)
	c.LastSeenAt = scantime(l)
	return c, err
}
func (s *Store) ListClients(ctx context.Context, limit int, afterFingerprint string) ([]Client, error) {
	limit = normalizeLimit(limit)
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint,note,username,email,computer_name,first_seen_at,last_seen_at,last_ip,last_client_id,last_reported_name,last_role,last_version FROM clients WHERE fingerprint>? ORDER BY fingerprint LIMIT ?`, afterFingerprint, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		var c Client
		var f, l int64
		if err = rows.Scan(&c.Fingerprint, &c.Note, &c.Username, &c.Email, &c.ComputerName, &f, &l, &c.LastIP, &c.LastClientID, &c.LastReportedName, &c.LastRole, &c.LastVersion); err != nil {
			return nil, err
		}
		c.FirstSeenAt = scantime(f)
		c.LastSeenAt = scantime(l)
		out = append(out, c)
	}
	return out, rows.Err()
}

type BlacklistEntry struct {
	Fingerprint, Reason, Operator string
	CreatedAt                     time.Time
	ExpiresAt                     *time.Time
}

func (s *Store) ActiveBlacklist(ctx context.Context, at time.Time) ([]BlacklistEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint,reason,operator,created_at,expires_at FROM blacklist WHERE expires_at IS NULL OR expires_at>?`, dbtime(at))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlacklistEntry
	for rows.Next() {
		var b BlacklistEntry
		var created int64
		var exp sql.NullInt64
		if err = rows.Scan(&b.Fingerprint, &b.Reason, &b.Operator, &created, &exp); err != nil {
			return nil, err
		}
		b.CreatedAt = scantime(created)
		if exp.Valid {
			x := scantime(exp.Int64)
			b.ExpiresAt = &x
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (s *Store) IsBlocked(ctx context.Context, fp string, at time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM blacklist WHERE fingerprint=? AND (expires_at IS NULL OR expires_at>?)`, fp, dbtime(at)).Scan(&n)
	return n != 0, err
}

type AdminAction struct {
	ID                                                                           int64
	Action, TargetType, TargetID, Reason, Result, Error, Operator, TransportPeer string
	SourceIP                                                                     *string
	CreatedAt                                                                    time.Time
}
type BlockRequest struct {
	Entry  BlacklistEntry
	Action AdminAction
}

func (s *Store) Block(ctx context.Context, r BlockRequest) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		b := r.Entry
		if b.CreatedAt.IsZero() {
			b.CreatedAt = time.Now()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blacklist(fingerprint,reason,operator,created_at,expires_at) VALUES(?,?,?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET reason=excluded.reason,operator=excluded.operator,created_at=excluded.created_at,expires_at=excluded.expires_at`, b.Fingerprint, b.Reason, b.Operator, dbtime(b.CreatedAt), nullableTime(b.ExpiresAt)); err != nil {
			return err
		}
		return insertAdmin(ctx, tx, r.Action)
	})
}
func (s *Store) Unblock(ctx context.Context, fp string, a AdminAction) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM blacklist WHERE fingerprint=?", fp); err != nil {
			return err
		}
		return insertAdmin(ctx, tx, a)
	})
}
func (s *Store) RecordAdminAction(ctx context.Context, a AdminAction) error {
	return insertAdmin(ctx, s.db, a)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAdmin(ctx context.Context, e execer, a AdminAction) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := e.ExecContext(ctx, `INSERT INTO admin_actions(action,target_type,target_id,reason,result,error,operator,transport_peer,source_ip,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, a.Action, a.TargetType, a.TargetID, a.Reason, a.Result, a.Error, a.Operator, a.TransportPeer, a.SourceIP, dbtime(a.CreatedAt))
	return err
}
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type ConnectionAudit struct {
	ID                                                                                     int64
	SessionID                                                                              *string
	Fingerprint, ClientID, RemoteIP, Result, DisconnectReason, ReportedName, Role, Version string
	AuthenticatedAt                                                                        time.Time
	HelloAt, DisconnectedAt                                                                *time.Time
}

func (s *Store) StartConnection(ctx context.Context, a ConnectionAudit) (int64, error) {
	if a.AuthenticatedAt.IsZero() {
		a.AuthenticatedAt = time.Now()
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO connection_audit(session_id,fingerprint,client_id,remote_ip,authenticated_at,result,disconnect_reason) VALUES(?,?,?,?,?,?,?)`, a.SessionID, a.Fingerprint, a.ClientID, a.RemoteIP, dbtime(a.AuthenticatedAt), a.Result, a.DisconnectReason)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}
func (s *Store) CompleteHello(ctx context.Context, id int64, a ConnectionAudit) error {
	at := a.HelloAt
	if at == nil {
		x := time.Now()
		at = &x
	}
	_, err := s.db.ExecContext(ctx, `UPDATE connection_audit SET client_id=?,hello_at=?,result=?,disconnect_reason=?,reported_name=?,role=?,version=? WHERE id=?`, a.ClientID, dbtime(*at), a.Result, a.DisconnectReason, a.ReportedName, a.Role, a.Version, id)
	return err
}
func (s *Store) FinishConnection(ctx context.Context, id int64, result, reason string, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE connection_audit SET disconnected_at=?,result=?,disconnect_reason=? WHERE id=? AND disconnected_at IS NULL`, dbtime(at), result, reason, id)
	return err
}

type AccessAudit struct {
	ID                                                                                                                                          int64
	VisitorSessionID, VisitorFingerprint, VisitorClientID, TargetSessionID, TargetFingerprint, TargetClientID, TargetTunnelID, TargetTunnelName string
	TargetRemotePort                                                                                                                            int
	StartedAt                                                                                                                                   time.Time
	EndedAt                                                                                                                                     *time.Time
	Result, Reason                                                                                                                              string
	BytesSent, BytesReceived                                                                                                                    int64
}

func (s *Store) StartAccess(ctx context.Context, a AccessAudit) (int64, error) {
	if a.StartedAt.IsZero() {
		a.StartedAt = time.Now()
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO access_audit(visitor_session_id,visitor_fingerprint,visitor_client_id,target_session_id,target_fingerprint,target_client_id,target_tunnel_id,target_tunnel_name,target_remote_port,started_at,result,reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, a.VisitorSessionID, a.VisitorFingerprint, a.VisitorClientID, a.TargetSessionID, a.TargetFingerprint, a.TargetClientID, a.TargetTunnelID, a.TargetTunnelName, a.TargetRemotePort, dbtime(a.StartedAt), a.Result, a.Reason)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}
func (s *Store) FinishAccess(ctx context.Context, id int64, result, reason string, sent, received int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE access_audit SET ended_at=?,result=?,reason=?,bytes_sent=?,bytes_received=? WHERE id=? AND ended_at IS NULL`, dbtime(at), result, reason, sent, received, id)
	return err
}

type AccessFilter struct {
	Fingerprint        string
	ClientID           string
	VisitorFingerprint string
	TargetFingerprint  string
	Result             string
	SessionID          string
	Since, Until       *time.Time
	BeforeID           int64
	CursorTime         *time.Time
	CursorID           int64
	Limit              int
}

func (s *Store) ListAccesses(ctx context.Context, f AccessFilter) ([]AccessAudit, error) {
	q := `SELECT id,visitor_session_id,visitor_fingerprint,visitor_client_id,target_session_id,target_fingerprint,target_client_id,target_tunnel_id,target_tunnel_name,target_remote_port,started_at,ended_at,result,reason,bytes_sent,bytes_received FROM access_audit WHERE 1=1`
	args := []any{}
	if f.Fingerprint != "" {
		q += " AND (visitor_fingerprint=? OR target_fingerprint=?)"
		args = append(args, f.Fingerprint, f.Fingerprint)
	}
	if f.ClientID != "" {
		q += " AND (visitor_client_id=? OR target_client_id=?)"
		args = append(args, f.ClientID, f.ClientID)
	}
	if f.VisitorFingerprint != "" {
		q += " AND visitor_fingerprint=?"
		args = append(args, f.VisitorFingerprint)
	}
	if f.TargetFingerprint != "" {
		q += " AND target_fingerprint=?"
		args = append(args, f.TargetFingerprint)
	}
	if f.SessionID != "" {
		q += " AND (visitor_session_id=? OR target_session_id=?)"
		args = append(args, f.SessionID, f.SessionID)
	}
	if f.Result != "" {
		q += " AND result=?"
		args = append(args, f.Result)
	}
	if f.Since != nil {
		q += " AND started_at>=?"
		args = append(args, dbtime(*f.Since))
	}
	if f.Until != nil {
		q += " AND started_at<?"
		args = append(args, dbtime(*f.Until))
	}
	if f.CursorTime != nil {
		q += " AND (started_at<? OR (started_at=? AND id<?))"
		cursorTime := dbtime(*f.CursorTime)
		args = append(args, cursorTime, cursorTime, f.CursorID)
	} else if f.BeforeID > 0 {
		q += " AND id<?"
		args = append(args, f.BeforeID)
	}
	q += " ORDER BY started_at DESC,id DESC LIMIT ?"
	args = append(args, normalizeLimit(f.Limit))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessAudit
	for rows.Next() {
		var a AccessAudit
		var started int64
		var ended sql.NullInt64
		if err = rows.Scan(&a.ID, &a.VisitorSessionID, &a.VisitorFingerprint, &a.VisitorClientID, &a.TargetSessionID, &a.TargetFingerprint, &a.TargetClientID, &a.TargetTunnelID, &a.TargetTunnelName, &a.TargetRemotePort, &started, &ended, &a.Result, &a.Reason, &a.BytesSent, &a.BytesReceived); err != nil {
			return nil, err
		}
		a.StartedAt = scantime(started)
		a.EndedAt = scanNullTime(ended)
		out = append(out, a)
	}
	return out, rows.Err()
}

type AuditFilter struct {
	Fingerprint, ClientID, SessionID, Result string
	Since, Until                             *time.Time
	BeforeID                                 int64
	CursorTime                               *time.Time
	CursorID                                 int64
	Limit                                    int
}

func (s *Store) ListConnections(ctx context.Context, f AuditFilter) ([]ConnectionAudit, error) {
	q := `SELECT id,session_id,fingerprint,client_id,remote_ip,authenticated_at,hello_at,disconnected_at,result,disconnect_reason,reported_name,role,version FROM connection_audit WHERE 1=1`
	args := []any{}
	q, args = applyAuditFilter(q, args, f, "authenticated_at")
	q += " ORDER BY authenticated_at DESC,id DESC LIMIT ?"
	args = append(args, normalizeLimit(f.Limit))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionAudit
	for rows.Next() {
		var a ConnectionAudit
		var auth int64
		var hello, disc sql.NullInt64
		if err = rows.Scan(&a.ID, &a.SessionID, &a.Fingerprint, &a.ClientID, &a.RemoteIP, &auth, &hello, &disc, &a.Result, &a.DisconnectReason, &a.ReportedName, &a.Role, &a.Version); err != nil {
			return nil, err
		}
		a.AuthenticatedAt = scantime(auth)
		a.HelloAt = scanNullTime(hello)
		a.DisconnectedAt = scanNullTime(disc)
		out = append(out, a)
	}
	return out, rows.Err()
}
func applyAuditFilter(q string, args []any, f AuditFilter, timecol string) (string, []any) {
	if f.Fingerprint != "" {
		q += " AND fingerprint=?"
		args = append(args, f.Fingerprint)
	}
	if f.ClientID != "" {
		q += " AND client_id=?"
		args = append(args, f.ClientID)
	}
	if f.SessionID != "" {
		q += " AND session_id=?"
		args = append(args, f.SessionID)
	}
	if f.Result != "" {
		q += " AND result=?"
		args = append(args, f.Result)
	}
	if f.Since != nil {
		q += " AND " + timecol + ">=?"
		args = append(args, dbtime(*f.Since))
	}
	if f.Until != nil {
		q += " AND " + timecol + "<?"
		args = append(args, dbtime(*f.Until))
	}
	if f.CursorTime != nil {
		q += " AND (" + timecol + "<? OR (" + timecol + "=? AND id<?))"
		cursorTime := dbtime(*f.CursorTime)
		args = append(args, cursorTime, cursorTime, f.CursorID)
	} else if f.BeforeID > 0 {
		q += " AND id<?"
		args = append(args, f.BeforeID)
	}
	return q, args
}
func scanNullTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := scantime(n.Int64)
	return &t
}
func normalizeLimit(n int) int {
	if n <= 0 {
		return 50
	}
	// HTTP endpoints expose at most 200 items, while backend adapters request
	// one extra row to determine whether a stable next cursor is needed.
	if n > 201 {
		return 201
	}
	return n
}

func (s *Store) ListAdminActions(ctx context.Context, beforeID int64, limit int) ([]AdminAction, error) {
	return s.ListAdminActionsFiltered(ctx, AdminActionFilter{BeforeID: beforeID, Limit: limit})
}

type AdminActionFilter struct {
	TargetID, Result string
	Since, Until     *time.Time
	BeforeID         int64
	CursorTime       *time.Time
	CursorID         int64
	Limit            int
}

func (s *Store) ListAdminActionsFiltered(ctx context.Context, f AdminActionFilter) ([]AdminAction, error) {
	q := `SELECT id,action,target_type,target_id,reason,result,error,operator,transport_peer,source_ip,created_at FROM admin_actions WHERE 1=1`
	var args []any
	if f.TargetID != "" {
		q += " AND target_id=?"
		args = append(args, f.TargetID)
	}
	if f.Result != "" {
		q += " AND result=?"
		args = append(args, f.Result)
	}
	if f.Since != nil {
		q += " AND created_at>=?"
		args = append(args, dbtime(*f.Since))
	}
	if f.Until != nil {
		q += " AND created_at<?"
		args = append(args, dbtime(*f.Until))
	}
	if f.CursorTime != nil && f.CursorID > 0 {
		q += " AND (created_at<? OR (created_at=? AND id<?))"
		cursorTime := dbtime(*f.CursorTime)
		args = append(args, cursorTime, cursorTime, f.CursorID)
	} else if f.BeforeID > 0 {
		q += " AND id<?"
		args = append(args, f.BeforeID)
	}
	q += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, normalizeLimit(f.Limit))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminAction
	for rows.Next() {
		var a AdminAction
		var ip sql.NullString
		var at int64
		if err = rows.Scan(&a.ID, &a.Action, &a.TargetType, &a.TargetID, &a.Reason, &a.Result, &a.Error, &a.Operator, &a.TransportPeer, &ip, &at); err != nil {
			return nil, err
		}
		if ip.Valid {
			a.SourceIP = &ip.String
		}
		a.CreatedAt = scantime(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) TodayRejected(ctx context.Context, now time.Time) (int64, time.Time, error) {
	local := now.In(time.Local)
	startLocal := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	start := startLocal.UTC()
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM connection_audit WHERE authenticated_at>=? AND result IN ('rejected','blocked')`, dbtime(start)).Scan(&n)
	return n, start, err
}

var ErrInvalidCursor = errors.New("invalid cursor")

func ValidateFingerprint(fp string) bool {
	const prefix = "SHA256:"
	if len(fp) != len(prefix)+43 || fp[:len(prefix)] != prefix {
		return false
	}
	b, err := base64.RawStdEncoding.DecodeString(fp[len(prefix):])
	return err == nil && len(b) == sha256DigestSize
}

// TimeUnit documents the on-disk UTC integer representation.
const TimeUnit = "unix_microseconds"
const sha256DigestSize = 32

var _ = microsPerSecond
