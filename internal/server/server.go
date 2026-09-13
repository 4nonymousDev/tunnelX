// Package server implements TunnelX's SSH tunnel and loopback management servers.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/policy"
	"tunnelx/internal/proto"
	"tunnelx/internal/registry"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

type Config struct {
	Addr, HostKeyPath, AuthorizedKeys, Version string
	AdminAddr, AdminTokenFile, DataDir         string
	AuditPath                                  string // legacy only; ignored when DataDir is set
	HelloTimeout                               time.Duration
}

type Server struct {
	cfg                     Config
	reg                     *registry.Registry
	sshCfg                  *ssh.ServerConfig
	logf                    func(string, ...any)
	auth                    *authKeys
	audit                   *auditLog
	sessions                *session.Manager
	store                   *store.Store
	policy                  *policy.Policy
	adminAPI                *adminapi.Server
	adminSrv                *http.Server
	mu                      sync.Mutex
	listener, adminListener net.Listener
	connections             map[net.Conn]struct{}
	registryIDs             map[string]uint64
	auditIDs                map[string]int64
	helloTimers             map[string]*time.Timer
	closeOnce               sync.Once
	closeErr                error
	closing                 atomic.Bool
	wg                      sync.WaitGroup
}

func New(cfg Config, logf func(string, ...any)) (*Server, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.HelloTimeout <= 0 {
		cfg.HelloTimeout = 15 * time.Second
	}
	hostKey, err := loadHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, err
	}
	authorized, err := newAuthKeys(cfg.AuthorizedKeys, logf)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, reg: registry.New(), logf: logf, auth: authorized, connections: map[net.Conn]struct{}{}, registryIDs: map[string]uint64{}, auditIDs: map[string]int64{}, helloTimers: map[string]*time.Timer{}}
	if cfg.DataDir != "" {
		if err = os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return nil, fmt.Errorf("创建数据目录: %w", err)
		}
		s.store, err = store.Open(filepath.Join(cfg.DataDir, "tunnel-server.db"))
		if err != nil {
			return nil, fmt.Errorf("打开管理数据库: %w", err)
		}
		entries, e := s.store.ActiveBlacklist(context.Background(), time.Now().UTC())
		if e != nil {
			s.store.Close()
			return nil, e
		}
		blocks := make([]policy.Block, 0, len(entries))
		for _, b := range entries {
			blocks = append(blocks, policy.Block{Fingerprint: b.Fingerprint, Reason: b.Reason, Operator: b.Operator, CreatedAt: b.CreatedAt, ExpiresAt: b.ExpiresAt})
		}
		s.policy = policy.New(blocks)
	} else {
		s.policy = policy.New(nil)
	}
	legacy := ""
	if cfg.DataDir == "" {
		legacy = cfg.AuditPath
	}
	s.audit, err = newAuditLog(legacy)
	if err != nil {
		s.cleanupNew()
		return nil, err
	}
	s.sessions = session.New(s.onSessionRemoved)
	if cfg.AdminAddr != "" || cfg.AdminTokenFile != "" {
		if cfg.DataDir == "" {
			s.cleanupNew()
			return nil, errors.New("管理端需要配置 data-dir")
		}
		if err = validateAdminAddr(cfg.AdminAddr); err != nil {
			s.cleanupNew()
			return nil, err
		}
		token, e := readAdminToken(cfg.AdminTokenFile)
		if e != nil {
			s.cleanupNew()
			return nil, e
		}
		s.adminAPI, e = adminapi.New((*adminBackend)(s), token)
		if e != nil {
			s.cleanupNew()
			return nil, e
		}
		s.adminSrv = &http.Server{Handler: adminTimeoutHandler(s.adminAPI), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	}
	s.sshCfg = &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		fp := ssh.FingerprintSHA256(key)
		remote := remoteHost(c.RemoteAddr())
		if s.policy.Blocked(fp) {
			s.recordRejected(fp, remote, "blocked")
			return nil, fmt.Errorf("公钥已被禁用: %s", fp)
		}
		if !authorized.Authorized(key) {
			s.recordRejected(fp, remote, "rejected")
			s.audit.write(auditEvent{Event: auditRejected, RemoteAddr: c.RemoteAddr().String(), User: c.User(), Fingerprint: fp, Reason: "公钥未被授权"})
			return nil, fmt.Errorf("公钥未被授权: %s", fp)
		}
		return &ssh.Permissions{Extensions: map[string]string{"pubkey-fp": fp}}, nil
	}}
	s.sshCfg.AddHostKey(hostKey)
	return s, nil
}

func (s *Server) cleanupNew() {
	if s.audit != nil {
		_ = s.audit.Close()
	}
	if s.store != nil {
		_ = s.store.Close()
	}
}
func (s *Server) ListenAndServe() error {
	if s.adminSrv != nil {
		ln, e := net.Listen("tcp", s.cfg.AdminAddr)
		if e != nil {
			return fmt.Errorf("监听管理端 %s: %w", s.cfg.AdminAddr, e)
		}
		s.mu.Lock()
		s.adminListener = ln
		s.mu.Unlock()
		go func() {
			if e := s.adminSrv.Serve(ln); e != nil && !errors.Is(e, http.ErrServerClosed) && !s.closing.Load() {
				s.logf("管理端服务失败: %v", e)
			}
		}()
		s.logf("管理端已启动，监听 %s", ln.Addr())
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		if s.adminSrv != nil {
			_ = s.adminSrv.Close()
		}
		return fmt.Errorf("监听 %s: %w", s.cfg.Addr, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	s.logf("tunnel-server %s 已启动，监听 %s（%d 个授权公钥）", s.cfg.Version, ln.Addr(), s.auth.Count())
	for {
		raw, e := ln.Accept()
		if e != nil {
			if errors.Is(e, net.ErrClosed) {
				break
			}
			s.logf("接受连接失败: %v", e)
			continue
		}
		s.mu.Lock()
		s.connections[raw] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.handleConn(raw) }()
	}
	s.wg.Wait()
	return nil
}
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		s.mu.Lock()
		ln, aln := s.listener, s.adminListener
		raws := make([]net.Conn, 0, len(s.connections))
		for c := range s.connections {
			raws = append(raws, c)
		}
		s.mu.Unlock()
		if ln != nil {
			_ = ln.Close()
		}
		if aln != nil {
			_ = aln.Close()
		}
		if s.adminSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.adminSrv.Shutdown(ctx)
			cancel()
		}
		if s.adminAPI != nil {
			_ = s.adminAPI.Close()
		}
		s.sessions.CloseAll("server_shutdown")
		for _, c := range raws {
			_ = c.Close()
		}
		s.wg.Wait()
		if s.audit != nil {
			_ = s.audit.Close()
		}
		if s.store != nil {
			s.closeErr = s.store.Close()
		}
	})
	return s.closeErr
}

func (s *Server) handleConn(raw net.Conn) {
	defer func() { s.mu.Lock(); delete(s.connections, raw); s.mu.Unlock(); _ = raw.Close() }()
	sshConn, chans, reqs, err := ssh.NewServerConn(raw, s.sshCfg)
	if err != nil {
		s.logf("握手失败 %s: %v", raw.RemoteAddr(), err)
		return
	}
	fp := sshConn.Permissions.Extensions["pubkey-fp"]
	remote := remoteHost(sshConn.RemoteAddr())
	var sess *session.Session
	admitted, err := s.policy.Admit(fp, func() error {
		var e error
		sess, e = s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: fp, RemoteIP: remote, ConnectedAt: time.Now().UTC(), Closer: sshConn})
		return e
	})
	if err != nil || !admitted {
		_ = sshConn.Close()
		s.recordRejected(fp, remote, "blocked")
		return
	}
	s.startHelloTimer(sess.ID)
	if s.adminAPI != nil {
		s.adminAPI.Publish("sessions.changed", map[string]any{"session_id": sess.ID})
	}
	if s.store != nil {
		id := sess.ID
		row, e := s.store.StartConnection(context.Background(), store.ConnectionAudit{SessionID: &id, Fingerprint: fp, RemoteIP: remote, AuthenticatedAt: sess.ConnectedAt, Result: "authenticated"})
		if e != nil {
			s.logf("记录连接审计失败: %v", e)
			s.sessions.Disconnect(sess.ID, "audit_failed")
			return
		}
		s.mu.Lock()
		s.auditIDs[sess.ID] = row
		s.mu.Unlock()
		s.notifyAudit(row)
		if e = s.store.UpsertClient(context.Background(), store.Seen{Fingerprint: fp, IP: remote, At: sess.ConnectedAt}); e != nil {
			s.sessions.Disconnect(sess.ID, "client_store_failed")
			return
		}
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
		}
	}
	s.audit.write(auditEvent{Event: auditConnect, RemoteAddr: remote, User: sshConn.User(), Fingerprint: fp})
	go s.handleGlobalRequests(sshConn, sess.ID, reqs)
	var channels sync.WaitGroup
	for newCh := range chans {
		switch newCh.ChannelType() {
		case proto.ChannelType:
			if e := s.sessions.BeginControl(sess.ID); e != nil {
				_ = newCh.Reject(ssh.Prohibited, "仅允许一个控制通道")
				continue
			}
			ch, chReqs, e := newCh.Accept()
			if e != nil {
				continue
			}
			go ssh.DiscardRequests(chReqs)
			channels.Add(1)
			go func() { defer channels.Done(); defer ch.Close(); s.serveControl(sess.ID, ch) }()
		case "direct-tcpip":
			channels.Add(1)
			go func(n ssh.NewChannel) { defer channels.Done(); s.handleDirectTCPIP(sess.ID, n) }(newCh)
		default:
			_ = newCh.Reject(ssh.UnknownChannelType, fmt.Sprintf("不支持的 channel 类型: %s", newCh.ChannelType()))
		}
	}
	channels.Wait()
	if r := s.sessions.Remove(sess.ID, "connection_closed"); r != nil {
		r.Close()
	}
}

type directTCPIPPayload struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

func (s *Server) handleDirectTCPIP(visitorID string, newCh ssh.NewChannel) {
	var p directTCPIPPayload
	if ssh.Unmarshal(newCh.ExtraData(), &p) != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, "无法解析转发请求")
		return
	}
	visitor, visitorOK := s.sessions.Get(visitorID)
	owner, ok := s.sessions.LookupPublishedPort(int(p.DestPort))
	if !visitorOK || visitor.State != session.Online || !isLoopback(p.DestAddr) || !ok {
		s.startFailedAccess(visitor, owner, int(p.DestPort), "target_not_published")
		_ = newCh.Reject(ssh.Prohibited, "目标端口未由在线 Exporter 发布")
		return
	}
	auditID := int64(0)
	if s.store != nil {
		var auditErr error
		auditID, auditErr = s.store.StartAccess(context.Background(), accessRecord(visitor, owner, "started", ""))
		if auditErr != nil {
			_ = newCh.Reject(ssh.ConnectionFailed, "无法记录访问审计")
			return
		}
		s.notifyAudit(auditID)
	}
	target := fmt.Sprintf("127.0.0.1:%d", p.DestPort)
	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		if auditID > 0 {
			_ = s.store.FinishAccess(context.Background(), auditID, "failed", err.Error(), 0, 0, time.Now())
			s.notifyAudit(auditID)
		}
		_ = newCh.Reject(ssh.ConnectionFailed, "目标已离线")
		return
	}
	defer conn.Close()
	ch, reqs, err := newCh.Accept()
	if err != nil {
		if auditID > 0 {
			_ = s.store.FinishAccess(context.Background(), auditID, "failed", err.Error(), 0, 0, time.Now())
			s.notifyAudit(auditID)
		}
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	var sent, received atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(conn, ch)
		sent.Add(n)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() { defer wg.Done(); n, _ := io.Copy(ch, conn); received.Add(n); _ = ch.CloseWrite() }()
	wg.Wait()
	if auditID > 0 {
		_ = s.store.FinishAccess(context.Background(), auditID, "completed", "", sent.Load(), received.Load(), time.Now())
		s.notifyAudit(auditID)
	}
}
func accessRecord(v session.Session, f session.Forward, result, reason string) store.AccessAudit {
	a := store.AccessAudit{VisitorSessionID: v.ID, VisitorFingerprint: v.Fingerprint, VisitorClientID: v.ClientID, TargetSessionID: f.SessionID, TargetFingerprint: f.Fingerprint, TargetClientID: f.ClientID, TargetRemotePort: f.Port, Result: result, Reason: reason}
	if f.Tunnel != nil {
		a.TargetTunnelID = f.Tunnel.ID
		a.TargetTunnelName = f.Tunnel.Name
	}
	return a
}
func (s *Server) startFailedAccess(v session.Session, f session.Forward, port int, reason string) {
	if s.store == nil {
		return
	}
	f.Port = port
	a := accessRecord(v, f, "failed", reason)
	id, e := s.store.StartAccess(context.Background(), a)
	if e == nil {
		s.notifyAudit(id)
		_ = s.store.FinishAccess(context.Background(), id, "failed", reason, 0, 0, time.Now())
		s.notifyAudit(id)
	}
}
func isLoopback(addr string) bool { ip := net.ParseIP(addr); return ip != nil && ip.IsLoopback() }
func splitAddr(a net.Addr) (string, int) {
	if tcp, ok := a.(*net.TCPAddr); ok {
		return tcp.IP.String(), tcp.Port
	}
	return "127.0.0.1", 1
}

type forwardRequest struct {
	Addr string
	Port uint32
}
type forwardReply struct{ Port uint32 }

func (s *Server) handleGlobalRequests(c *ssh.ServerConn, sid string, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			s.handleForward(c, sid, req)
		case "cancel-tcpip-forward":
			var fr forwardRequest
			if ssh.Unmarshal(req.Payload, &fr) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_, e := s.sessions.CancelForward(sid, int(fr.Port))
			if e == nil {
				s.removePublishedPort(sid, int(fr.Port))
			}
			_ = req.Reply(e == nil, nil)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}
func (s *Server) handleForward(c *ssh.ServerConn, sid string, req *ssh.Request) {
	var fr forwardRequest
	if ssh.Unmarshal(req.Payload, &fr) != nil || fr.Port != 0 {
		_ = req.Reply(false, nil)
		return
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		_ = req.Reply(false, nil)
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if e = s.sessions.RegisterForward(sid, port, ln); e != nil {
		_ = ln.Close()
		_ = req.Reply(false, nil)
		return
	}
	if req.WantReply {
		_ = req.Reply(true, ssh.Marshal(forwardReply{Port: uint32(port)}))
	}
	go s.acceptForwarded(c, ln, port)
}
func (s *Server) acceptForwarded(c *ssh.ServerConn, ln net.Listener, port int) {
	defer ln.Close()
	for {
		conn, e := ln.Accept()
		if e != nil {
			return
		}
		go s.forwardOne(c, conn, port)
	}
}

func (s *Server) removePublishedPort(sid string, port int) {
	current, ok := s.sessions.Get(sid)
	if !ok {
		return
	}
	tunnels := make([]session.Tunnel, 0, len(current.Tunnels))
	registryTunnels := make([]proto.TunnelSpec, 0, len(current.Tunnels))
	for _, tunnel := range current.Tunnels {
		if tunnel.RemotePort == port {
			continue
		}
		tunnels = append(tunnels, tunnel)
		registryTunnels = append(registryTunnels, proto.TunnelSpec{
			TunnelID: tunnel.ID, SrcHost: tunnel.SrcHost, SrcPort: tunnel.SrcPort,
			RemotePort: tunnel.RemotePort, Name: tunnel.Name,
		})
	}
	if len(tunnels) == len(current.Tunnels) {
		return
	}
	_ = s.sessions.Publish(sid, tunnels)
	s.reg.Publish(s.registryID(sid), registryTunnels)
	if s.adminAPI != nil {
		s.adminAPI.Publish("sessions.changed", map[string]any{"session_id": sid})
	}
}

type forwardedTCPPayload struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

func (s *Server) forwardOne(c *ssh.ServerConn, conn net.Conn, port int) {
	defer conn.Close()
	oa, op := splitAddr(conn.RemoteAddr())
	ch, reqs, e := c.OpenChannel("forwarded-tcpip", ssh.Marshal(forwardedTCPPayload{Addr: "127.0.0.1", Port: uint32(port), OriginAddr: oa, OriginPort: uint32(op)}))
	if e != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(ch, conn); _ = ch.CloseWrite() }()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, ch)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
}

func (s *Server) serveControl(sid string, ch ssh.Channel) {
	pc := proto.NewConn(ch)
	_, ok := s.handshake(sid, pc)
	if !ok {
		s.sessions.Disconnect(sid, "hello_failed")
		return
	}
	updates, unsub := s.reg.Subscribe()
	defer unsub()
	done := make(chan struct{})
	defer close(done)
	go func() {
		s.pushRegistry(pc)
		for {
			select {
			case <-done:
				return
			case <-updates:
				s.pushRegistry(pc)
			}
		}
	}()
	for {
		env, raw, e := pc.Recv()
		if e != nil {
			return
		}
		switch env.Type {
		case proto.TypePublish:
			var p proto.Publish
			if proto.Decode(raw, &p) != nil || !validTunnelSpecs(p.Tunnels) {
				s.sendErr(pc, proto.CodeBadRequest, "无法解析上报内容")
				continue
			}
			tunnels := make([]session.Tunnel, len(p.Tunnels))
			for i, t := range p.Tunnels {
				tunnels[i] = session.Tunnel{ID: t.TunnelID, Name: t.Name, SrcHost: t.SrcHost, SrcPort: t.SrcPort, RemotePort: t.RemotePort}
			}
			_ = s.sessions.Publish(sid, tunnels)
			rid := s.registryID(sid)
			s.reg.Publish(rid, p.Tunnels)
			_ = pc.Send(proto.PublishOK{V: proto.Version, Type: proto.TypePublishOK})
			if s.adminAPI != nil {
				s.adminAPI.Publish("sessions.changed", map[string]any{"session_id": sid})
			}
		default:
			s.sendErr(pc, proto.CodeBadRequest, "未知消息类型")
		}
	}
}
func (s *Server) handshake(sid string, pc *proto.Conn) (proto.Hello, bool) {
	env, raw, e := pc.Recv()
	if e != nil {
		return proto.Hello{}, false
	}
	if env.Type != proto.TypeHello {
		s.sendErr(pc, proto.CodeBadRequest, "首条消息必须是 hello")
		return proto.Hello{}, false
	}
	if env.V != proto.Version {
		s.sendErr(pc, proto.CodeVersionMismatch, "协议版本不兼容")
		return proto.Hello{}, false
	}
	var h proto.Hello
	if proto.Decode(raw, &h) != nil || !validHello(h) {
		s.sendErr(pc, proto.CodeBadRequest, "hello 字段无效")
		return h, false
	}
	if e = s.sessions.CompleteHello(sid, session.Hello{ClientID: h.ID, Name: h.Name, Role: h.Role, Version: h.ClientVersion}); e != nil {
		if errors.Is(e, session.ErrDuplicateClient) {
			s.sendErr(pc, proto.CodeDuplicateID, "该客户端标识已在线")
		} else {
			s.sendErr(pc, proto.CodeInternalError, "登记会话失败")
		}
		return h, false
	}
	s.cancelHelloTimer(sid)
	rid, ok := s.reg.Join(h.ID, h.Name, h.ClientVersion, h.Role)
	if !ok {
		s.sendErr(pc, proto.CodeDuplicateID, "该客户端标识已在线")
		return h, false
	}
	s.mu.Lock()
	s.registryIDs[sid] = rid
	auditID := s.auditIDs[sid]
	s.mu.Unlock()
	if s.store != nil {
		now := time.Now().UTC()
		if e = s.store.UpsertClient(context.Background(), store.Seen{Fingerprint: s.fingerprint(sid), ClientID: h.ID, ReportedName: h.Name, Role: h.Role, Version: h.ClientVersion, At: now}); e == nil {
			e = s.store.CompleteHello(context.Background(), auditID, store.ConnectionAudit{ClientID: h.ID, HelloAt: &now, Result: "online", ReportedName: h.Name, Role: h.Role, Version: h.ClientVersion})
		}
		if e != nil {
			s.reg.Leave(rid)
			s.sendErr(pc, proto.CodeInternalError, "持久化会话失败")
			return h, false
		}
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": s.fingerprint(sid)})
			s.notifyAudit(auditID)
		}
	}
	if e = pc.Send(proto.HelloOK{V: proto.Version, Type: proto.TypeHelloOK, ServerVersion: s.cfg.Version}); e != nil {
		s.reg.Leave(rid)
		return h, false
	}
	if s.adminAPI != nil {
		s.adminAPI.Publish("sessions.changed", map[string]any{"session_id": sid})
	}
	return h, true
}
func validHello(h proto.Hello) bool {
	return strings.TrimSpace(h.ID) != "" && utf8.RuneCountInString(h.ID) <= 128 && utf8.RuneCountInString(h.Name) <= 200 && utf8.RuneCountInString(h.ClientVersion) <= 100 && (h.Role == proto.RoleExporter || h.Role == proto.RoleImporter)
}

func validTunnelSpecs(tunnels []proto.TunnelSpec) bool {
	if len(tunnels) > 1000 {
		return false
	}
	for _, tunnel := range tunnels {
		if tunnel.SrcPort < 0 || tunnel.SrcPort > 65535 || tunnel.RemotePort < 1 || tunnel.RemotePort > 65535 {
			return false
		}
		if utf8.RuneCountInString(tunnel.TunnelID) > 128 || utf8.RuneCountInString(tunnel.Name) > 200 || utf8.RuneCountInString(tunnel.SrcHost) > 255 {
			return false
		}
	}
	return true
}
func (s *Server) registryID(sid string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registryIDs[sid]
}
func (s *Server) fingerprint(sid string) string { x, _ := s.sessions.Get(sid); return x.Fingerprint }
func (s *Server) pushRegistry(pc *proto.Conn) {
	entries := s.reg.Snapshot()
	if entries == nil {
		entries = []proto.RegistryEntry{}
	}
	_ = pc.Send(proto.Registry{V: proto.Version, Type: proto.TypeRegistry, Entries: entries})
}
func (s *Server) sendErr(pc *proto.Conn, code, msg string) {
	_ = pc.Send(proto.Error{V: proto.Version, Type: proto.TypeError, Code: code, Msg: msg})
}

func (s *Server) onSessionRemoved(x session.Session, reason string) {
	s.mu.Lock()
	rid := s.registryIDs[x.ID]
	delete(s.registryIDs, x.ID)
	aid := s.auditIDs[x.ID]
	delete(s.auditIDs, x.ID)
	if timer := s.helloTimers[x.ID]; timer != nil {
		timer.Stop()
		delete(s.helloTimers, x.ID)
	}
	s.mu.Unlock()
	if rid != 0 {
		s.reg.Leave(rid)
	}
	if s.store != nil && aid != 0 {
		_ = s.store.FinishConnection(context.Background(), aid, "disconnected", reason, time.Now())
		s.notifyAudit(aid)
	}
	if s.adminAPI != nil {
		s.adminAPI.Publish("sessions.changed", map[string]any{"session_id": x.ID})
		s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": x.Fingerprint})
	}
}

func (s *Server) startHelloTimer(sid string) {
	s.mu.Lock()
	s.helloTimers[sid] = time.AfterFunc(s.cfg.HelloTimeout, func() {
		s.mu.Lock()
		_, ok := s.helloTimers[sid]
		if ok {
			delete(s.helloTimers, sid)
		}
		s.mu.Unlock()
		if ok {
			s.sessions.Disconnect(sid, "hello_timeout")
		}
	})
	s.mu.Unlock()
}

func (s *Server) cancelHelloTimer(sid string) {
	s.mu.Lock()
	if timer := s.helloTimers[sid]; timer != nil {
		timer.Stop()
		delete(s.helloTimers, sid)
	}
	s.mu.Unlock()
}
func (s *Server) recordRejected(fp, remote, result string) {
	if s.store == nil {
		return
	}
	id, e := s.store.StartConnection(context.Background(), store.ConnectionAudit{Fingerprint: fp, RemoteIP: remote, AuthenticatedAt: time.Now(), Result: result, DisconnectReason: result})
	if e == nil {
		s.notifyAudit(id)
	}
}
func (s *Server) notifyAudit(id int64) {
	if s.adminAPI == nil {
		return
	}
	data := map[string]int64{}
	if id > 0 {
		data["last_id"] = id
	}
	s.adminAPI.Publish("audit.appended", data)
}

func remoteHost(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err == nil {
		return host
	}
	return addr.String()
}

func loadHostKey(p string) (ssh.Signer, error) {
	b, e := os.ReadFile(p)
	if e != nil {
		return nil, fmt.Errorf("读取主机密钥 %s: %w", p, e)
	}
	x, e := ssh.ParsePrivateKey(b)
	if e != nil {
		return nil, fmt.Errorf("解析主机密钥 %s: %w", p, e)
	}
	return x, nil
}
func validateAdminAddr(a string) error {
	host, _, e := net.SplitHostPort(a)
	if e != nil {
		return fmt.Errorf("管理监听地址无效: %w", e)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || !ip.IsLoopback() {
		return errors.New("管理监听地址必须是明确的 loopback IP")
	}
	return nil
}
func readAdminToken(p string) (string, error) {
	if p == "" {
		return "", errors.New("admin-token-file 不能为空")
	}
	info, e := os.Stat(p)
	if e != nil {
		return "", fmt.Errorf("读取管理 Token: %w", e)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("管理 Token 必须是普通文件")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("管理 Token 权限必须为 0600，当前为 %04o", info.Mode().Perm())
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return "", e
	}
	token := strings.TrimSuffix(string(b), "\n")
	token = strings.TrimSuffix(token, "\r")
	if len(token) != 64 {
		return "", errors.New("管理 Token 必须是 64 位十六进制文本")
	}
	for _, c := range token {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", errors.New("管理 Token 必须是 64 位十六进制文本")
		}
	}
	return token, nil
}
func adminTimeoutHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
