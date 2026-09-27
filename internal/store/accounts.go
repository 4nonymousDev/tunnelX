package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"tunnelx/internal/accountauth"
)

var (
	ErrAccountAuth     = errors.New("account authentication failed")
	ErrAccountInvalid  = errors.New("invalid account parameters")
	ErrAccountConflict = errors.New("account changed; reload before updating")
	ErrAccountLimit    = errors.New("account capacity reached")
	ErrAccountBusy     = accountauth.ErrBusy
)

const MaxAccounts = 1000
const DefaultMaxAccountDevices = 10

var schemaV4 = []string{
	`CREATE TABLE accounts(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),generation INTEGER NOT NULL CHECK(generation>0),max_devices INTEGER NOT NULL CHECK(max_devices BETWEEN 1 AND 50),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`,
	`CREATE TABLE account_devices(client_id TEXT PRIMARY KEY,fingerprint TEXT NOT NULL UNIQUE,username TEXT NOT NULL REFERENCES accounts(username),generation INTEGER NOT NULL CHECK(generation>0),name TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`,
	`CREATE INDEX account_device_username ON account_devices(username)`,
}

// Account intentionally has no password or hash field, including in JSON.
type Account struct {
	Username   string    `json:"username"`
	Enabled    bool      `json:"enabled"`
	IsAdmin    bool      `json:"is_admin"`
	Generation int64     `json:"generation"`
	MaxDevices int       `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func ValidAccountUsername(username string) bool {
	if len(username) < 3 || len(username) > 64 {
		return false
	}
	for i := range len(username) {
		c := username[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		if i == 0 || (c != '_' && c != '.' && c != '-') {
			return false
		}
	}
	return true
}

type accountScanner interface{ Scan(...any) error }

func scanAccount(row accountScanner) (Account, error) {
	var a Account
	var created, updated int64
	err := row.Scan(&a.Username, &a.Enabled, &a.IsAdmin, &a.Generation, &a.MaxDevices, &created, &updated)
	a.CreatedAt, a.UpdatedAt = scantime(created), scantime(updated)
	return a, err
}

const accountColumns = `username,enabled,is_admin,generation,max_devices,created_at,updated_at`

func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY username LIMIT ?`, MaxAccounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateAccount(ctx context.Context, username, password string, maxDevices int, action AdminAction) (Account, error) {
	return s.CreateAccountWithRole(ctx, username, password, maxDevices, false, action)
}

func (s *Store) CreateAccountWithRole(ctx context.Context, username, password string, maxDevices int, isAdmin bool, action AdminAction) (Account, error) {
	if !ValidAccountUsername(username) || !accountauth.ValidPassword(password) || maxDevices < 1 || maxDevices > 50 {
		return Account{}, ErrAccountInvalid
	}
	if err := s.CheckAdmission(ctx); err != nil {
		return Account{}, err
	}
	// The expensive derivation must never hold a SQLite transaction/connection.
	hash, err := accountauth.Hash(password)
	if err != nil {
		return Account{}, err
	}
	var result Account
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var count, exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),count(CASE WHEN username=? THEN 1 END) FROM accounts`, username).Scan(&count, &exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrAccountConflict
		}
		if count >= MaxAccounts {
			return ErrAccountLimit
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(username,password_hash,enabled,is_admin,generation,max_devices,created_at,updated_at) VALUES(?,?,1,?,1,?,?,?)`, username, hash, isAdmin, maxDevices, dbtime(now), dbtime(now)); err != nil {
			return err
		}
		action.Action, action.TargetType, action.TargetID, action.Result = "create_account", "account", username, "success"
		if err := insertAdmin(ctx, tx, action); err != nil {
			return err
		}
		result = Account{Username: username, Enabled: true, IsAdmin: isAdmin, Generation: 1, MaxDevices: maxDevices, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	return result, err
}

// UpdateAccount increments the credential generation whenever the password or
// enabled state changes. Only administrator browser sessions are affected.
func (s *Store) UpdateAccount(ctx context.Context, username, password string, enabled *bool, expectedGeneration int64, action AdminAction) (Account, error) {
	return s.UpdateAccountWithRole(ctx, username, password, enabled, nil, expectedGeneration, action)
}

// UpdateAccountWithRole also invalidates credentials when an administrator role
// changes. The last enabled administrator cannot be disabled or demoted.
func (s *Store) UpdateAccountWithRole(ctx context.Context, username, password string, enabled, isAdmin *bool, expectedGeneration int64, action AdminAction) (Account, error) {
	if !ValidAccountUsername(username) || expectedGeneration < 1 || (password != "" && !accountauth.ValidPassword(password)) {
		return Account{}, ErrAccountInvalid
	}
	var hash string
	var err error
	if password != "" {
		hash, err = accountauth.Hash(password)
		if err != nil {
			return Account{}, err
		}
	}
	var result Account
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		a, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE username=?`, username))
		if err != nil {
			return err
		}
		if a.Generation != expectedGeneration || a.Generation == math.MaxInt64 {
			return ErrAccountConflict
		}
		changed := hash != "" || (enabled != nil && a.Enabled != *enabled) || (isAdmin != nil && a.IsAdmin != *isAdmin)
		if changed {
			wasEnabledAdmin := a.Enabled && a.IsAdmin
			if enabled != nil {
				a.Enabled = *enabled
			}
			if isAdmin != nil {
				a.IsAdmin = *isAdmin
			}
			if wasEnabledAdmin && (!a.Enabled || !a.IsAdmin) {
				var others int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE enabled=1 AND is_admin=1 AND username<>?`, username).Scan(&others); err != nil {
					return err
				}
				if others == 0 {
					return ErrAccountConflict
				}
			}
			a.Generation++
			a.UpdatedAt = time.Now().UTC()
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET password_hash=CASE WHEN ?<>'' THEN ? ELSE password_hash END,enabled=?,is_admin=?,generation=?,updated_at=? WHERE username=?`, hash, hash, a.Enabled, a.IsAdmin, a.Generation, dbtime(a.UpdatedAt), username); err != nil {
				return err
			}
		}
		action.Action, action.TargetType, action.TargetID, action.Result = "update_account", "account", username, "success"
		if err = insertAdmin(ctx, tx, action); err != nil {
			return err
		}
		result = a
		return nil
	})
	return result, err
}

// AuthenticateAccount uses a dummy derivation for missing accounts. Its returned
// generation is checked again on each administrator browser request.
func (s *Store) AuthenticateAccount(ctx context.Context, username, password string) (Account, error) {
	if s.commitUncertain.Load() {
		return Account{}, ErrCommitOutcomeUnknown
	}
	var a Account
	var hash string
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT `+accountColumns+`,password_hash FROM accounts WHERE username=?`, username).Scan(&a.Username, &a.Enabled, &a.IsAdmin, &a.Generation, &a.MaxDevices, &created, &updated, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Account{}, err
	}
	match, verifyErr := accountauth.Verify(hash, password)
	if verifyErr != nil {
		return Account{}, verifyErr
	}
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	if !match || !a.Enabled || !ValidAccountUsername(username) || err != nil {
		return Account{}, ErrAccountAuth
	}
	a.CreatedAt, a.UpdatedAt = scantime(created), scantime(updated)
	return a, nil
}
