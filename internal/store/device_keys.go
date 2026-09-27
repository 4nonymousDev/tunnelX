package store

import (
	"context"
	"database/sql"
	"errors"
)

// Account enrollment stored fingerprints rather than public-key text. Keep those
// authorizations independently of administrators, including denied keys so an
// old authorized_keys entry cannot accidentally reactivate a disabled device.
var schemaV7 = []string{
	`CREATE TABLE device_keys(fingerprint TEXT PRIMARY KEY,client_id TEXT NOT NULL UNIQUE,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)))`,
	`INSERT INTO device_keys(fingerprint,client_id,enabled)
	 SELECT d.fingerprint,d.client_id,COALESCE(a.enabled=1 AND a.generation=d.generation AND i.fingerprint=d.fingerprint AND i.revoked=0,0)
	 FROM account_devices d LEFT JOIN accounts a ON a.username=d.username LEFT JOIN identity_bindings i ON i.client_id=d.client_id`,
	`DROP TABLE account_devices`,
	`DELETE FROM accounts WHERE is_admin=0`,
}

func (s *Store) DeviceKeyStatus(ctx context.Context, fp string) (known, allowed bool, err error) {
	if s.commitUncertain.Load() {
		return false, false, ErrCommitOutcomeUnknown
	}
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(d.enabled=1 AND i.fingerprint=d.fingerprint AND i.revoked=0,0) FROM device_keys d LEFT JOIN identity_bindings i ON i.client_id=d.client_id WHERE d.fingerprint=?`, fp).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, allowed, err
}

// DeviceKeyIdentity is an ownership lookup, never an authorization decision.
func (s *Store) DeviceKeyIdentity(ctx context.Context, fp string) (string, error) {
	if s.commitUncertain.Load() {
		return "", ErrCommitOutcomeUnknown
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT client_id FROM device_keys WHERE fingerprint=?`, fp).Scan(&id)
	return id, err
}

// Explicit administrator registration/recovery may restore exactly the original
// ID/key. This runs in the same transaction as the identity change and audit.
func restoreDeviceKeyTx(ctx context.Context, tx *sql.Tx, id, fp string) error {
	var originalID string
	err := tx.QueryRowContext(ctx, `SELECT client_id FROM device_keys WHERE fingerprint=?`, fp).Scan(&originalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if originalID != id {
		return ErrIdentityConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE device_keys SET enabled=1 WHERE fingerprint=?`, fp)
	return err
}
