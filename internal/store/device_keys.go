package store

import (
	"context"
	"database/sql"
	"errors"
)

// Device authorizations are independent of administrators. Denied keys must not
// fall back to an authorized_keys entry.
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
