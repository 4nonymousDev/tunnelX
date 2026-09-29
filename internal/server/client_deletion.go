package server

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"tunnelx/internal/adminapi"
	"tunnelx/internal/store"
)

func (b *adminBackend) DeleteClient(ctx context.Context, fp, reason string) error {
	s := (*Server)(b)
	err := s.policy.PersistChange(fp, func() error {
		s.clientRecordsMu.Lock()
		defer s.clientRecordsMu.Unlock()
		// Admission is gated before checking sessions, including pre-hello sessions.
		for _, current := range s.sessions.Snapshot() {
			if current.Fingerprint == fp {
				return &adminapi.BackendError{Code: "client_online", Message: "设备仍有在线会话，请先断开客户端或在详情中拉黑后再删除"}
			}
		}
		if _, err := s.store.GetClient(ctx, fp); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return adminapi.ErrNotFound
			}
			return err
		}
		op, err := s.store.BeginAdminOperation(ctx, s.action(ctx, fp, "delete_client", reason, "pending", ""))
		if err != nil {
			return err
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		applyErr := s.auth.Remove(fp)
		if applyErr == nil {
			applyErr = s.store.CompleteClientDeletion(finishCtx, op, fp)
		}
		if applyErr == nil {
			return nil
		}
		if errors.Is(applyErr, store.ErrCommitOutcomeUnknown) {
			s.store.MarkAdminOperationUncertain(op)
			return applyErr
		}
		if err := s.store.CompleteAdminOperation(finishCtx, op, "needs_reconcile", applyErr.Error()); err != nil {
			s.store.MarkAdminOperationUncertain(op)
			if errors.Is(err, store.ErrCommitOutcomeUnknown) {
				return err
			}
		}
		return &adminapi.PendingOperationError{ID: op}
	}, nil)
	s.notifyAudit(0)
	if err == nil && s.adminAPI != nil {
		s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
	}
	return err
}

// Reconciliation only observes current state; it never replays a deletion.
func (b *adminBackend) reconcileClientDeletion(ctx context.Context, op store.AdminOperation, reason string) error {
	s := (*Server)(b)
	err := s.policy.PersistChange(op.TargetID, func() error {
		authorized, healthy := s.auth.FingerprintStatus(op.TargetID)
		if !healthy {
			return adminapi.ErrConflict
		}
		_, err := s.store.GetClient(ctx, op.TargetID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		missing := errors.Is(err, sql.ErrNoRows)
		_, allowed, err := s.store.DeviceKeyStatus(ctx, op.TargetID)
		if err != nil {
			return err
		}
		bindings, err := s.store.ListIdentities(ctx, op.TargetID, 1000)
		if err != nil {
			return err
		}
		active := false
		for _, binding := range bindings {
			active = active || !binding.Revoked
		}
		state := "failed"
		if missing && !authorized && !allowed && !active {
			state = "applied"
		}
		return s.store.ReconcileAdminOperation(ctx, op.ID, state, "reconciled from current state: "+reason, s.action(ctx, op.ID, "reconcile_operation", reason, "success", ""))
	}, nil)
	if err == nil {
		s.notifyAudit(0)
	}
	return err
}

// A disconnected session must not recreate an asset or claim after deletion.
// Database I/O stays outside the policy gate; unrelated session writes may run
// concurrently. Deletion waits for earlier writes before removing the record.
func (s *Server) persistClientSession(sid string, write func() error) error {
	s.clientRecordsMu.RLock()
	defer s.clientRecordsMu.RUnlock()
	if _, live := s.sessions.Get(sid); !live {
		return errors.New("client session ended")
	}
	return write()
}
