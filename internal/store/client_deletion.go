package store

import (
	"context"
	"database/sql"
	"time"
)

// CompleteClientDeletion preserves revoked bindings and generations, disabled
// device authorizations, blacklist entries and historical audits.
func (s *Store) CompleteClientDeletion(ctx context.Context, operation, fp string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var auditID int64
		if err := tx.QueryRowContext(ctx, `SELECT audit_id FROM admin_operations WHERE id=? AND action='delete_client' AND target_id=? AND state='pending'`, operation, fp).Scan(&auditID); err != nil {
			return err
		}
		var overflow bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identity_bindings WHERE fingerprint=? AND revoked=0 AND generation=9223372036854775807)`, fp).Scan(&overflow); err != nil {
			return err
		}
		if overflow {
			return ErrIdentityConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identity_bindings SET revoked=1,generation=generation+1,updated_at=? WHERE fingerprint=? AND revoked=0`, dbtime(time.Now()), fp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE device_keys SET enabled=0 WHERE fingerprint=?`, fp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_claims WHERE fingerprint=?`, fp); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM clients WHERE fingerprint=?`, fp)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		return setAdminOperationOutcomeTx(ctx, tx, operation, auditID, "applied", "")
	})
}
