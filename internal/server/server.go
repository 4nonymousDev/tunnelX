// Package server implements TunnelX's SSH tunnel and loopback management servers.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/policy"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

type Config struct {
	Addr, HostKeyPath, AuthorizedKeys, Version  string
	AdminAddr, AdminTokenFile, DataDir          string
	AuditPath                                   string // legacy only; ignored when DataDir is set
	HelloTimeout                                time.Duration
	HandshakeTimeout, WriteTimeout, OpenTimeout time.Duration
	Limits                                      Limits
	IdentityBindings                            map[string]string // explicit bindings for embedded instances without a Store
}

type Server struct {
	cfg                     Config
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
	requestRates            map[*ssh.ServerConn]*rateBucket
	auditIDs                map[string]int64
	helloTimers             map[string]*time.Timer
	closeOnce               sync.Once
	closeErr                error
	closing                 atomic.Bool
	wg                      sync.WaitGroup
	admission               *admission
	logAfter                atomic.Int64
	auditJobs               chan func(context.Context)
	auditStop               chan struct{}
	auditDone               chan struct{}
	loginRates              loginLimiter
}

func New(cfg Config, logf func(string, ...any)) (*Server, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.HelloTimeout <= 0 {
		cfg.HelloTimeout = 15 * time.Second
	}
	cfg.Limits.defaults()
	if cfg.Limits.RegistryBytes > 3<<20 || cfg.Limits.PublishBytes > 3<<20 {
		return nil, errors.New("控制消息预算不能超过 3 MiB")
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = 10 * time.Second
	}
	bindings := map[string]string{}
	for id, fp := range cfg.IdentityBindings {
		bindings[id] = fp
	}
	cfg.IdentityBindings = bindings
	hostKey, err := loadHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, err
	}
	authorized, err := newAuthKeys(cfg.AuthorizedKeys, logf)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, logf: logf, auth: authorized, connections: map[net.Conn]struct{}{}, auditIDs: map[string]int64{}, helloTimers: map[string]*time.Timer{}, admission: newAdmission(cfg.Limits), auditJobs: make(chan func(context.Context), 256), auditStop: make(chan struct{}), auditDone: make(chan struct{})}
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
	s.sessions = session.NewWithLimits(s.onSessionRemoved, cfg.Limits.sessions())
	s.requestRates = make(map[*ssh.ServerConn]*rateBucket)
	if cfg.AdminAddr != "" {
		if cfg.DataDir == "" {
			s.cleanupNew()
			return nil, errors.New("管理端需要配置 data-dir")
		}
		if err = validateAdminAddr(cfg.AdminAddr); err != nil {
			s.cleanupNew()
			return nil, err
		}
		var e error
		s.adminAPI, e = adminapi.New((*adminBackend)(s), s.store)
		if e != nil {
			s.cleanupNew()
			return nil, e
		}
		s.adminSrv = &http.Server{Handler: adminTimeoutHandler(s.adminAPI), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	}
	s.sshCfg = &ssh.ServerConfig{MaxAuthTries: 3, PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		fp := ssh.FingerprintSHA256(key)
		remote := remoteHost(c.RemoteAddr())
		if s.policy.Blocked(fp) {
			s.recordRejected(fp, remote, "blocked")
			return nil, fmt.Errorf("公钥已被禁用: %s", fp)
		}
		if !s.fingerprintAuthorized(fp) {
			s.recordRejected(fp, remote, "rejected")
			return nil, fmt.Errorf("公钥未被授权: %s", fp)
		}
		return &ssh.Permissions{Extensions: map[string]string{"pubkey-fp": fp}}, nil
	}}
	if s.store != nil {
		s.sshCfg.PasswordCallback = s.authenticateAccount
	}
	s.sshCfg.AddHostKey(hostKey)
	go s.runAuditJobs()
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
		if s.closing.Load() {
			s.mu.Unlock()
			_ = ln.Close()
			return net.ErrClosed
		}
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
	if s.closing.Load() {
		s.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
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
		finish, release, ok := s.admission.accept(raw)
		if !ok {
			_ = raw.Close()
			s.recordRejected("", "", "capacity")
			continue
		}
		s.mu.Lock()
		if s.closing.Load() {
			s.mu.Unlock()
			release()
			_ = raw.Close()
			break
		}
		s.connections[raw] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() { defer s.wg.Done(); s.handleAdmitted(raw, finish, release) }()
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
		for _, c := range raws {
			_ = c.Close()
		}
		s.sessions.CloseAll("server_shutdown")
		s.wg.Wait()
		close(s.auditStop)
		<-s.auditDone
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
	finish, release, ok := s.admission.accept(raw)
	if !ok {
		_ = raw.Close()
		return
	}
	s.handleAdmitted(raw, finish, release)
}

func (s *Server) handleAdmitted(raw net.Conn, finish, release func()) {
	defer release()
	defer func() { s.mu.Lock(); delete(s.connections, raw); s.mu.Unlock(); _ = raw.Close() }()
	if err := raw.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout)); err != nil {
		return
	}
	sshConn, chans, reqs, err := ssh.NewServerConn(raw, s.sshCfg)
	finish()
	if err != nil {
		s.recordRejected("", "", "handshake")
		return
	}
	_ = raw.SetDeadline(time.Time{})
	defer sshConn.Close()
	if sshConn.Permissions.Extensions["auth-mode"] == "account-enrollment" {
		s.serveEnrollment(raw, sshConn, chans, reqs)
		return
	}
	rate := newBucket(20, 40)
	s.mu.Lock()
	s.requestRates[sshConn] = rate
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.requestRates, sshConn); s.mu.Unlock() }()
	fp := sshConn.Permissions.Extensions["pubkey-fp"]
	remote := remoteHost(sshConn.RemoteAddr())
	version := s.policy.Version()
	if fp == "" || !s.fingerprintAuthorized(fp) {
		return
	}
	var sess *session.Session
	admitted, err := s.policy.AdmitVersion(fp, version, func() error {
		var e error
		sess, e = s.sessions.AddAuthenticated(session.Authenticated{Fingerprint: fp, RemoteIP: remote, ConnectedAt: time.Now().UTC(), Closer: sshConn})
		return e
	})
	if err != nil || !admitted {
		_ = sshConn.Close()
		s.recordRejected(fp, remote, "blocked")
		return
	}
	defer s.sessions.Disconnect(sess.ID, "disconnected")
	if s.adminAPI != nil {
		s.adminAPI.Publish("sessions.changed", map[string]any{"session_id": sess.ID})
	}
	if s.store != nil {
		id := sess.ID
		ctx, cancel := s.dbContext()
		row, e := s.store.StartConnection(ctx, store.ConnectionAudit{SessionID: &id, Fingerprint: fp, RemoteIP: remote, AuthenticatedAt: sess.ConnectedAt, Result: "authenticated"})
		cancel()
		if e != nil {
			s.limitedLog("记录连接审计失败: %v", e)
			s.sessions.Disconnect(sess.ID, "audit_failed")
			return
		}
		s.mu.Lock()
		_, live := s.sessions.Get(sess.ID)
		if live {
			s.auditIDs[sess.ID] = row
		}
		s.mu.Unlock()
		if !live {
			s.queueAudit(func(ctx context.Context) {
				_ = s.store.FinishConnection(ctx, row, "disconnected", "admission_cancelled", time.Now())
			})
			return
		}
		s.notifyAudit(row)
		ctx, cancel = s.dbContext()
		e = s.store.UpsertClient(ctx, store.Seen{Fingerprint: fp, IP: remote, At: sess.ConnectedAt})
		cancel()
		if e != nil {
			s.sessions.Disconnect(sess.ID, "client_store_failed")
			return
		}
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": fp})
		}
	}
	s.startHelloTimer(sess.ID)
	s.audit.write(auditEvent{Event: auditConnect, RemoteAddr: remote, User: sshConn.User(), Fingerprint: fp})
	var channels sync.WaitGroup
	channels.Add(1)
	go func() { defer channels.Done(); s.discardRequests(sshConn, reqs, rate) }()
	for newCh := range chans {
		if !rate.allow() {
			_ = sshConn.Close()
			break
		}
		switch newCh.ChannelType() {
		case proto.ChannelType:
			if e := s.sessions.BeginControl(sess.ID); e != nil {
				s.rejectChannel(sshConn, newCh, ssh.Prohibited, "仅允许一个控制通道")
				continue
			}
			ch, chReqs, e := s.acceptChannel(sshConn, newCh)
			if e != nil {
				_ = sshConn.Close()
				continue
			}
			channels.Add(2)
			go func() { defer channels.Done(); s.discardRequests(sshConn, chReqs, rate) }()
			go func() {
				defer channels.Done()
				defer s.sessions.Disconnect(sess.ID, "control_closed")
				s.serveControl(sess.ID, sshConn, ch, rate)
			}()
		case proto.OpenChannelType:
			target, e := proto.DecodeOpen(newCh.ExtraData())
			if e != nil {
				s.rejectChannel(sshConn, newCh, ssh.Prohibited, "目标无效")
				continue
			}
			lease, e := s.acquire(sess.ID, target)
			if e != nil {
				s.rejectChannel(sshConn, newCh, ssh.Prohibited, "目标已改变、未授权或容量已满")
				continue
			}
			channels.Add(1)
			go func(n ssh.NewChannel, l *session.Lease) { defer channels.Done(); s.forward(sshConn, n, l, rate) }(newCh, lease)
		default:
			s.rejectChannel(sshConn, newCh, ssh.UnknownChannelType, "不支持的 channel 类型")
		}
	}
	_ = sshConn.Close()
	s.sessions.Disconnect(sess.ID, "connection_closed")
	channels.Wait()
}

func (s *Server) onSessionRemoved(x session.Session, reason string) {
	s.mu.Lock()
	aid := s.auditIDs[x.ID]
	delete(s.auditIDs, x.ID)
	if timer := s.helloTimers[x.ID]; timer != nil {
		timer.Stop()
		delete(s.helloTimers, x.ID)
	}
	s.mu.Unlock()
	if s.store != nil && aid != 0 {
		s.queueAudit(func(ctx context.Context) {
			if err := s.store.FinishConnection(ctx, aid, "disconnected", reason, time.Now()); err != nil {
				s.limitedLog("连接收尾审计失败: %v", err)
			}
			s.notifyAudit(aid)
		})
	}
	if s.adminAPI != nil {
		s.adminAPI.Publish("sessions.changed", map[string]any{"session_id": x.ID})
		s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": x.Fingerprint})
	}
}

func (s *Server) startHelloTimer(sid string) {
	s.mu.Lock()
	if _, ok := s.sessions.Get(sid); !ok {
		s.mu.Unlock()
		return
	}
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
