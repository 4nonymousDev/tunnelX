package server

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	errHandshakeBlockedKey      = errors.New("公钥已被禁用")
	errHandshakeUnauthorizedKey = errors.New("公钥未被授权")
)

const (
	handshakeLogInterval = 5 * time.Second
	handshakeLogBudget   = 20
	maxHandshakeLogText  = 512
)

// Metadata before authentication is self-reported, including the fingerprint
// of an offered key. Never retain passwords, signatures or raw packet bodies.
type handshakeAttempt struct {
	Remote, ClientVersion, User, Method, Fingerprint, KeyStatus string
	Result, Reason, Error, AuthError                            string
	Duration                                                    time.Duration
}

func (s *Server) observedSSHConfig(a *handshakeAttempt) ssh.ServerConfig {
	cfg := *s.sshCfg
	metadata := func(c ssh.ConnMetadata) {
		a.ClientVersion, a.User = logText(string(c.ClientVersion())), logText(c.User())
	}
	banner, authLog, publicKey := cfg.BannerCallback, cfg.AuthLogCallback, cfg.PublicKeyCallback
	cfg.BannerCallback = func(c ssh.ConnMetadata) string {
		metadata(c)
		if banner != nil {
			return banner(c)
		}
		return ""
	}
	cfg.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		metadata(c)
		a.Fingerprint = ssh.FingerprintSHA256(key)
		perms, err := publicKey(c, key)
		switch {
		case errors.Is(err, errHandshakeBlockedKey):
			a.KeyStatus = "blocked_key"
		case errors.Is(err, errHandshakeUnauthorizedKey):
			a.KeyStatus = "unauthorized_key"
		case err == nil:
			a.KeyStatus = "authorized_key"
		default:
			a.KeyStatus = "key_rejected"
		}
		return perms, err
	}
	cfg.AuthLogCallback = func(c ssh.ConnMetadata, method string, err error) {
		metadata(c)
		// "none" is SSH's normal discovery request, not a failed credential.
		if method != "none" {
			a.Method = logText(method)
			a.AuthError = ""
			if err != nil {
				a.AuthError = logText(err.Error())
			}
		}
		if authLog != nil {
			authLog(c, method, err)
		}
	}
	return cfg
}

func handshakeFailureReason(err error, keyStatus string) string {
	if keyStatus == "blocked_key" || keyStatus == "unauthorized_key" {
		return keyStatus
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var authErr *ssh.ServerAuthError
	if errors.As(err, &authErr) {
		return "authentication_failed"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return "peer_closed"
	}
	if errors.As(err, &netErr) {
		return "network_error"
	}
	return "protocol_error"
}

func logText(s string) string {
	if len(s) > maxHandshakeLogText {
		s = strings.ToValidUTF8(s[:maxHandshakeLogText], "?") + "..."
	}
	return s
}

func (s *Server) recordHandshake(a handshakeAttempt) {
	// Count each connection once, rather than counting every offered key and
	// then counting the same connection again as a generic handshake failure.
	if s.store != nil && (a.Result == "failed" || a.Result == "rejected") {
		reason := "handshake"
		if a.Reason == "unauthorized_key" {
			reason = "rejected"
		} else if a.Reason == "blocked_key" {
			reason = "blocked"
		}
		s.store.RecordRejection(reason)
	}
	if entry := s.handshakeLogs.record(time.Now(), a); entry != nil {
		entry.write(s.logf)
	}
}

type handshakeSource struct {
	windowStart, last, nextLog time.Time
	failures, suppressed       uint64
	alerted                    bool
	pendingAlert               *handshakeLog
	latest                     handshakeAttempt
}

type handshakeMonitor struct {
	mu        sync.Mutex
	sources   map[string]*handshakeSource
	limit     int
	window    time.Duration
	threshold uint64
	budget    int
	reset     time.Time
	untracked uint64
}

func newHandshakeMonitor(limit int, window time.Duration, threshold int) *handshakeMonitor {
	return &handshakeMonitor{sources: make(map[string]*handshakeSource), limit: limit, window: window, threshold: uint64(threshold)}
}

type handshakeLog struct {
	attempt              handshakeAttempt
	event                string
	failures, suppressed uint64
	window               time.Duration
	windowStart          time.Time
	suspicious           bool
	untracked            uint64
}

func (e handshakeLog) write(logf func(string, ...any)) {
	if e.event == "overflow" {
		logf("SSH 握手汇总 event=overflow untracked_attempts=%d reason=source_tracking_full", e.untracked)
		return
	}
	a := e.attempt
	rule := "none"
	if e.suspicious {
		rule = "frequent_failures"
	}
	// %q escapes newlines, terminal control characters and other untrusted text.
	logf("SSH 握手 event=%s remote=%q client_version=%q user=%q method=%q fingerprint=%q key_status=%q result=%s reason=%s duration_ms=%d suspicious=%t rule=%s failures=%d window=%s window_start=%s suppressed=%d error=%q auth_error=%q",
		e.event, logText(a.Remote), logText(a.ClientVersion), logText(a.User), logText(a.Method), logText(a.Fingerprint), logText(a.KeyStatus), a.Result, a.Reason, a.Duration.Milliseconds(), e.suspicious, rule, e.failures, e.window, e.windowStart.Format(time.RFC3339Nano), e.suppressed, logText(a.Error), logText(a.AuthError))
}

func (m *handshakeMonitor) resetBudget(now time.Time) {
	if !now.Before(m.reset) {
		m.budget = handshakeLogBudget
		m.reset = now.Add(handshakeLogInterval)
	}
}

func (m *handshakeMonitor) record(now time.Time, a handshakeAttempt) *handshakeLog {
	// Bound retained memory as well as the rendered line length.
	for _, field := range []*string{&a.Remote, &a.ClientVersion, &a.User, &a.Method, &a.Fingerprint, &a.KeyStatus, &a.Error, &a.AuthError} {
		*field = logText(*field)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// After startup only the periodic flush refills the shared budget. New
	// traffic cannot consume a refill before queued alerts get their turn.
	if m.reset.IsZero() {
		m.resetBudget(now)
	}
	ip := remoteHostString(a.Remote)
	source := m.sources[ip]
	if source == nil {
		if len(m.sources) >= m.limit {
			m.untracked++
			return nil
		}
		source = &handshakeSource{windowStart: now}
		m.sources[ip] = source
	}
	if now.Sub(source.windowStart) >= m.window {
		source.windowStart, source.failures = now, 0
		source.alerted = false
		// Keep an alert which was previously suppressed by the global budget.
	}
	source.last, source.latest = now, a
	if suspiciousHandshakeFailure(a) {
		source.failures++
		if source.failures >= m.threshold && !source.alerted && source.pendingAlert == nil {
			source.pendingAlert = &handshakeLog{attempt: a, event: "alert", failures: source.failures, window: m.window, windowStart: source.windowStart, suspicious: true}
			source.alerted = true
		}
	}
	if m.budget <= 0 || (now.Before(source.nextLog) && source.pendingAlert == nil) {
		source.suppressed++
		return nil
	}
	entry := m.entry(source, now, "attempt")
	return &entry
}

func remoteHostString(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

func suspiciousHandshakeFailure(a handshakeAttempt) bool {
	if a.Result == "failed" {
		return true
	}
	// Global capacity or local setup failures are not evidence against a peer.
	return a.Result == "rejected" && (a.Reason == "source_handshake_limit" || a.Reason == "source_rate_limit")
}

// entry is called with m.mu held. Only emitted records consume log budget;
// all attempts still contribute to the bounded source statistics.
func (m *handshakeMonitor) entry(source *handshakeSource, now time.Time, event string) handshakeLog {
	entry := handshakeLog{attempt: source.latest, event: event, failures: source.failures, window: m.window, windowStart: source.windowStart, suspicious: source.failures >= m.threshold}
	if source.pendingAlert != nil {
		entry = *source.pendingAlert
		source.pendingAlert = nil
	}
	entry.suppressed = source.suppressed
	source.suppressed = 0
	source.nextLog = now.Add(handshakeLogInterval)
	m.budget--
	return entry
}

func (m *handshakeMonitor) flush(now time.Time, final bool) []handshakeLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetBudget(now)
	var entries []handshakeLog
	// Alerts take priority over routine summaries after a distributed burst.
	for _, alerts := range []bool{true, false} {
		for _, source := range m.sources {
			if (source.pendingAlert != nil) != alerts || (source.suppressed == 0 && source.pendingAlert == nil) {
				continue
			}
			if !final && (m.budget <= 0 || now.Before(source.nextLog)) {
				continue
			}
			entries = append(entries, m.entry(source, now, "summary"))
		}
	}
	if m.untracked != 0 {
		entries = append(entries, handshakeLog{event: "overflow", untracked: m.untracked})
		m.untracked = 0
	}
	for ip, source := range m.sources {
		if source.suppressed == 0 && source.pendingAlert == nil && now.Sub(source.last) >= max(m.window, handshakeLogInterval) {
			delete(m.sources, ip)
		}
	}
	return entries
}

func (s *Server) flushHandshakeLogs(now time.Time, final bool) {
	for _, entry := range s.handshakeLogs.flush(now, final) {
		entry.write(s.logf)
	}
}
