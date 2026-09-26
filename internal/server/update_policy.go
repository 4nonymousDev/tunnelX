package server

import (
	"context"
	"errors"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/policy"
	"tunnelx/internal/proto"
	"tunnelx/internal/store"
	"tunnelx/internal/updatepolicy"
)

func (s *Server) clientUpdateStatus(version string) *updatepolicy.Status {
	if p := s.clientUpdates.Load(); p != nil {
		return p.Status(version, time.Now())
	}
	return nil
}

func (s *Server) clientVersionBlocked(version string) bool {
	v := s.clientUpdateStatus(version)
	return v != nil && v.Blocked
}

func (s *Server) rejectClientVersion(conn *ssh.ServerConn, pc *proto.Conn, version string) bool {
	v := s.clientUpdateStatus(version)
	if v == nil || !v.Blocked {
		return false
	}
	_ = s.sendControl(conn, pc, proto.Error{V: proto.Version, Type: proto.TypeError, Code: proto.CodeUpdateRequired, Msg: v.Reason(), UpdatePolicy: v})
	return true
}

func (b *adminBackend) GetClientUpdatePolicy(ctx context.Context) (updatepolicy.Policy, error) {
	return (*Server)(b).store.GetClientUpdatePolicy(ctx)
}

func (b *adminBackend) SetClientUpdatePolicy(ctx context.Context, r adminapi.UpdatePolicyRequest) (updatepolicy.Policy, error) {
	s := (*Server)(b)
	var p updatepolicy.Policy
	err := s.policy.PersistChange("", func() error {
		var err error
		p, err = s.store.SetClientUpdatePolicy(ctx, updatepolicy.Policy{MinimumVersion: r.MinimumVersion, EnforceAfter: r.EnforceAfter, Message: r.Message}, r.ExpectedGeneration, s.action(ctx, "global", "set_client_update_policy", r.Reason, "success", ""))
		return err
	}, func(_ *policy.Snapshot) { s.clientUpdates.Store(&p) })
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		// The durable policy may already have changed. No live session may
		// continue under a potentially stale minimum version.
		for _, sess := range s.sessions.Snapshot() {
			s.sessions.Disconnect(sess.ID, "authorization_commit_uncertain")
		}
	}
	if err == nil && s.adminAPI != nil {
		s.adminAPI.Publish("update-policy.changed", nil)
		s.notifyAudit(0)
	}
	return p, accountBackendError(err)
}
