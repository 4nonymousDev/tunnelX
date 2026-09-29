package store

import (
	"context"
	"fmt"
)

// initialize creates the current schema without modifying existing records.
// Historical database layouts are not supported; there are no version migrations.
func (s *Store) initialize(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range schema {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("initialize database: %w", err)
		}
	}
	return tx.Commit()
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS clients(fingerprint TEXT PRIMARY KEY,note TEXT NOT NULL DEFAULT '',first_seen_at INTEGER NOT NULL,last_seen_at INTEGER NOT NULL,last_ip TEXT NOT NULL DEFAULT '',last_client_id TEXT NOT NULL DEFAULT '',last_reported_name TEXT NOT NULL DEFAULT '',last_role TEXT NOT NULL DEFAULT '',last_version TEXT NOT NULL DEFAULT '',username TEXT NOT NULL DEFAULT '',email TEXT NOT NULL DEFAULT '',computer_name TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS blacklist(fingerprint TEXT PRIMARY KEY,reason TEXT NOT NULL,operator TEXT NOT NULL,created_at INTEGER NOT NULL,expires_at INTEGER NULL)`,
	`CREATE TABLE IF NOT EXISTS connection_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,session_id TEXT NULL,fingerprint TEXT NOT NULL DEFAULT '',client_id TEXT NOT NULL DEFAULT '',remote_ip TEXT NOT NULL DEFAULT '',authenticated_at INTEGER NOT NULL,hello_at INTEGER NULL,disconnected_at INTEGER NULL,result TEXT NOT NULL,disconnect_reason TEXT NOT NULL DEFAULT '',reported_name TEXT NOT NULL DEFAULT '',role TEXT NOT NULL DEFAULT '',version TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS access_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,visitor_session_id TEXT NOT NULL DEFAULT '',visitor_fingerprint TEXT NOT NULL DEFAULT '',visitor_client_id TEXT NOT NULL DEFAULT '',target_session_id TEXT NOT NULL DEFAULT '',target_fingerprint TEXT NOT NULL DEFAULT '',target_client_id TEXT NOT NULL DEFAULT '',target_tunnel_id TEXT NOT NULL DEFAULT '',target_tunnel_name TEXT NOT NULL DEFAULT '',target_remote_port INTEGER NOT NULL DEFAULT 0,started_at INTEGER NOT NULL,ended_at INTEGER NULL,result TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',bytes_sent INTEGER NOT NULL DEFAULT 0,bytes_received INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS admin_actions(id INTEGER PRIMARY KEY AUTOINCREMENT,action TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,reason TEXT NOT NULL,result TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',operator TEXT NOT NULL,transport_peer TEXT NOT NULL,source_ip TEXT NULL,created_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS connection_audit_time ON connection_audit(authenticated_at DESC,id DESC)`, `CREATE INDEX IF NOT EXISTS connection_audit_fingerprint_time ON connection_audit(fingerprint,authenticated_at DESC)`, `CREATE INDEX IF NOT EXISTS connection_audit_session ON connection_audit(session_id)`, `CREATE INDEX IF NOT EXISTS connection_audit_client_time ON connection_audit(client_id,authenticated_at DESC)`,
	`CREATE INDEX IF NOT EXISTS access_audit_time ON access_audit(started_at DESC,id DESC)`, `CREATE INDEX IF NOT EXISTS access_audit_target_fingerprint_time ON access_audit(target_fingerprint,started_at DESC)`, `CREATE INDEX IF NOT EXISTS access_audit_visitor_fingerprint_time ON access_audit(visitor_fingerprint,started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS admin_actions_time ON admin_actions(created_at DESC,id DESC)`, `CREATE INDEX IF NOT EXISTS blacklist_expires_at ON blacklist(expires_at)`,
	`CREATE TABLE IF NOT EXISTS identity_bindings(client_id TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,generation INTEGER NOT NULL CHECK(generation>0),revoked INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS identity_fingerprint ON identity_bindings(fingerprint)`,
	`CREATE TABLE IF NOT EXISTS identity_claims(fingerprint TEXT NOT NULL,client_id TEXT NOT NULL,reported_name TEXT NOT NULL DEFAULT '',remote_ip TEXT NOT NULL DEFAULT '',last_seen_at INTEGER NOT NULL,PRIMARY KEY(fingerprint,client_id))`,
	`CREATE INDEX IF NOT EXISTS identity_claim_time ON identity_claims(last_seen_at)`,
	`CREATE TABLE IF NOT EXISTS rejection_counts(bucket_at INTEGER NOT NULL,reason TEXT NOT NULL,count INTEGER NOT NULL CHECK(count>=0),PRIMARY KEY(bucket_at,reason))`,
	`CREATE TABLE IF NOT EXISTS admin_operations(id TEXT PRIMARY KEY,action TEXT NOT NULL,target_id TEXT NOT NULL,state TEXT NOT NULL,audit_id INTEGER NOT NULL REFERENCES admin_actions(id),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS admin_operation_state ON admin_operations(state,created_at)`,
	`CREATE TABLE IF NOT EXISTS accounts(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),generation INTEGER NOT NULL CHECK(generation>0),max_devices INTEGER NOT NULL CHECK(max_devices BETWEEN 1 AND 50),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,is_admin INTEGER NOT NULL DEFAULT 0 CHECK(is_admin IN (0,1)))`,
	`CREATE TABLE IF NOT EXISTS device_keys(fingerprint TEXT PRIMARY KEY,client_id TEXT NOT NULL UNIQUE,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)))`,
}
