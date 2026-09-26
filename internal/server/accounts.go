package server

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/enrollment"
	"tunnelx/internal/store"
)

// Account ownership is permanent. A managed key must never fall back to an old
// authorized_keys entry after password reset, account disable or device revoke.
func (s *Server) fingerprintAuthorized(fp string) bool {
	if s.store != nil {
		ctx, cancel := s.dbContext()
		owned, allowed, err := s.store.AccountKeyStatus(ctx, fp)
		cancel()
		if err != nil {
			return false
		}
		if owned {
			return allowed
		}
	}
	return s.auth.AuthorizedFingerprint(fp)
}

func (s *Server) authenticateAccount(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
	deny := func() (*ssh.Permissions, error) {
		s.recordRejected("", "", "credentials")
		return nil, errors.New("account authentication failed")
	}
	username := c.User()
	if s.store == nil || !store.ValidAccountUsername(username) || len(password) > 512 || !s.loginRates.allow(remoteHost(c.RemoteAddr()), username, time.Now()) {
		return deny()
	}
	ctx, cancel := s.dbContext()
	defer cancel()
	if s.store.CheckAdmission(ctx) != nil || s.policy.Blocked("") {
		return deny()
	}
	account, err := s.store.AuthenticateAccount(ctx, username, string(password))
	if err != nil {
		return deny()
	}
	return &ssh.Permissions{Extensions: map[string]string{
		"auth-mode": "account-enrollment", "account": account.Username,
		"account-generation": strconv.FormatInt(account.Generation, 10),
	}}, nil
}

// Password-authenticated transports can only enroll a device. They never enter
// the business session registry or acquire tunnel/forwarding permissions.
func (s *Server) serveEnrollment(raw net.Conn, conn *ssh.ServerConn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request) {
	deadline := time.Now().Add(15 * time.Second)
	_ = raw.SetDeadline(deadline)
	timer := time.AfterFunc(time.Until(deadline), func() { _ = conn.Close() })
	defer timer.Stop()
	var workers sync.WaitGroup
	defer func() { _ = conn.Close(); workers.Wait() }()
	rate := newBucket(10, 20)
	workers.Add(1)
	go func() { defer workers.Done(); s.discardRequests(conn, reqs, rate) }()
	next, ok := <-chans
	if !ok {
		return
	}
	if next.ChannelType() != enrollment.ChannelType || len(next.ExtraData()) != 0 {
		s.rejectChannel(conn, next, ssh.Prohibited, "account login permits device registration only")
		return
	}
	ch, requests, err := s.acceptChannel(conn, next)
	if err != nil {
		return
	}
	workers.Add(1)
	go func() { defer workers.Done(); s.discardRequests(conn, requests, rate) }()
	// Any additional channel is a protocol violation, not a second enrollment.
	workers.Add(1)
	go func() {
		defer workers.Done()
		if _, extra := <-chans; extra {
			_ = conn.Close()
		}
	}()
	var req enrollment.Request
	if enrollment.Read(ch, &req) != nil {
		return
	}
	username := conn.Permissions.Extensions["account"]
	generation, err := strconv.ParseInt(conn.Permissions.Extensions["account-generation"], 10, 64)
	if err != nil || generation < 1 || !store.ValidClientID(req.ClientID) || !utf8.ValidString(req.Name) || utf8.RuneCountInString(req.Name) > 200 || len(req.PublicKey) > 8192 || len(req.Signature) > 4096 {
		_ = enrollment.Write(ch, enrollment.Response{Error: "设备登记失败：资料无效"})
		return
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(req.PublicKey)))
	var signature ssh.Signature
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || ssh.Unmarshal(req.Signature, &signature) != nil {
		_ = enrollment.Write(ch, enrollment.Response{Error: "设备登记失败：凭据无效"})
		return
	}
	if _, certificate := key.(*ssh.Certificate); certificate || key.Verify(enrollment.SigningPayload(conn.SessionID(), username, req.ClientID, key), &signature) != nil {
		_ = enrollment.Write(ch, enrollment.Response{Error: "设备登记失败：持钥验证失败"})
		return
	}
	fp := ssh.FingerprintSHA256(key)
	err = s.policy.PersistChange(fp, func() error {
		ctx, cancel := s.dbContext()
		defer cancel()
		// Epoch, identity, ownership and blacklist are checked again inside the
		// transaction. Account disabling cannot race a new device into trust.
		return s.store.EnrollAccountDevice(ctx, username, generation, req.ClientID, fp, req.Name, store.AdminAction{
			Action: "account_device_login", TargetType: "client", TargetID: fp,
			Operator: "account:" + username, TransportPeer: conn.RemoteAddr().String(),
			Reason: "account-authenticated device registration", Result: "success", CreatedAt: time.Now(),
		})
	}, nil)
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		for _, removal := range s.sessions.RemoveFingerprint(fp, "authorization_commit_uncertain") {
			removal.Close()
		}
	}
	if err != nil {
		message := "设备登记失败：账号状态、设备归属或数量限制不允许此操作，请联系管理员"
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			message = "服务器存储异常，已暂停接入，请联系管理员"
		}
		_ = enrollment.Write(ch, enrollment.Response{Error: message})
		return
	}
	s.notifyAudit(0)
	if s.adminAPI != nil {
		s.adminAPI.Publish("clients.changed", nil)
	}
	_ = enrollment.Write(ch, enrollment.Response{OK: true})
}

type loginWindow struct {
	start time.Time
	count int
}
type loginLimiter struct {
	mu      sync.Mutex
	windows map[string]loginWindow
}

// Bound password guesses before expensive hashing. Windows and key cardinality
// are bounded; an attacker cannot permanently lock an account or grow this map.
func (l *loginLimiter) allow(ip, username string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = make(map[string]loginWindow)
	}
	const duration = 5 * time.Minute
	for key, window := range l.windows {
		if now.Sub(window.start) >= duration {
			delete(l.windows, key)
		}
	}
	keys := []string{"global", "ip:" + ip, "user:" + username}
	limits := []int{120, 30, 10}
	missing := 0
	for i, key := range keys {
		window, ok := l.windows[key]
		if !ok {
			missing++
		}
		if window.count >= limits[i] {
			return false
		}
	}
	if len(l.windows)+missing > 2048 {
		return false
	}
	for _, key := range keys {
		window := l.windows[key]
		if window.start.IsZero() {
			window.start = now
		}
		window.count++
		l.windows[key] = window
	}
	return true
}
