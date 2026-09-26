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

type AccountDevice struct {
	Username    string    `json:"username"`
	ClientID    string    `json:"client_id"`
	Fingerprint string    `json:"fingerprint"`
	Generation  int64     `json:"generation"`
	Name        string    `json:"name"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LEFT JOIN is intentional: an orphaned/corrupt account must still own its key
// and deny admission, never permit fallback to the legacy authorized_keys file.
const accountDeviceJoins = ` FROM account_devices d LEFT JOIN accounts a ON a.username=d.username LEFT JOIN identity_bindings i ON i.client_id=d.client_id `
const accountDeviceActive = `COALESCE(a.enabled=1 AND a.generation=d.generation AND i.fingerprint=d.fingerprint AND i.revoked=0 AND NOT EXISTS(SELECT 1 FROM blacklist b WHERE b.fingerprint=d.fingerprint AND (b.expires_at IS NULL OR b.expires_at>?)),0)`

func (s *Store) AccountFingerprints(ctx context.Context, username string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint FROM account_devices WHERE username=? ORDER BY fingerprint LIMIT 50`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		out = append(out, fp)
	}
	return out, rows.Err()
}

// AccountDeviceForKey returns permanent ownership, including inactive devices.
// Administrators may use it to restore exactly this ID/key through an explicit
// generation-CAS identity change. It is not a business authorization decision.
func (s *Store) AccountDeviceForKey(ctx context.Context, fp string) (AccountDevice, error) {
	if s.commitUncertain.Load() {
		return AccountDevice{}, ErrCommitOutcomeUnknown
	}
	var d AccountDevice
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT d.username,d.client_id,d.fingerprint,d.generation,d.name,d.created_at,d.updated_at,`+accountDeviceActive+accountDeviceJoins+`WHERE d.fingerprint=?`, dbtime(time.Now()), fp).Scan(&d.Username, &d.ClientID, &d.Fingerprint, &d.Generation, &d.Name, &created, &updated, &d.Active)
	if err != nil {
		return AccountDevice{}, err
	}
	d.CreatedAt, d.UpdatedAt = scantime(created), scantime(updated)
	return d, nil
}

func (s *Store) AccountKeyStatus(ctx context.Context, fp string) (owned, allowed bool, err error) {
	if s.commitUncertain.Load() {
		return false, false, ErrCommitOutcomeUnknown
	}
	err = s.db.QueryRowContext(ctx, `SELECT `+accountDeviceActive+accountDeviceJoins+`WHERE d.fingerprint=?`, dbtime(time.Now()), fp).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, allowed, err
}

func (s *Store) ValidateAccountDevice(ctx context.Context, fp, clientID string) error {
	if s.commitUncertain.Load() {
		return ErrCommitOutcomeUnknown
	}
	var registeredID string
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT d.client_id,`+accountDeviceActive+accountDeviceJoins+`WHERE d.fingerprint=?`, dbtime(time.Now()), fp).Scan(&registeredID, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !active || registeredID != clientID {
		return ErrAccountAuth
	}
	return nil
}

func (s *Store) ListAccountDevices(ctx context.Context, username string) ([]AccountDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.username,d.client_id,d.fingerprint,d.generation,d.name,d.created_at,d.updated_at,`+accountDeviceActive+accountDeviceJoins+`WHERE d.username=? ORDER BY d.client_id LIMIT 50`, dbtime(time.Now()), username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountDevice{}
	for rows.Next() {
		var d AccountDevice
		var created, updated int64
		if err := rows.Scan(&d.Username, &d.ClientID, &d.Fingerprint, &d.Generation, &d.Name, &created, &updated, &d.Active); err != nil {
			return nil, err
		}
		d.CreatedAt, d.UpdatedAt = scantime(created), scantime(updated)
		out = append(out, d)
	}
	return out, rows.Err()
}

// EnrollAccountDevice is called only after password authentication and proof of
// possession of the submitted device key. No authorized_keys mutation is needed:
// this transaction is the durable authority for these keys and their identity.
func (s *Store) EnrollAccountDevice(ctx context.Context, username string, generation int64, clientID, fp, name string, action AdminAction) error {
	if !ValidAccountUsername(username) || generation < 1 || !ValidClientID(clientID) || !ValidateFingerprint(fp) || len(name) > 1020 || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ErrAccountInvalid
	}
	if err := s.CheckAdmission(ctx); err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var enabled bool
		var currentGeneration int64
		var maxDevices int
		err := tx.QueryRowContext(ctx, `SELECT enabled,generation,max_devices FROM accounts WHERE username=?`, username).Scan(&enabled, &currentGeneration, &maxDevices)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAccountAuth
		}
		if err != nil {
			return err
		}
		if !enabled || currentGeneration != generation {
			return ErrAccountAuth
		}
		var blocked bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blacklist WHERE fingerprint=? AND (expires_at IS NULL OR expires_at>?))`, fp, dbtime(time.Now())).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return ErrAccountAuth
		}
		rows, err := tx.QueryContext(ctx, `SELECT username,client_id,fingerprint FROM account_devices WHERE client_id=? OR fingerprint=?`, clientID, fp)
		if err != nil {
			return err
		}
		existing := false
		for rows.Next() {
			var owner, id, key string
			if err = rows.Scan(&owner, &id, &key); err != nil {
				rows.Close()
				return err
			}
			if owner != username || id != clientID || key != fp {
				rows.Close()
				return ErrAccountConflict
			}
			existing = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !existing {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM account_devices WHERE username=?`, username).Scan(&count); err != nil {
				return err
			}
			if count >= maxDevices {
				return ErrAccountLimit
			}
		}
		bound, err := registrationIdentityTx(ctx, tx, clientID, fp)
		if err != nil {
			return err
		}
		now := dbtime(time.Now())
		if _, err = tx.ExecContext(ctx, `INSERT INTO clients(fingerprint,first_seen_at,last_seen_at,computer_name,last_reported_name) VALUES(?,?,?,?,?) ON CONFLICT(fingerprint) DO NOTHING`, fp, now, now, name, name); err != nil {
			return err
		}
		if !bound {
			if _, err = tx.ExecContext(ctx, `INSERT INTO identity_bindings(client_id,fingerprint,generation,revoked,created_at,updated_at) VALUES(?,?,1,0,?,?)`, clientID, fp, now, now); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_devices(client_id,fingerprint,username,generation,name,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(client_id) DO UPDATE SET generation=excluded.generation,name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE account_devices.name END,updated_at=excluded.updated_at`, clientID, fp, username, generation, name, now, now); err != nil {
			return err
		}
		action.Action, action.TargetType, action.TargetID, action.Result = "enroll_account_device", "client", fp, "success"
		return insertAdmin(ctx, tx, action)
	})
}
