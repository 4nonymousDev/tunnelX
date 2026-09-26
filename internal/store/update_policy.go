package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"tunnelx/internal/updatepolicy"
)

var schemaV6 = []string{
	`CREATE TABLE client_update_policy(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`,
	`INSERT INTO client_update_policy(id,value) VALUES(1,'{"minimum_version":"","enforce_after":null,"message":"","generation":1,"updated_at":"1970-01-01T00:00:00Z"}')`,
}

func readUpdatePolicy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (updatepolicy.Policy, error) {
	var raw string
	var p updatepolicy.Policy
	if err := q.QueryRowContext(ctx, `SELECT value FROM client_update_policy WHERE id=1`).Scan(&raw); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	if p.Generation < 1 {
		return p, ErrAccountInvalid
	}
	return p, p.Validate()
}

func (s *Store) GetClientUpdatePolicy(ctx context.Context) (updatepolicy.Policy, error) {
	if s.commitUncertain.Load() {
		return updatepolicy.Policy{}, ErrCommitOutcomeUnknown
	}
	return readUpdatePolicy(ctx, s.db)
}

func (s *Store) SetClientUpdatePolicy(ctx context.Context, p updatepolicy.Policy, expected int64, action AdminAction) (updatepolicy.Policy, error) {
	if err := p.Validate(); err != nil {
		return p, err
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		old, err := readUpdatePolicy(ctx, tx)
		if err != nil {
			return err
		}
		if old.Generation != expected || expected == math.MaxInt64 {
			return ErrAccountConflict
		}
		p.Generation, p.UpdatedAt = expected+1, time.Now().UTC()
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE client_update_policy SET value=? WHERE id=1`, string(raw)); err != nil {
			return err
		}
		action.Action, action.TargetType, action.TargetID, action.Result = "set_client_update_policy", "update_policy", "global", "success"
		deadline := "disabled"
		if p.EnforceAfter != nil {
			deadline = p.EnforceAfter.UTC().Format(time.RFC3339Nano)
		}
		action.Reason = fmt.Sprintf("%s; generation=%d; minimum_version=%q; enforce_after=%s; message=%q", action.Reason, p.Generation, p.MinimumVersion, deadline, p.Message)
		return insertAdmin(ctx, tx, action)
	})
	return p, err
}
