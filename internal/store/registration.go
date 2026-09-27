package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type clientRegistrationTarget struct {
	ClientID    string `json:"client_id"`
	Fingerprint string `json:"fingerprint"`
}

// ParseClientRegistrationTarget reads the bounded recovery record, not a client
// claim. Reject ambiguous JSON so reconciliation checks exactly one identity.
func ParseClientRegistrationTarget(raw string) (clientID, fingerprint string, err error) {
	if len(raw) > 4096 {
		return "", "", ErrIdentityMismatch
	}
	d := json.NewDecoder(strings.NewReader(raw))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return "", "", ErrIdentityMismatch
	}
	seen := map[string]bool{}
	for d.More() {
		token, e := d.Token()
		key, ok := token.(string)
		if e != nil || !ok || seen[key] || (key != "client_id" && key != "fingerprint") {
			return "", "", ErrIdentityMismatch
		}
		seen[key] = true
		var value string
		if e = d.Decode(&value); e != nil {
			return "", "", ErrIdentityMismatch
		}
		if key == "client_id" {
			clientID = value
		} else {
			fingerprint = value
		}
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return "", "", ErrIdentityMismatch
	}
	var extra any
	if d.Decode(&extra) != io.EOF || !ValidClientID(clientID) || !ValidateFingerprint(fingerprint) {
		return "", "", ErrIdentityMismatch
	}
	return clientID, fingerprint, nil
}

func (s *Store) BeginClientRegistration(ctx context.Context, clientID, fingerprint string, a AdminAction) (string, error) {
	if !ValidClientID(clientID) || !ValidateFingerprint(fingerprint) {
		return "", ErrIdentityMismatch
	}
	if err := s.CheckAdmission(ctx); err != nil {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	target, err := json.Marshal(clientRegistrationTarget{ClientID: clientID, Fingerprint: fingerprint})
	if err != nil {
		return "", err
	}
	a.Action, a.TargetType, a.TargetID = "register_client", "client", fingerprint
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := registrationIdentityTx(ctx, tx, clientID, fingerprint); err != nil {
			return err
		}
		return beginAdminOperationTx(ctx, tx, id, string(target), a)
	})
	return id, err
}

// registrationIdentityTx permits creation or the identical active binding.
// Reassignment and reactivation remain separate explicit administrator actions.
func registrationIdentityTx(ctx context.Context, tx *sql.Tx, clientID, fingerprint string) (bool, error) {
	var originalID string
	lookupErr := tx.QueryRowContext(ctx, `SELECT client_id FROM device_keys WHERE fingerprint=?`, fingerprint).Scan(&originalID)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return false, lookupErr
	}
	if lookupErr == nil && originalID != clientID {
		return false, ErrIdentityMismatch
	}
	var current string
	var revoked bool
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,revoked FROM identity_bindings WHERE client_id=?`, clientID).Scan(&current, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if revoked {
		return true, ErrIdentityRevoked
	}
	if current != fingerprint {
		return true, ErrIdentityMismatch
	}
	return true, nil
}

func registrationOperationTx(ctx context.Context, tx *sql.Tx, id string) (clientID, fingerprint, state string, auditID int64, err error) {
	var action, target string
	err = tx.QueryRowContext(ctx, `SELECT action,target_id,state,audit_id FROM admin_operations WHERE id=?`, id).Scan(&action, &target, &state, &auditID)
	if err != nil {
		return
	}
	if action != "register_client" {
		err = ErrIdentityConflict
		return
	}
	clientID, fingerprint, err = ParseClientRegistrationTarget(target)
	return
}

// CompleteClientRegistration commits metadata, identity and successful audit
// together after the caller durably authorizes the public key. It never changes
// blacklist entries, existing notes, history or an existing binding generation.
func (s *Store) CompleteClientRegistration(ctx context.Context, operationID, clientID, fingerprint, username, email, computerName string) error {
	if !ValidClientID(clientID) || !ValidateFingerprint(fingerprint) {
		return ErrIdentityMismatch
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		id, fp, state, auditID, err := registrationOperationTx(ctx, tx, operationID)
		if err != nil {
			return err
		}
		if id != clientID || fp != fingerprint {
			return ErrIdentityMismatch
		}
		exists, err := registrationIdentityTx(ctx, tx, clientID, fingerprint)
		if err != nil {
			return err
		}
		if state == "applied" {
			if !exists {
				return ErrIdentityConflict
			}
			return nil
		}
		if state != "pending" {
			return ErrIdentityConflict
		}
		now := dbtime(time.Now())
		_, err = tx.ExecContext(ctx, `INSERT INTO clients(fingerprint,first_seen_at,last_seen_at,username,email,computer_name,last_reported_name) VALUES(?,?,?,?,?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET username=CASE WHEN excluded.username<>'' THEN excluded.username ELSE clients.username END,email=CASE WHEN excluded.email<>'' THEN excluded.email ELSE clients.email END,computer_name=CASE WHEN excluded.computer_name<>'' THEN excluded.computer_name ELSE clients.computer_name END,last_reported_name=CASE WHEN clients.last_reported_name='' THEN excluded.computer_name ELSE clients.last_reported_name END`, fingerprint, now, now, username, email, computerName, computerName)
		if err != nil {
			return err
		}
		if !exists {
			if _, err = tx.ExecContext(ctx, `INSERT INTO identity_bindings(client_id,fingerprint,generation,revoked,created_at,updated_at) VALUES(?,?,1,0,?,?)`, clientID, fingerprint, now, now); err != nil {
				return err
			}
		}
		if err = restoreDeviceKeyTx(ctx, tx, clientID, fingerprint); err != nil {
			return err
		}
		return setAdminOperationOutcomeTx(ctx, tx, operationID, auditID, "applied", "")
	})
	if err == nil {
		s.uncertainMu.Lock()
		delete(s.uncertainOps, operationID)
		s.uncertainMu.Unlock()
	}
	return err
}

// ReconcileClientRegistration only observes current authorization and binding.
// It cannot replay an import, invent a missing binding, or undo a revocation.
func (s *Store) ReconcileClientRegistration(ctx context.Context, operationID string, authorized bool, a AdminAction) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		clientID, fingerprint, previous, auditID, err := registrationOperationTx(ctx, tx, operationID)
		if err != nil {
			return err
		}
		exists, identityErr := registrationIdentityTx(ctx, tx, clientID, fingerprint)
		if identityErr != nil && !errors.Is(identityErr, ErrIdentityRevoked) && !errors.Is(identityErr, ErrIdentityMismatch) {
			return identityErr
		}
		state, outcome := "failed", "public key authorization and active identity binding do not both match"
		if authorized && exists && identityErr == nil {
			state, outcome = "applied", ""
		}
		if previous == "applied" || previous == "failed" {
			if previous == state {
				return nil
			}
			return ErrIdentityConflict
		}
		if previous != "pending" && previous != "needs_reconcile" {
			return ErrIdentityConflict
		}
		if err = setAdminOperationOutcomeTx(ctx, tx, operationID, auditID, state, outcome); err != nil {
			return err
		}
		a.Action, a.TargetType, a.TargetID, a.Result = "reconcile_operation", "operation", operationID, "success"
		return insertAdmin(ctx, tx, a)
	})
	if err == nil {
		s.uncertainMu.Lock()
		delete(s.uncertainOps, operationID)
		s.uncertainMu.Unlock()
	}
	return err
}
