package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrIdentityUnbound  = errors.New("device identity requires administrator approval")
	ErrIdentityMismatch = errors.New("device identity belongs to another public key")
	ErrIdentityRevoked  = errors.New("device identity has been revoked")
	ErrIdentityConflict = errors.New("device identity changed; reload before updating")
)

var schemaV3 = []string{
	`CREATE TABLE identity_bindings(client_id TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,generation INTEGER NOT NULL CHECK(generation>0),revoked INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`,
	`CREATE INDEX identity_fingerprint ON identity_bindings(fingerprint)`,
	`CREATE TABLE identity_claims(fingerprint TEXT NOT NULL,client_id TEXT NOT NULL,reported_name TEXT NOT NULL DEFAULT '',remote_ip TEXT NOT NULL DEFAULT '',last_seen_at INTEGER NOT NULL,PRIMARY KEY(fingerprint,client_id))`,
	`CREATE INDEX identity_claim_time ON identity_claims(last_seen_at)`,
	`CREATE TABLE rejection_counts(bucket_at INTEGER NOT NULL,reason TEXT NOT NULL,count INTEGER NOT NULL CHECK(count>=0),PRIMARY KEY(bucket_at,reason))`,
	`CREATE TABLE admin_operations(id TEXT PRIMARY KEY,action TEXT NOT NULL,target_id TEXT NOT NULL,state TEXT NOT NULL,audit_id INTEGER NOT NULL REFERENCES admin_actions(id),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`,
	`CREATE INDEX admin_operation_state ON admin_operations(state,created_at)`,
}

type IdentityBinding struct {
	ClientID    string    `json:"client_id"`
	Fingerprint string    `json:"fingerprint"`
	Generation  int64     `json:"generation"`
	Revoked     bool      `json:"revoked"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type IdentityClaim struct {
	Fingerprint  string    `json:"fingerprint"`
	ClientID     string    `json:"client_id"`
	ReportedName string    `json:"reported_name"`
	RemoteIP     string    `json:"remote_ip"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

func ValidClientID(id string) bool {
	if id == "" || strings.TrimSpace(id) != id || !utf8.ValidString(id) || utf8.RuneCountInString(id) > 128 {
		return false
	}
	return strings.IndexFunc(id, unicode.IsControl) < 0
}

func (s *Store) GetIdentity(ctx context.Context, id string) (IdentityBinding, error) {
	var b IdentityBinding
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT client_id,fingerprint,generation,revoked,created_at,updated_at FROM identity_bindings WHERE client_id=?`, id).Scan(&b.ClientID, &b.Fingerprint, &b.Generation, &b.Revoked, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrIdentityUnbound
	}
	b.CreatedAt, b.UpdatedAt = scantime(created), scantime(updated)
	return b, err
}

func (s *Store) ValidateIdentity(ctx context.Context, fp, id string) error {
	if !ValidClientID(id) || !ValidateFingerprint(fp) {
		return ErrIdentityMismatch
	}
	b, err := s.GetIdentity(ctx, id)
	if err != nil {
		return err
	}
	if b.Revoked {
		return ErrIdentityRevoked
	}
	if b.Fingerprint != fp {
		return ErrIdentityMismatch
	}
	// A migrated authorization remains scoped to the exact enrolled device ID,
	// even if another historical identity happens to reference the same key.
	var originalID string
	var enabled bool
	err = s.db.QueryRowContext(ctx, `SELECT client_id,enabled FROM device_keys WHERE fingerprint=?`, fp).Scan(&originalID, &enabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (!enabled || originalID != id) {
		return ErrIdentityMismatch
	}
	return nil
}

// RecordIdentityClaim records only an untrusted claim from an authenticated key.
// It never creates/updates an identity binding. Both text and total cardinality are bounded.
func (s *Store) RecordIdentityClaim(ctx context.Context, fp, id, name, ip string) error {
	if !ValidateFingerprint(fp) || !ValidClientID(id) {
		return ErrIdentityMismatch
	}
	name = truncateRunes(name, 255)
	ip = truncateRunes(ip, 64)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		// Repeated reconnects update at most once per minute.
		now := time.Now().UTC()
		_, err := tx.ExecContext(ctx, `INSERT INTO identity_claims(fingerprint,client_id,reported_name,remote_ip,last_seen_at) VALUES(?,?,?,?,?) ON CONFLICT(fingerprint,client_id) DO UPDATE SET reported_name=excluded.reported_name,remote_ip=excluded.remote_ip,last_seen_at=excluded.last_seen_at WHERE identity_claims.last_seen_at<?`, fp, id, name, ip, dbtime(now), dbtime(now.Add(-time.Minute)))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM identity_claims WHERE rowid IN (SELECT rowid FROM identity_claims ORDER BY last_seen_at DESC,rowid DESC LIMIT -1 OFFSET 1024)`)
		return err
	})
}

func truncateRunes(v string, n int) string {
	r := []rune(v)
	if len(r) > n {
		return string(r[:n])
	}
	return v
}

// BindIdentity requires an explicit administrator decision and generation CAS.
// expected=0 creates a new identity; replacing/reactivating requires its current generation.
func (s *Store) BindIdentity(ctx context.Context, id, fp string, expected int64, a AdminAction) (IdentityBinding, error) {
	if !ValidClientID(id) || !ValidateFingerprint(fp) || expected < 0 {
		return IdentityBinding{}, ErrIdentityMismatch
	}
	var result IdentityBinding
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var generation int64
		var created int64
		err := tx.QueryRowContext(ctx, `SELECT generation,created_at FROM identity_bindings WHERE client_id=?`, id).Scan(&generation, &created)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if generation != expected || generation == int64(^uint64(0)>>1) {
			return ErrIdentityConflict
		}
		now := time.Now().UTC()
		if generation == 0 {
			created = dbtime(now)
			_, err = tx.ExecContext(ctx, `INSERT INTO identity_bindings(client_id,fingerprint,generation,revoked,created_at,updated_at) VALUES(?,?,1,0,?,?)`, id, fp, dbtime(now), dbtime(now))
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE identity_bindings SET fingerprint=?,generation=generation+1,revoked=0,updated_at=? WHERE client_id=? AND generation=?`, fp, dbtime(now), id, expected)
		}
		if err != nil {
			return err
		}
		if err = restoreDeviceKeyTx(ctx, tx, id, fp); err != nil {
			return err
		}
		a.Action = "bind_identity"
		a.TargetType = "identity"
		a.TargetID = id
		a.Result = "success"
		if err = insertAdmin(ctx, tx, a); err != nil {
			return err
		}
		result = IdentityBinding{ClientID: id, Fingerprint: fp, Generation: generation + 1, CreatedAt: scantime(created), UpdatedAt: now}
		return nil
	})
	return result, err
}

func (s *Store) RevokeIdentity(ctx context.Context, id string, expected int64, a AdminAction) error {
	if !ValidClientID(id) || expected <= 0 {
		return ErrIdentityConflict
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE identity_bindings SET revoked=1,generation=generation+1,updated_at=? WHERE client_id=? AND generation=? AND generation<9223372036854775807`, dbtime(time.Now()), id, expected)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrIdentityConflict
		}
		a.Action = "revoke_identity"
		a.TargetType = "identity"
		a.TargetID = id
		a.Result = "success"
		return insertAdmin(ctx, tx, a)
	})
}

func (s *Store) ListIdentities(ctx context.Context, fp string, limit int) ([]IdentityBinding, error) {
	q := `SELECT client_id,fingerprint,generation,revoked,created_at,updated_at FROM identity_bindings WHERE (?='' OR fingerprint=?) ORDER BY client_id LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, fp, fp, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IdentityBinding{}
	for rows.Next() {
		var b IdentityBinding
		var c, u int64
		if err = rows.Scan(&b.ClientID, &b.Fingerprint, &b.Generation, &b.Revoked, &c, &u); err != nil {
			return nil, err
		}
		b.CreatedAt = scantime(c)
		b.UpdatedAt = scantime(u)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) ListIdentityClaims(ctx context.Context, fp string, limit int) ([]IdentityClaim, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint,client_id,reported_name,remote_ip,last_seen_at FROM identity_claims WHERE (?='' OR fingerprint=?) ORDER BY last_seen_at DESC,fingerprint,client_id LIMIT ?`, fp, fp, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IdentityClaim{}
	for rows.Next() {
		var c IdentityClaim
		var at int64
		if err = rows.Scan(&c.Fingerprint, &c.ClientID, &c.ReportedName, &c.RemoteIP, &at); err != nil {
			return nil, err
		}
		c.LastSeenAt = scantime(at)
		out = append(out, c)
	}
	return out, rows.Err()
}
