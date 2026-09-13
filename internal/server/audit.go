package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/keygen"
	"tunnelx/internal/policy"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

// auditLog 记录会话与隧道的历史，与在线注册表完全分离。
// 在线注册表是纯内存的、断开即摘除——它回答"现在谁在线"。
// 审计日志回答的是另一个问题："上周三谁访问了办公室PC"。二者不可互相替代，
// 也正因如此，加入审计不影响注册表"重启即清空"这一决策的简洁性。
// 输出为 JSON Lines：每行一个事件，便于用 grep/jq 直接分析，也便于日后导入
// 其他系统。
// auditLog records session and tunnel history separately from the live in-memory
// registry: one answers historical access questions, while the other describes who
// is online now. JSON Lines remains easy to inspect with grep/jq and import elsewhere.
type auditLog struct {
	mu sync.Mutex
	f  *os.File
	// enabled 为 false 时所有写入变为空操作，用于未配置审计路径的情形。
	// When enabled is false, writes are no-ops because no audit path was configured.
	enabled bool
}

// adminBackend adapts live and persistent server state to the transport-only
// adminapi contract without exposing either implementation package to HTTP.
type adminBackend Server

func (b *adminBackend) Overview(ctx context.Context) (adminapi.Overview, error) {
	s := (*Server)(b)
	var o adminapi.Overview
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
func (s *Server) action(target, kind, reason, result, errText string) store.AdminAction {
	targetType := "client"
	if kind == "disconnect" {
		targetType = "session"
	}
	peer, _, err := net.SplitHostPort(s.cfg.AdminAddr)
	if err != nil || peer == "" {
		peer = "127.0.0.1"
	}
	return store.AdminAction{Action: kind, TargetType: targetType, TargetID: target, Reason: reason, Result: result, Error: errText, Operator: "admin-token", TransportPeer: peer, CreatedAt: time.Now()}
}
func (b *adminBackend) DisconnectSession(ctx context.Context, id, reason string) error {
	s := (*Server)(b)
	ok := s.sessions.Disconnect(id, reason)
	result := "success"
	errText := ""
	if !ok {
		result = "failed"
		errText = "session not found"
	}
	e := s.store.RecordAdminAction(ctx, s.action(id, "disconnect", reason, result, errText))
	if e != nil {
		return e
	}
	s.notifyAudit(0)
	if !ok {
		return adminapi.ErrNotFound
	}
	return nil
}
func clientDTO(s *Server, c store.Client) adminapi.Client {
	x := adminapi.Client{Fingerprint: c.Fingerprint, Note: c.Note, Username: c.Username, Email: c.Email, ComputerName: c.ComputerName, FirstSeenAt: c.FirstSeenAt, LastSeenAt: c.LastSeenAt, LastIP: c.LastIP, LastClientID: c.LastClientID, LastReportedName: c.LastReportedName, LastRole: c.LastRole, LastVersion: c.LastVersion, Authorized: s.auth.AuthorizedFingerprint(c.Fingerprint), Blocked: s.policy.Blocked(c.Fingerprint)}
	for _, v := range s.sessions.Snapshot() {
		if v.Fingerprint == c.Fingerprint {
			x.ActiveSessionCount++
		}
	}
	x.Online = x.ActiveSessionCount > 0
	x.EffectiveAccess = x.Authorized && !x.Blocked
	return x
}

func (b *adminBackend) ImportPublicKey(ctx context.Context, r adminapi.ImportPublicKeyRequest) (adminapi.ImportPublicKeyResult, error) {
	s := (*Server)(b)
	key, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(r.PublicKey)))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		if err == nil {
			err = errors.New("multiple public keys are not allowed")
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
	if err = s.store.ImportClient(ctx, fp, metadata.Username, metadata.Email, metadata.ComputerName, time.Now()); err == nil {
		_, err = s.auth.Import(r.PublicKey, canonicalComment)
	}
	result, errText := "success", ""
	if err != nil {
		result, errText = "failed", err.Error()
	}
	if auditErr := s.store.RecordAdminAction(ctx, s.action(fp, "import_public_key", r.Reason, result, errText)); auditErr != nil {
		return adminapi.ImportPublicKeyResult{}, auditErr
	}
	s.notifyAudit(0)
	if errors.Is(err, errAuthorizedKeyExists) {
		return adminapi.ImportPublicKeyResult{}, adminapi.ErrConflict
	}
	if err != nil {
		return adminapi.ImportPublicKeyResult{}, err
	}
	if s.adminAPI != nil {
		s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
	}
	return adminapi.ImportPublicKeyResult{Fingerprint: fp, Username: metadata.Username, Email: metadata.Email, ComputerName: metadata.ComputerName}, nil
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
	e := s.store.UpdateClientNote(ctx, fp, note)
	result := "success"
	errText := ""
	if e != nil {
		result = "failed"
		errText = e.Error()
	}
	ae := s.store.RecordAdminAction(ctx, s.action(fp, "update_note", reason, result, errText))
	if ae != nil {
		return ae
	}
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
	err := s.policy.Change(func(snapshot *policy.Snapshot) error {
		now := time.Now().UTC()
		a := s.action(fp, "block", r.Reason, "success", "")
		e := s.store.Block(ctx, store.BlockRequest{Entry: store.BlacklistEntry{Fingerprint: fp, Reason: r.Reason, Operator: "admin-token", CreatedAt: now, ExpiresAt: r.ExpiresAt}, Action: a})
		if e != nil {
			_ = s.store.RecordAdminAction(ctx, s.action(fp, "block", r.Reason, "failed", e.Error()))
			s.notifyAudit(0)
			return e
		}
		snapshot.Set(policy.Block{Fingerprint: fp, Reason: r.Reason, Operator: "admin-token", CreatedAt: now, ExpiresAt: r.ExpiresAt})
		removals = s.sessions.RemoveFingerprint(fp, "blocked")
		return nil
	})
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
	return s.policy.Change(func(snapshot *policy.Snapshot) error {
		e := s.store.Unblock(ctx, fp, s.action(fp, "unblock", reason, "success", ""))
		if e != nil {
			_ = s.store.RecordAdminAction(ctx, s.action(fp, "unblock", reason, "failed", e.Error()))
			s.notifyAudit(0)
			return e
		}
		snapshot.Delete(fp)
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
		}
		s.notifyAudit(0)
		return nil
	})
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

// 审计事件类型。
// Audit event types.
const (
	auditConnect = "connect" // 连接建立并通过认证 / Connection established and authenticated.
	// Connection established and authenticated.
	auditDisconnect = "disconnect" // 连接断开 / Connection closed.
	// Connection closed.
	auditRegister = "register" // 控制通道握手成功 / Control-channel handshake succeeded.
	// Control-channel handshake completed.
	auditPublish = "publish" // 上报隧道列表 / Tunnel list published.
	// Tunnel list published.
	auditForward = "forward" // 经服务端转发一次连接 / One connection forwarded through the server.
	// One connection forwarded through the server.
	auditRejected = "rejected" // 认证或请求被拒 / Authentication or request rejected.
	// Authentication or request rejected.
)

// auditEvent 是一条审计记录。
// 字段刻意保持扁平：JSON Lines 的价值在于能被 grep 直接过滤，
// 嵌套结构会让 `grep '"client_id":"abc"'` 这类用法失效。
// auditEvent is deliberately flat so simple JSON Lines grep filters keep working.
type auditEvent struct {
	Time        time.Time `json:"time"`
	Event       string    `json:"event"`
	RemoteAddr  string    `json:"remote_addr,omitempty"`
	User        string    `json:"user,omitempty"`
	ClientID    string    `json:"client_id,omitempty"`
	ClientName  string    `json:"client_name,omitempty"`
	Role        string    `json:"role,omitempty"`
	Version     string    `json:"version,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	// 转发相关
	// Forwarding fields.
	RemotePort int `json:"remote_port,omitempty"`
	SrcPort    int `json:"src_port,omitempty"`
	Tunnels    int `json:"tunnels,omitempty"`
	// 拒绝原因
	// Rejection reason.
	Reason string `json:"reason,omitempty"`
}

// newAuditLog 打开审计日志文件。path 为空时返回一个禁用的实例。
// newAuditLog opens the audit file or returns a disabled instance for an empty path.
func newAuditLog(path string) (*auditLog, error) {
	if path == "" {
		return &auditLog{enabled: false}, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开审计日志 %s: %w", path, err)
	}
	return &auditLog{f: f, enabled: true}, nil
}

// write 追加一条事件。
// 立即落盘而不做缓冲：审计日志的价值在于事后追溯，进程被杀时丢失最近的记录
// 恰恰会丢掉最关键的部分。事件频率很低（连接、断开、上报），开销可忽略。
// write appends and immediately flushes an event so termination cannot lose the
// most important recent audit records; event volume is low.
func (a *auditLog) write(e auditEvent) {
	if a == nil || !a.enabled {
		return
	}

	e.Time = time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.f == nil {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.f.Write(append(data, '\n'))
	a.f.Sync()
}

// Close 关闭审计日志。
// Close closes the audit log.
func (a *auditLog) Close() error {
	if a == nil || !a.enabled {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	return err
}
