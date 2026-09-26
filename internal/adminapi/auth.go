package adminapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"tunnelx/internal/accountauth"
	"tunnelx/internal/store"
)

const sessionCookieName = "tunnelx_admin_session"
const sessionLifetime = 7 * 24 * time.Hour
const sessionIdleTimeout = 12 * time.Hour

type AuthBackend interface {
	AuthenticateAccount(context.Context, string, string) (store.Account, error)
	GetAccount(context.Context, string) (store.Account, error)
	RecordAdminAction(context.Context, store.AdminAction) error
}

type browserIdentity struct {
	Username   string    `json:"username"`
	CSRF       string    `json:"csrf_token"`
	ExpiresAt  time.Time `json:"expires_at"`
	Generation int64     `json:"-"`
	LastSeen   time.Time `json:"-"`
	Origin     string    `json:"-"`
}

type browserSessions struct {
	mu      sync.Mutex
	entries map[[32]byte]browserIdentity
}

func randomCredential() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

func expiredSession(v browserIdentity, now time.Time) bool {
	return !now.Before(v.ExpiresAt) || now.Sub(v.LastSeen) >= sessionIdleTimeout
}

func (b *browserSessions) create(account store.Account, origin string, now time.Time) (string, browserIdentity, error) {
	id, err := randomCredential()
	if err != nil {
		return "", browserIdentity{}, err
	}
	csrf, err := randomCredential()
	if err != nil {
		return "", browserIdentity{}, err
	}
	v := browserIdentity{Username: account.Username, Generation: account.Generation, CSRF: csrf, Origin: origin, ExpiresAt: now.Add(sessionLifetime), LastSeen: now}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = make(map[[32]byte]browserIdentity)
	}
	var oldest [32]byte
	var oldestTime time.Time
	count := 0
	for key, existing := range b.entries {
		if expiredSession(existing, now) {
			delete(b.entries, key)
			continue
		}
		if existing.Username == account.Username {
			count++
			if oldestTime.IsZero() || existing.LastSeen.Before(oldestTime) {
				oldest, oldestTime = key, existing.LastSeen
			}
		}
	}
	if count >= 8 {
		delete(b.entries, oldest)
	}
	if len(b.entries) >= 256 {
		return "", browserIdentity{}, store.ErrAccountBusy
	}
	b.entries[sha256.Sum256([]byte(id))] = v
	return id, v, nil
}

func (b *browserSessions) remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, sha256.Sum256([]byte(id)))
}

func (b *browserSessions) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.entries)
}

func cookieCredential(r *http.Request) string {
	// Duplicate cookies can come from another local service/path. Never choose
	// one ambiguously. A credential is useful only for this exact origin.
	cookies := r.CookiesNamed(sessionCookieName)
	if len(cookies) != 1 || len(cookies[0].Value) != 43 {
		return ""
	}
	id := cookies[0].Value
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(decoded) != 32 {
		return ""
	}
	return id
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + strings.ToLower(r.Host)
}

func safeMethod(method string) bool { return method == http.MethodGet || method == http.MethodHead }

func validBrowserOrigin(r *http.Request) bool {
	u, err := url.Parse(requestOrigin(r))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if r.TLS == nil {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return false
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return safeMethod(r.Method)
	}
	return strings.EqualFold(origin, requestOrigin(r))
}

func validCSRF(r *http.Request, expected string) bool {
	actual := r.Header.Get("X-CSRF-Token")
	return len(actual) == 43 && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, id string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: id, Path: "/api/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds()), Expires: expires})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/api/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (s *Server) browserIdentity(r *http.Request, touch bool) (browserIdentity, bool) {
	id := cookieCredential(r)
	if id == "" {
		return browserIdentity{}, false
	}
	key := sha256.Sum256([]byte(id))
	now := time.Now()
	s.sessions.mu.Lock()
	v, ok := s.sessions.entries[key]
	if ok && (expiredSession(v, now) || v.Origin != requestOrigin(r)) {
		delete(s.sessions.entries, key)
		ok = false
	}
	s.sessions.mu.Unlock()
	if !ok {
		return browserIdentity{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	account, err := s.auth.GetAccount(ctx, v.Username)
	if err != nil || !account.Enabled || !account.IsAdmin || account.Generation != v.Generation {
		s.sessions.remove(id)
		return browserIdentity{}, false
	}
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	current, ok := s.sessions.entries[key]
	if !ok {
		return browserIdentity{}, false
	}
	if touch {
		current.LastSeen = now
		s.sessions.entries[key] = current
	}
	return current, true
}

func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request, rid string) {
	if !allow(w, r, rid, http.MethodPost) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	// Small login input, in addition to the normal API decoder limit.
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	if !decodeJSON(w, r, rid, &body) {
		return
	}
	body.Username = strings.ToLower(strings.TrimSpace(body.Username))
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if !s.logins.allow(ip, body.Username, time.Now()) {
		w.Header().Set("Retry-After", "300")
		writeError(w, 429, "rate_limited", "too many login attempts; retry later", rid)
		return
	}
	if !store.ValidAccountUsername(body.Username) || !accountauth.ValidPassword(body.Password) {
		writeError(w, 401, "login_failed", "invalid administrator credentials", rid)
		return
	}
	account, err := s.auth.AuthenticateAccount(r.Context(), body.Username, body.Password)
	body.Password = ""
	if errors.Is(err, store.ErrAccountBusy) {
		writeBackendError(w, rid, err)
		return
	}
	if err != nil || !account.Enabled || !account.IsAdmin {
		writeError(w, 401, "login_failed", "invalid administrator credentials", rid)
		return
	}
	if err := s.auth.RecordAdminAction(r.Context(), store.AdminAction{Action: "admin_login", TargetType: "account", TargetID: account.Username, Operator: "account:" + account.Username, TransportPeer: r.RemoteAddr, Reason: "administrator password login", Result: "success", CreatedAt: time.Now()}); err != nil {
		writeBackendError(w, rid, err)
		return
	}
	// Rotate on every successful login; an existing cookie never selects the
	// new session ID, and the password is never kept in session state.
	s.sessions.remove(cookieCredential(r))
	id, identity, err := s.sessions.create(account, requestOrigin(r), time.Now())
	if err != nil {
		writeBackendError(w, rid, err)
		return
	}
	setSessionCookie(w, r, id, identity.ExpiresAt)
	writeJSON(w, 200, identity)
}

func (s *Server) serveAuth(w http.ResponseWriter, r *http.Request, rid string, v browserIdentity) bool {
	switch r.URL.EscapedPath() {
	case "/api/v1/auth/session":
		if allow(w, r, rid, http.MethodGet) {
			writeJSON(w, 200, v)
		}
		return true
	case "/api/v1/auth/logout":
		if !allow(w, r, rid, http.MethodPost) {
			return true
		}
		s.sessions.remove(cookieCredential(r))
		clearSessionCookie(w, r)
		err := s.auth.RecordAdminAction(r.Context(), store.AdminAction{Action: "admin_logout", TargetType: "account", TargetID: v.Username, Operator: "account:" + v.Username, TransportPeer: r.RemoteAddr, Reason: "administrator logout", Result: "success", CreatedAt: time.Now()})
		s.respondEmpty(w, rid, err)
		return true
	}
	return false
}

type adminLoginWindow struct {
	start time.Time
	count int
}
type adminLoginLimiter struct {
	mu      sync.Mutex
	windows map[string]adminLoginWindow
}

func (l *adminLoginLimiter) allow(ip, username string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = make(map[string]adminLoginWindow)
	}
	for key, window := range l.windows {
		if now.Sub(window.start) >= 5*time.Minute {
			delete(l.windows, key)
		}
	}
	// Hash untrusted names to keep retained map keys bounded even on invalid input.
	name := sha256.Sum256([]byte(username))
	keys := []string{"all", "ip:" + ip, "user:" + base64.RawURLEncoding.EncodeToString(name[:])}
	limits := []int{120, 30, 10}
	missing := 0
	for i, key := range keys {
		window, exists := l.windows[key]
		if !exists {
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
