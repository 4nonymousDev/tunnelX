package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/keygen"
	"tunnelx/internal/policy"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

// adminBackend adapts live and persistent server state to the transport-only
// adminapi contract without exposing either implementation package to HTTP.
type adminBackend Server

func (b *adminBackend) Overview(ctx context.Context) (adminapi.Overview, error) {
	s := (*Server)(b)
	var o adminapi.Overview
	if s.store.CommitUncertain() {
		o.StorageStatus = "commit_uncertain"
		return o, nil
	}
	o.StorageHealthy = true
	o.StorageStatus = "healthy"
	if err := s.store.AdmissionError(ctx); err != nil {
		o.StorageHealthy = false
		o.StorageStatus = "new_work_paused"
	} else if s.store.MaintenanceError() != "" {
		o.StorageHealthy = false
		o.StorageStatus = "maintenance_failed"
	}
	for _, x := range s.sessions.Snapshot() {
		if x.State != session.Online {
			continue
		}
		o.OnlineUsers++
		if x.Role == "importer" {
			o.Importers++
		}
		if len(x.Tunnels) > 0 {
			o.ActiveExporters++
		}
		o.ActiveTunnels += len(x.Tunnels)
	}
	n, start, e := s.store.TodayRejected(ctx, time.Now())
	o.RejectedToday = int(n)
	o.PeriodStart = start
	name, _ := time.Now().Zone()
	o.Timezone = name
	return o, e
}
func sessionDTO(x session.Session) adminapi.Session {
	t := make([]adminapi.Tunnel, len(x.Tunnels))
	for i, v := range x.Tunnels {
		t[i] = adminapi.Tunnel{ID: v.ID, Name: v.Name, RemotePort: uint32(v.RemotePort)}
	}
	return adminapi.Session{ID: x.ID, Fingerprint: x.Fingerprint, ClientID: x.ClientID, Name: x.Name, Role: x.Role, Version: x.Version, RemoteIP: x.RemoteIP, ConnectedAt: x.ConnectedAt, State: string(x.State), Tunnels: t}
}
func (b *adminBackend) ListSessions(_ context.Context, q adminapi.ListQuery) (adminapi.Page[adminapi.Session], error) {
	xs := (*Server)(b).sessions.Snapshot()
	out := make([]adminapi.Session, 0, len(xs))
	for _, x := range xs {
		if q.Fingerprint != "" && x.Fingerprint != q.Fingerprint {
			continue
		}
		if q.ClientID != "" && x.ClientID != q.ClientID {
			continue
		}
		out = append(out, sessionDTO(x))
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return adminapi.Page[adminapi.Session]{Items: out}, nil
}
func (b *adminBackend) GetSession(_ context.Context, id string) (adminapi.Session, error) {
	x, ok := (*Server)(b).sessions.Get(id)
	if !ok {
		return adminapi.Session{}, adminapi.ErrNotFound
	}
	return sessionDTO(x), nil
}
func (s *Server) action(ctx context.Context, target, kind, reason, result, errText string) store.AdminAction {
	targetType := "client"
	if kind == "disconnect" {
		targetType = "session"
	}
	meta := adminapi.RequestMetadata(ctx)
	peer := meta.RemoteAddr
	var sourceIP *string
	if host, _, err := net.SplitHostPort(peer); err == nil {
		sourceIP = &host
	}
	operator := meta.Operator
	if operator == "" {
		operator = "server"
	}
	return store.AdminAction{Action: kind, TargetType: targetType, TargetID: target, Reason: reason, Result: result, Error: errText, Operator: operator, TransportPeer: peer, SourceIP: sourceIP, CreatedAt: time.Now()}
}
func (b *adminBackend) DisconnectSession(ctx context.Context, id, reason string) error {
	s := (*Server)(b)
	op, err := s.store.BeginAdminOperation(ctx, s.action(ctx, id, "disconnect", reason, "pending", ""))
	if err != nil {
		return err
	}
	ok := s.sessions.Disconnect(id, reason)
	result := "applied"
	errText := ""
	if !ok {
		result = "failed"
		errText = "session not found"
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	e := s.store.CompleteAdminOperation(finishCtx, op, result, errText)
	if e != nil {
		s.store.MarkAdminOperationUncertain(op)
		return &adminapi.PendingOperationError{ID: op}
	}
	s.notifyAudit(0)
	if !ok {
		return adminapi.ErrNotFound
	}
	return nil
}
func clientDTO(s *Server, c store.Client) adminapi.Client {
	x := adminapi.Client{Fingerprint: c.Fingerprint, Note: c.Note, Username: c.Username, Email: c.Email, ComputerName: c.ComputerName, FirstSeenAt: c.FirstSeenAt, LastSeenAt: c.LastSeenAt, LastIP: c.LastIP, LastClientID: c.LastClientID, LastReportedName: c.LastReportedName, LastRole: c.LastRole, LastVersion: c.LastVersion, Authorized: s.fingerprintAuthorized(c.Fingerprint), Blocked: s.policy.Blocked(c.Fingerprint)}
	for _, v := range s.sessions.Snapshot() {
		if v.Fingerprint == c.Fingerprint {
			x.ActiveSessionCount++
		}
	}
	x.Online = x.ActiveSessionCount > 0
	x.IdentityVerified = c.HasActiveIdentity
	x.EffectiveAccess = x.Authorized && !x.Blocked && x.IdentityVerified
	return x
}

func (b *adminBackend) ImportPublicKey(ctx context.Context, r adminapi.ImportPublicKeyRequest) (adminapi.ImportPublicKeyResult, error) {
	s := (*Server)(b)
	r.ClientID = strings.TrimSpace(r.ClientID)
	if !store.ValidClientID(r.ClientID) {
		return adminapi.ImportPublicKeyResult{}, &adminapi.BackendError{Code: "invalid_client_id", Message: "a valid original device ID is required"}
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(r.PublicKey)))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 || len(options) != 0 {
		if err == nil {
			err = errors.New("multiple public keys and authorized_keys options are not allowed")
		}
		return adminapi.ImportPublicKeyResult{}, &adminapi.BackendError{Code: "invalid_public_key", Message: err.Error()}
	}
	// Parse embedded metadata when present so malformed TunnelX comments are not silently accepted.
	if strings.HasPrefix(strings.TrimSpace(comment), "tunnelx:") {
		if _, err = keygen.ParseMetadataComment(comment); err != nil {
			return adminapi.ImportPublicKeyResult{}, &adminapi.BackendError{Code: "invalid_key_metadata", Message: err.Error()}
		}
	}
	metadata := keygen.Metadata{Username: strings.TrimSpace(r.Username), Email: strings.TrimSpace(r.Email), ComputerName: strings.TrimSpace(r.ComputerName)}
	canonicalComment, err := metadata.Comment()
	if err != nil {
		return adminapi.ImportPublicKeyResult{}, &adminapi.BackendError{Code: "invalid_key_metadata", Message: err.Error()}
	}
	fp := ssh.FingerprintSHA256(key)
	err = s.policy.PersistChange(fp, func() error {
		// Check conflicts and persist intent before changing the authorization
		// file. Management changes are serialized through the policy writer.
		op, beginErr := s.store.BeginClientRegistration(ctx, r.ClientID, fp, s.action(ctx, fp, "register_client", r.Reason, "pending", ""))
		if beginErr != nil {
			if errors.Is(beginErr, store.ErrIdentityMismatch) || errors.Is(beginErr, store.ErrIdentityRevoked) || errors.Is(beginErr, store.ErrIdentityConflict) {
				return adminapi.ErrConflict
			}
			return beginErr
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, applyErr := s.auth.Import(r.PublicKey, canonicalComment)
		if applyErr == nil || errors.Is(applyErr, errAuthorizedKeyExists) {
			// Metadata, binding and successful audit commit together. A known
			// public key can acquire its missing binding through this same form.
			applyErr = s.store.CompleteClientRegistration(finishCtx, op, r.ClientID, fp, metadata.Username, metadata.Email, metadata.ComputerName)
		}
		if applyErr == nil {
			return nil
		}
		if errors.Is(applyErr, store.ErrCommitOutcomeUnknown) {
			s.store.MarkAdminOperationUncertain(op)
			return applyErr // Keep the policy gate fail-closed on an uncertain commit.
		}
		// File replacement and SQLite cannot share a transaction. An interrupted
		// registration is recoverable, but must never be reported as success.
		if auditErr := s.store.CompleteAdminOperation(finishCtx, op, "needs_reconcile", applyErr.Error()); auditErr != nil {
			s.store.MarkAdminOperationUncertain(op)
			if errors.Is(auditErr, store.ErrCommitOutcomeUnknown) {
				return auditErr
			}
		}
		return &adminapi.PendingOperationError{ID: op}
	}, nil)
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		for _, removal := range s.sessions.RemoveFingerprint(fp, "authorization_commit_uncertain") {
			removal.Close()
		}
	}
	s.notifyAudit(0)
	if err != nil {
		return adminapi.ImportPublicKeyResult{}, err
	}
	if s.adminAPI != nil {
		s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
	}
	return adminapi.ImportPublicKeyResult{ClientID: r.ClientID, Fingerprint: fp, Username: metadata.Username, Email: metadata.Email, ComputerName: metadata.ComputerName}, nil
}
func (b *adminBackend) ListClients(ctx context.Context, q adminapi.ListQuery) (adminapi.Page[adminapi.Client], error) {
	s := (*Server)(b)
	if q.Fingerprint != "" {
		x, e := b.GetClient(ctx, q.Fingerprint)
		if errors.Is(e, adminapi.ErrNotFound) {
			return adminapi.Page[adminapi.Client]{Items: []adminapi.Client{}}, nil
		}
		return adminapi.Page[adminapi.Client]{Items: []adminapi.Client{x}}, e
	}
	xs, e := s.store.ListClients(ctx, q.Limit+1, "")
	if e != nil {
		return adminapi.Page[adminapi.Client]{}, e
	}
	out := make([]adminapi.Client, 0, min(len(xs), q.Limit))
	for i, c := range xs {
		if i == q.Limit {
			break
		}
		out = append(out, clientDTO(s, c))
	}
	return adminapi.Page[adminapi.Client]{Items: out}, nil
}
func (b *adminBackend) GetClient(ctx context.Context, fp string) (adminapi.Client, error) {
	s := (*Server)(b)
	c, e := s.store.GetClient(ctx, fp)
	if errors.Is(e, sql.ErrNoRows) {
		return adminapi.Client{}, adminapi.ErrNotFound
	}
	if e != nil {
		return adminapi.Client{}, e
	}
	x := clientDTO(s, c)
	entries, e := s.store.ActiveBlacklist(ctx, time.Now())
	if e != nil {
		return x, e
	}
	for _, v := range entries {
		if v.Fingerprint == fp {
			x.BlockReason = v.Reason
			x.BlockExpiresAt = v.ExpiresAt
			break
		}
	}
	return x, nil
}
func (b *adminBackend) UpdateClientNote(ctx context.Context, fp, note, reason string) error {
	s := (*Server)(b)
	e := s.store.UpdateClientNoteAudited(ctx, fp, note, s.action(ctx, fp, "update_note", reason, "success", ""))
	s.notifyAudit(0)
	if e == nil && s.adminAPI != nil {
		s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
	}
	if errors.Is(e, sql.ErrNoRows) {
		return adminapi.ErrNotFound
	}
	return e
}
func (b *adminBackend) BlockClient(ctx context.Context, fp string, r adminapi.BlockRequest) error {
	s := (*Server)(b)
	var removals []*session.Removal
	now := time.Now().UTC()
	a := s.action(ctx, fp, "block", r.Reason, "success", "")
	err := s.policy.PersistChange(fp, func() error {
		e := s.store.Block(ctx, store.BlockRequest{Entry: store.BlacklistEntry{Fingerprint: fp, Reason: r.Reason, Operator: a.Operator, CreatedAt: now, ExpiresAt: r.ExpiresAt}, Action: a})
		if e != nil {
			_ = s.store.RecordAdminAction(ctx, s.action(ctx, fp, "block", r.Reason, "failed", e.Error()))
			s.notifyAudit(0)
			return e
		}
		return nil
	}, func(snapshot *policy.Snapshot) {
		snapshot.Set(policy.Block{Fingerprint: fp, Reason: r.Reason, Operator: a.Operator, CreatedAt: now, ExpiresAt: r.ExpiresAt})
		removals = s.sessions.RemoveFingerprint(fp, "blocked")
	})
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		removals = append(removals, s.sessions.RemoveFingerprint(fp, "authorization_commit_uncertain")...)
	}
	for _, removal := range removals {
		removal.Close()
	}
	if err == nil {
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
		}
		s.notifyAudit(0)
	}
	return err
}
func (b *adminBackend) UnblockClient(ctx context.Context, fp, reason string) error {
	s := (*Server)(b)
	err := s.policy.PersistChange(fp, func() error {
		e := s.store.Unblock(ctx, fp, s.action(ctx, fp, "unblock", reason, "success", ""))
		if e != nil {
			_ = s.store.RecordAdminAction(ctx, s.action(ctx, fp, "unblock", reason, "failed", e.Error()))
			s.notifyAudit(0)
			return e
		}
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
		}
		s.notifyAudit(0)
		return nil
	}, func(snapshot *policy.Snapshot) { snapshot.Delete(fp) })
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		for _, r := range s.sessions.RemoveFingerprint(fp, "authorization_commit_uncertain") {
			r.Close()
		}
	}
	return err
}

func identityBackendError(err error) error {
	if errors.Is(err, store.ErrIdentityConflict) {
		return adminapi.ErrConflict
	}
	if errors.Is(err, store.ErrIdentityUnbound) {
		return adminapi.ErrNotFound
	}
	if errors.Is(err, store.ErrIdentityMismatch) {
		return &adminapi.BackendError{Code: "invalid_identity", Message: "invalid client ID or public key fingerprint"}
	}
	return err
}

func (b *adminBackend) ListIdentities(ctx context.Context, fp string, limit int) ([]store.IdentityBinding, error) {
	return (*Server)(b).store.ListIdentities(ctx, fp, limit)
}
func (b *adminBackend) GetIdentity(ctx context.Context, id string) (store.IdentityBinding, error) {
	v, err := (*Server)(b).store.GetIdentity(ctx, id)
	return v, identityBackendError(err)
}
func (b *adminBackend) ListIdentityClaims(ctx context.Context, fp string, limit int) ([]store.IdentityClaim, error) {
	return (*Server)(b).store.ListIdentityClaims(ctx, fp, limit)
}
func (b *adminBackend) ListAdminOperations(ctx context.Context, limit int) ([]store.AdminOperation, error) {
	return (*Server)(b).store.ListAdminOperations(ctx, limit)
}

func (b *adminBackend) BindIdentity(ctx context.Context, r adminapi.BindIdentityRequest) (store.IdentityBinding, error) {
	s := (*Server)(b)
	var bound store.IdentityBinding
	var removals []*session.Removal
	var metadata keygen.Metadata
	if r.PublicKey != "" {
		var err error
		r.Fingerprint, metadata, err = parseReplacementKey(r)
		if err != nil {
			return bound, err
		}
	}
	previous, err := s.store.GetIdentity(ctx, r.ClientID)
	if err != nil && !errors.Is(err, store.ErrIdentityUnbound) {
		return bound, identityBackendError(err)
	}
	if previous.Generation != r.ExpectedGeneration {
		return bound, adminapi.ErrConflict
	}
	previousFingerprint := previous.Fingerprint
	err = s.policy.PersistChanges([]string{previousFingerprint, r.Fingerprint}, func() error {
		// Recheck under the management writer before authorizing a new key.
		// A stale browser must not change authorized_keys as a side effect.
		current, lookupErr := s.store.GetIdentity(ctx, r.ClientID)
		if lookupErr != nil && !errors.Is(lookupErr, store.ErrIdentityUnbound) {
			return lookupErr
		}
		if current.Generation != r.ExpectedGeneration {
			return adminapi.ErrConflict
		}
		// Stored device keys retain their original identity; restoration requires explicit approval.
		deviceID, lookupErr := s.store.DeviceKeyIdentity(ctx, r.Fingerprint)
		if lookupErr == nil {
			if deviceID != r.ClientID {
				return adminapi.ErrConflict
			}
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		} else if r.PublicKey == "" && !s.auth.AuthorizedFingerprint(r.Fingerprint) {
			return &adminapi.BackendError{Code: "key_not_authorized", Message: "fingerprint must already be registered"}
		}
		if r.PublicKey != "" {
			if err := s.importReplacementKey(ctx, r, metadata); err != nil {
				return err
			}
		}
		bound, err = s.store.BindIdentity(ctx, r.ClientID, r.Fingerprint, r.ExpectedGeneration, s.action(ctx, r.ClientID, "bind_identity", r.Reason, "success", ""))
		return err
	}, func(_ *policy.Snapshot) {
		if previousFingerprint != "" && previousFingerprint != r.Fingerprint {
			removals = append(removals, s.sessions.RemoveFingerprint(previousFingerprint, "identity_rebound")...)
		}
		removals = append(removals, s.sessions.RemoveFingerprint(r.Fingerprint, "identity_updated")...)
	})
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		removals = append(removals, s.sessions.RemoveFingerprint(r.Fingerprint, "authorization_commit_uncertain")...)
		if previousFingerprint != "" && previousFingerprint != r.Fingerprint {
			removals = append(removals, s.sessions.RemoveFingerprint(previousFingerprint, "authorization_commit_uncertain")...)
		}
	}
	for _, r := range removals {
		r.Close()
	}
	if err == nil && s.adminAPI != nil {
		s.adminAPI.Publish("clients.changed", nil)
		s.notifyAudit(0)
	}
	return bound, identityBackendError(err)
}

func (b *adminBackend) RevokeIdentity(ctx context.Context, id string, generation int64, reason string) error {
	s := (*Server)(b)
	identity, err := s.store.GetIdentity(ctx, id)
	if err != nil {
		return identityBackendError(err)
	}
	var removals []*session.Removal
	err = s.policy.PersistChange(identity.Fingerprint, func() error {
		return s.store.RevokeIdentity(ctx, id, generation, s.action(ctx, id, "revoke_identity", reason, "success", ""))
	}, func(_ *policy.Snapshot) {
		removals = s.sessions.RemoveFingerprint(identity.Fingerprint, "identity_revoked")
	})
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		removals = append(removals, s.sessions.RemoveFingerprint(identity.Fingerprint, "authorization_commit_uncertain")...)
	}
	for _, r := range removals {
		r.Close()
	}
	if err == nil && s.adminAPI != nil {
		s.adminAPI.Publish("clients.changed", nil)
		s.notifyAudit(0)
	}
	return identityBackendError(err)
}

// Reconciliation observes actual state; it never replays an authorization file.
func (b *adminBackend) ReconcileAdminOperation(ctx context.Context, id, reason string) error {
	s := (*Server)(b)
	operations, err := s.store.ListAdminOperations(ctx, 200)
	if err != nil {
		return err
	}
	for _, op := range operations {
		if op.ID != id {
			continue
		}
		if op.State == "applied" || op.State == "failed" {
			return nil
		}
		if op.State == "pending" {
			return adminapi.ErrConflict
		}
		applied := false
		switch op.Action {
		case "register_client":
			_, fp, parseErr := store.ParseClientRegistrationTarget(op.TargetID)
			if parseErr != nil {
				return adminapi.ErrConflict
			}
			err = s.policy.PersistChange(fp, func() error {
				authorized, healthy := s.auth.FingerprintStatus(fp)
				if !healthy {
					return adminapi.ErrConflict
				}
				return s.store.ReconcileClientRegistration(ctx, id, authorized, s.action(ctx, id, "reconcile_operation", reason, "success", ""))
			}, nil)
			if errors.Is(err, store.ErrCommitOutcomeUnknown) {
				for _, removal := range s.sessions.RemoveFingerprint(fp, "authorization_commit_uncertain") {
					removal.Close()
				}
			}
			if err == nil {
				s.notifyAudit(0)
			}
			return identityBackendError(err)
		case "delete_client":
			return b.reconcileClientDeletion(ctx, op, reason)
		case "import_public_key":
			var healthy bool
			applied, healthy = s.auth.FingerprintStatus(op.TargetID)
			if !healthy {
				return adminapi.ErrConflict
			}
		case "disconnect":
			_, online := s.sessions.Get(op.TargetID)
			applied = !online
		default:
			return adminapi.ErrConflict
		}
		state := "failed"
		if applied {
			state = "applied"
		}
		if err = s.store.ReconcileAdminOperation(ctx, id, state, "reconciled from current state: "+reason, s.action(ctx, id, "reconcile_operation", reason, "success", "")); err != nil {
			return err
		}
		s.notifyAudit(0)
		return nil
	}
	return adminapi.ErrNotFound
}

func older(t time.Time, id int64, c *adminapi.Cursor) bool {
	return c == nil || t.Before(c.Time) || (t.Equal(c.Time) && id < c.ID)
}
func (b *adminBackend) ListAuditEvents(ctx context.Context, q adminapi.ListQuery) (adminapi.Page[adminapi.AuditEvent], error) {
	s := (*Server)(b)
	limit := q.Limit * 2
	if limit < 50 {
		limit = 50
	}
	connectionFilter := store.AuditFilter{Fingerprint: q.Fingerprint, ClientID: q.ClientID, Result: q.Result, Since: q.From, Until: q.To, Limit: limit}
	accessFilter := store.AccessFilter{Fingerprint: q.Fingerprint, ClientID: q.ClientID, Result: q.Result, Since: q.From, Until: q.To, Limit: limit}
	if q.Cursor != nil {
		connectionFilter.CursorTime = &q.Cursor.Time
		connectionFilter.CursorID = (q.Cursor.ID + 1) / 2
		accessFilter.CursorTime = &q.Cursor.Time
		accessFilter.CursorID = q.Cursor.ID / 2
	}
	cs, e := s.store.ListConnections(ctx, connectionFilter)
	if e != nil {
		return adminapi.Page[adminapi.AuditEvent]{}, e
	}
	as, e := s.store.ListAccesses(ctx, accessFilter)
	if e != nil {
		return adminapi.Page[adminapi.AuditEvent]{}, e
	}
	all := make([]adminapi.AuditEvent, 0, len(cs)+len(as))
	for _, v := range cs {
		id := v.ID * 2
		if older(v.AuthenticatedAt, id, q.Cursor) {
			sid := ""
			if v.SessionID != nil {
				sid = *v.SessionID
			}
			all = append(all, adminapi.AuditEvent{ID: id, Time: v.AuthenticatedAt, Kind: "connection", SessionID: sid, Fingerprint: v.Fingerprint, ClientID: v.ClientID, RemoteIP: v.RemoteIP, Result: v.Result, Reason: v.DisconnectReason})
		}
	}
	for _, v := range as {
		id := v.ID*2 + 1
		if older(v.StartedAt, id, q.Cursor) {
			all = append(all, adminapi.AuditEvent{ID: id, Time: v.StartedAt, Kind: "access", SessionID: v.VisitorSessionID, Fingerprint: v.VisitorFingerprint, ClientID: v.VisitorClientID, TargetSessionID: v.TargetSessionID, TargetFingerprint: v.TargetFingerprint, TargetClientID: v.TargetClientID, TargetTunnelID: v.TargetTunnelID, TargetTunnelName: v.TargetTunnelName, Result: v.Result, Reason: v.Reason, BytesSent: v.BytesSent, BytesReceived: v.BytesReceived})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Time.Equal(all[j].Time) {
			return all[i].ID > all[j].ID
		}
		return all[i].Time.After(all[j].Time)
	})
	page := adminapi.Page[adminapi.AuditEvent]{}
	hasMore := len(all) > q.Limit
	if hasMore {
		all = all[:q.Limit]
	}
	page.Items = all
	if hasMore {
		last := all[len(all)-1]
		page.NextCursor = adminapi.EncodeCursor(adminapi.Cursor{Time: last.Time, ID: last.ID})
	}
	return page, nil
}
func (b *adminBackend) ListAdminActions(ctx context.Context, q adminapi.ListQuery) (adminapi.Page[adminapi.AdminAction], error) {
	filter := store.AdminActionFilter{TargetID: q.Fingerprint, Result: q.Result, Since: q.From, Until: q.To, Limit: q.Limit + 1}
	if q.Cursor != nil {
		filter.CursorTime = &q.Cursor.Time
		filter.CursorID = q.Cursor.ID
	}
	xs, e := (*Server)(b).store.ListAdminActionsFiltered(ctx, filter)
	if e != nil {
		return adminapi.Page[adminapi.AdminAction]{}, e
	}
	page := adminapi.Page[adminapi.AdminAction]{Items: make([]adminapi.AdminAction, 0, min(q.Limit, len(xs)))}
	for _, v := range xs {
		if len(page.Items) == q.Limit {
			last := page.Items[len(page.Items)-1]
			page.NextCursor = adminapi.EncodeCursor(adminapi.Cursor{Time: last.CreatedAt, ID: last.ID})
			break
		}
		page.Items = append(page.Items, adminapi.AdminAction{ID: v.ID, Action: v.Action, TargetType: v.TargetType, TargetID: v.TargetID, Reason: v.Reason, Result: v.Result, Error: v.Error, Operator: v.Operator, TransportPeer: v.TransportPeer, SourceIP: v.SourceIP, CreatedAt: v.CreatedAt})
	}
	return page, nil
}
