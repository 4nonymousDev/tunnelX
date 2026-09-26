package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"tunnelx/internal/accountauth"
)

// GetAccount returns current authorization state for session validation. A
// session must compare both IsAdmin/Enabled and its saved credential generation.
func (s *Store) GetAccount(ctx context.Context, username string) (Account, error) {
	if s.commitUncertain.Load() {
		return Account{}, ErrCommitOutcomeUnknown
	}
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE username=?`, username))
}

// ProvisionAdminAccount is a local operator recovery operation. It explicitly
// creates or promotes an account, enables it, and replaces its password. Existing
// device ownership, quotas and creation time are preserved, but all prior
// credentials are invalidated through a new generation.
func (s *Store) ProvisionAdminAccount(ctx context.Context, username, password string, action AdminAction) (Account, error) {
	if !ValidAccountUsername(username) || !accountauth.ValidPassword(password) {
		return Account{}, ErrAccountInvalid
	}
	if err := s.CheckAdmission(ctx); err != nil {
		return Account{}, err
	}
	hash, err := accountauth.Hash(password)
	if err != nil {
		return Account{}, err
	}
	var result Account
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		a, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE username=?`, username))
		now := time.Now().UTC()
		if errors.Is(err, sql.ErrNoRows) {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&count); err != nil {
				return err
			}
			if count >= MaxAccounts {
				return ErrAccountLimit
			}
			a = Account{Username: username, Enabled: true, IsAdmin: true, Generation: 1, MaxDevices: DefaultMaxAccountDevices, CreatedAt: now, UpdatedAt: now}
			if _, err = tx.ExecContext(ctx, `INSERT INTO accounts(username,password_hash,enabled,is_admin,generation,max_devices,created_at,updated_at) VALUES(?,?,1,1,1,?,?,?)`, username, hash, a.MaxDevices, dbtime(now), dbtime(now)); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if a.Generation == math.MaxInt64 {
				return ErrAccountConflict
			}
			a.Enabled, a.IsAdmin = true, true
			a.Generation++
			a.UpdatedAt = now
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET password_hash=?,enabled=1,is_admin=1,generation=?,updated_at=? WHERE username=?`, hash, a.Generation, dbtime(now), username); err != nil {
				return err
			}
		}
		action.Action, action.TargetType, action.TargetID, action.Result = "provision_admin_account", "account", username, "success"
		if err := insertAdmin(ctx, tx, action); err != nil {
			return err
		}
		result = a
		return nil
	})
	return result, err
}
