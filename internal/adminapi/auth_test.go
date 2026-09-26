package adminapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tunnelx/internal/store"
)

const authPassword = "  administrator test password  "

func authFixture(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"operator", "backup"} {
		if _, err = db.ProvisionAdminAccount(context.Background(), name, authPassword, store.AdminAction{Operator: "test", Reason: "setup"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.CreateAccount(context.Background(), "ordinary", authPassword, 10, store.AdminAction{Operator: "test", Reason: "setup"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(&fakeBackend{}, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, db
}

func browserRequest(s *Server, method, target, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:2223"+target, strings.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:2223")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func loginBrowser(t *testing.T, s *Server, username, password string, previous *http.Cookie) (*http.Cookie, browserIdentity) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	w := browserRequest(s, "POST", "/api/v1/auth/login", string(body), previous, "")
	if w.Code != 200 {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	var v browserIdentity
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), password) || strings.Contains(w.Body.String(), cookies[0].Value) {
		t.Fatal("credential leaked to JSON")
	}
	return cookies[0], v
}

func TestBrowserLoginRefreshLogoutAndRotation(t *testing.T) {
	s, _ := authFixture(t)
	cookie, v := loginBrowser(t, s, "operator", authPassword, nil)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/" || cookie.Domain != "" || cookie.Secure || cookie.MaxAge != int(sessionLifetime.Seconds()) {
		t.Fatalf("cookie flags: %+v", cookie)
	}
	w := browserRequest(s, "GET", "/api/v1/auth/session", "", cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), v.CSRF) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("refresh status=%d", w.Code)
	}
	newCookie, newSession := loginBrowser(t, s, "operator", authPassword, cookie)
	if newCookie.Value == cookie.Value || newSession.CSRF == v.CSRF {
		t.Fatal("login did not rotate credentials")
	}
	if w := browserRequest(s, "GET", "/api/v1/overview", "", cookie, ""); w.Code != 401 {
		t.Fatal("old session survived rotation")
	}
	if w := browserRequest(s, "POST", "/api/v1/auth/logout", "", newCookie, newSession.CSRF); w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout=%d", w.Code)
	}
	if w := browserRequest(s, "GET", "/api/v1/auth/session", "", newCookie, ""); w.Code != 401 {
		t.Fatal("session survived logout")
	}
}

func TestOnlyEnabledAdministratorsCanLoginAndSessionTracksAccount(t *testing.T) {
	s, db := authFixture(t)
	for _, username := range []string{"ordinary", "missing"} {
		body, _ := json.Marshal(map[string]string{"username": username, "password": authPassword})
		if w := browserRequest(s, "POST", "/api/v1/auth/login", string(body), nil, ""); w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatalf("non-admin logged in: %s %d", username, w.Code)
		}
	}
	for _, change := range []string{"password", "disabled", "demoted"} {
		t.Run(change, func(t *testing.T) {
			if _, err := db.ProvisionAdminAccount(context.Background(), "operator", authPassword, store.AdminAction{Operator: "test", Reason: "restore"}); err != nil {
				t.Fatal(err)
			}
			cookie, _ := loginBrowser(t, s, "operator", authPassword, nil)
			a, err := db.GetAccount(context.Background(), "operator")
			if err != nil {
				t.Fatal(err)
			}
			var enabled, isAdmin *bool
			password := ""
			no := false
			switch change {
			case "password":
				password = "a new administrator password"
			case "disabled":
				enabled = &no
			case "demoted":
				isAdmin = &no
			}
			if _, err = db.UpdateAccountWithRole(context.Background(), "operator", password, enabled, isAdmin, a.Generation, store.AdminAction{Operator: "test", Reason: change}); err != nil {
				t.Fatal(err)
			}
			if w := browserRequest(s, "GET", "/api/v1/overview", "", cookie, ""); w.Code != 401 {
				t.Fatalf("%s session remained authorized: %d", change, w.Code)
			}
		})
	}
}

func TestBrowserCSRFOriginAndLegacyBearerRejected(t *testing.T) {
	s, _ := authFixture(t)
	cookie, v := loginBrowser(t, s, "operator", authPassword, nil)
	for _, path := range []string{"/api/v1/auth/logout", "/api/v1/sessions/s1/disconnect"} {
		for _, csrf := range []string{"", strings.Repeat("X", 43)} {
			if w := browserRequest(s, "POST", path, `{"reason":"test"}`, cookie, csrf); w.Code != 403 {
				t.Fatalf("missing/wrong CSRF accepted: %d", w.Code)
			}
		}
	}
	for _, tc := range []struct{ origin, host, site string }{
		{"http://evil.example", "127.0.0.1:2223", ""}, {"null", "127.0.0.1:2223", ""},
		{"", "127.0.0.1:2223", ""}, {"http://127.0.0.1:9999", "127.0.0.1:2223", ""},
		{"http://evil.example", "evil.example", ""}, {"http://127.0.0.1:2223", "127.0.0.1:2223", "cross-site"},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:2223/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"`+authPassword+`"}`))
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.site)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", v.CSRF)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("bad origin accepted: %+v %d", tc, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:2223/api/v1/overview", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("legacy token authorized")
	}
	if w := browserRequest(s, "POST", "/api/v1/sessions/s1/disconnect", `{"reason":"test"}`, cookie, v.CSRF); w.Code != 204 {
		t.Fatalf("valid write denied: %d", w.Code)
	}
}

func TestBrowserSessionExpiryCapacityAndOriginBinding(t *testing.T) {
	s := newTestServer(t, &fakeBackend{})
	cookie, v := loginBrowser(t, s, "operator", "a long test password", nil)
	r := httptest.NewRequest("GET", "http://localhost:2223/api/v1/overview", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("cookie usable on different origin")
	}
	for _, idle := range []bool{true, false} {
		cookie, v = loginBrowser(t, s, "operator", "a long test password", nil)
		key := sha256.Sum256([]byte(cookie.Value))
		s.sessions.mu.Lock()
		entry := s.sessions.entries[key]
		if idle {
			entry.LastSeen = time.Now().Add(-sessionIdleTimeout)
		} else {
			entry.ExpiresAt = time.Now().Add(-time.Second)
		}
		s.sessions.entries[key] = entry
		s.sessions.mu.Unlock()
		if w := browserRequest(s, "GET", "/api/v1/overview", "", cookie, ""); w.Code != 401 {
			t.Fatal("expired session usable")
		}
	}
	account, _ := s.auth.GetAccount(context.Background(), "operator")
	for i := 0; i < 50; i++ {
		if _, _, err := s.sessions.create(account, "http://127.0.0.1:2223", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	s.sessions.mu.Lock()
	count := len(s.sessions.entries)
	s.sessions.mu.Unlock()
	if count != 8 {
		t.Fatalf("per-account session capacity=%d", count)
	}
	_ = v
}

func TestSecureCookieAndFailClosedStorage(t *testing.T) {
	s, db := authFixture(t)
	r := httptest.NewRequest("POST", "https://admin.example/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"`+authPassword+`"}`))
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Origin", "https://admin.example")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || !w.Result().Cookies()[0].Secure {
		t.Fatalf("HTTPS cookie: %d", w.Code)
	}
	cookie, _ := loginBrowser(t, s, "operator", authPassword, nil)
	_ = db.Close()
	if w := browserRequest(s, "GET", "/api/v1/overview", "", cookie, ""); w.Code != 401 {
		t.Fatal("storage failure allowed session")
	}
}

func TestSSERevalidatesRevokedAccountWithoutExtendingIdle(t *testing.T) {
	s, db := authFixture(t)
	s.heartbeat = 20 * time.Millisecond
	h := httptest.NewServer(s)
	defer h.Close()
	body, _ := json.Marshal(map[string]string{"username": "operator", "password": authPassword})
	r, _ := http.NewRequest("POST", h.URL+"/api/v1/auth/login", strings.NewReader(string(body)))
	r.Header.Set("Origin", h.URL)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	cookie := resp.Cookies()[0]
	resp.Body.Close()
	r, _ = http.NewRequest("GET", h.URL+"/api/v1/events", nil)
	r.AddCookie(cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	resp, err = http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "connected") {
		t.Fatalf("SSE start: %s %v", line, err)
	}
	key := sha256.Sum256([]byte(cookie.Value))
	s.sessions.mu.Lock()
	before := s.sessions.entries[key].LastSeen
	s.sessions.mu.Unlock()
	for i := 0; i < 5; i++ {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	s.sessions.mu.Lock()
	after := s.sessions.entries[key].LastSeen
	s.sessions.mu.Unlock()
	if !before.Equal(after) {
		t.Fatal("SSE heartbeat extended idle deadline")
	}
	a, _ := db.GetAccount(context.Background(), "operator")
	if _, err = db.UpdateAccount(context.Background(), "operator", "new administrator password", nil, a.Generation, store.AdminAction{Operator: "test", Reason: "reset"}); err != nil {
		t.Fatal(err)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("revoked stream did not terminate: %v", err)
	}
	if strings.Contains(string(remaining), "event:") {
		t.Fatal("revoked stream emitted data")
	}
}

func TestAdminLoginRateLimitsExpireAndBoundSessions(t *testing.T) {
	var l adminLoginLimiter
	now := time.Now()
	for i := 0; i < 10; i++ {
		if !l.allow("127.0.0.1", "operator", now) {
			t.Fatal("early rate limit")
		}
	}
	if l.allow("127.0.0.1", "operator", now) {
		t.Fatal("account guesses unlimited")
	}
	if !l.allow("127.0.0.1", "operator", now.Add(5*time.Minute)) {
		t.Fatal("account permanently locked")
	}
	s := newTestServer(t, &fakeBackend{})
	for i := 0; i < 11; i++ {
		w := browserRequest(s, "POST", "/api/v1/auth/login", `{"username":"operator","password":"invalid test password"}`, nil, "")
		want := 401
		if i == 10 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d status=%d want=%d", i, w.Code, want)
		}
	}
}
