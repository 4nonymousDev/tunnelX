package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type AdminOperation struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	TargetID  string    `json:"target_id"`
	State     string    `json:"state"`
	AuditID   int64     `json:"audit_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) BeginAdminOperation(ctx context.Context, a AdminAction) (string, error) {
	if err := s.CheckAdmission(ctx); err != nil {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		return beginAdminOperationTx(ctx, tx, id, a.TargetID, a)
	})
	return id, err
}

// operationTarget can retain structured recovery data while the audit target
// remains a fingerprint usable by the existing client history filters.
func beginAdminOperationTx(ctx context.Context, tx *sql.Tx, id, operationTarget string, a AdminAction) error {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_operations WHERE state IN ('pending','needs_reconcile')`).Scan(&pending); err != nil {
		return err
	}
	if pending >= 64 {
		return errors.New("too many unresolved administrative operations")
	}
	a.Result = "pending"
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	if err := insertAdmin(ctx, tx, a); err != nil {
		return err
	}
	var auditID int64
	if err := tx.QueryRowContext(ctx, `SELECT last_insert_rowid()`).Scan(&auditID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_operations(id,action,target_id,state,audit_id,created_at,updated_at) VALUES(?,?,?,'pending',?,?,?)`, id, a.Action, operationTarget, auditID, dbtime(a.CreatedAt), dbtime(a.CreatedAt))
	return err
}

func (s *Store) CompleteAdminOperation(ctx context.Context, id, state, errText string) error {
	return s.completeAdminOperation(ctx, id, state, errText, nil)
}

func (s *Store) ReconcileAdminOperation(ctx context.Context, id, state, errText string, a AdminAction) error {
	a.Action = "reconcile_operation"
	a.TargetType = "operation"
	a.TargetID = id
	a.Result = "success"
	return s.completeAdminOperation(ctx, id, state, errText, &a)
}

func (s *Store) completeAdminOperation(ctx context.Context, id, state, errText string, audit *AdminAction) error {
	if state != "applied" && state != "failed" && state != "needs_reconcile" {
		return errors.New("invalid operation state")
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var auditID int64
		var previous string
		if err := tx.QueryRowContext(ctx, `SELECT audit_id,state FROM admin_operations WHERE id=?`, id).Scan(&auditID, &previous); err != nil {
			return err
		}
		if previous == "applied" || previous == "failed" {
			if previous == state {
				return nil
			}
			return ErrIdentityConflict
		}
		err := setAdminOperationOutcomeTx(ctx, tx, id, auditID, state, errText)
		if err == nil && audit != nil {
			err = insertAdmin(ctx, tx, *audit)
		}
		return err
	})
	if err == nil {
		s.uncertainMu.Lock()
		delete(s.uncertainOps, id)
		s.uncertainMu.Unlock()
	}
	return err
}

func setAdminOperationOutcomeTx(ctx context.Context, tx *sql.Tx, id string, auditID int64, state, errText string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE admin_operations SET state=?,updated_at=? WHERE id=?`, state, dbtime(time.Now()), id); err != nil {
		return err
	}
	result := state
	if state == "applied" {
		result = "success"
	}
	_, err := tx.ExecContext(ctx, `UPDATE admin_actions SET result=?,error=? WHERE id=?`, result, truncateRunes(errText, 500), auditID)
	return err
}

// MarkAdminOperationUncertain lets the running process reconcile a completed
// side effect whose outcome write failed. The durable pending intent remains
// authoritative across restarts. At most 64 such intents can exist.
func (s *Store) MarkAdminOperationUncertain(id string) {
	s.uncertainMu.Lock()
	defer s.uncertainMu.Unlock()
	if len(s.uncertainOps) < 64 {
		s.uncertainOps[id] = struct{}{}
	}
}

func (s *Store) ListAdminOperations(ctx context.Context, limit int) ([]AdminOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,action,target_id,state,audit_id,created_at,updated_at FROM admin_operations ORDER BY CASE WHEN state IN ('pending','needs_reconcile') THEN 0 ELSE 1 END,created_at DESC LIMIT ?`, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminOperation{}
	for rows.Next() {
		var o AdminOperation
		var c, u int64
		if err = rows.Scan(&o.ID, &o.Action, &o.TargetID, &o.State, &o.AuditID, &c, &u); err != nil {
			return nil, err
		}
		o.CreatedAt = scantime(c)
		o.UpdatedAt = scantime(u)
		s.uncertainMu.Lock()
		_, uncertain := s.uncertainOps[o.ID]
		s.uncertainMu.Unlock()
		if uncertain && o.State == "pending" {
			o.State = "needs_reconcile"
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) UpdateClientNoteAudited(ctx context.Context, fp, note string, a AdminAction) error {
	missing := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE clients SET note=? WHERE fingerprint=?`, note, fp)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			missing = true
			a.Result = "failed"
			a.Error = "client not found"
		} else {
			a.Result = "success"
		}
		if err = insertAdmin(ctx, tx, a); err != nil {
			return err
		}
		// The missing-target attempt is still committed and reported after this transaction.
		return nil
	})
	if err == nil && missing {
		return sql.ErrNoRows
	}
	return err
}
