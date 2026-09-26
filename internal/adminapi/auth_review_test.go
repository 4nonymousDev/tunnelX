package adminapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tunnelx/internal/store"
)

func TestBrowserTotalCapacityFailsClosedAndReclaimsExpiredSession(t *testing.T) {
	s := newTestServer(t, &fakeBackend{})
	now := time.Now()
	var oldest string
	for i := 0; i < 256; i++ {
		id, _, err := s.sessions.create(store.Account{Username: fmt.Sprintf("admin-%d", i), Enabled: true, IsAdmin: true, Generation: 1}, "http://127.0.0.1:2223", now)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = id
		}
	}
	w := browserRequest(s, http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"a long test password"}`, nil, "")
	if w.Code != http.StatusServiceUnavailable || len(w.Result().Cookies()) != 0 {
		t.Fatalf("full session store issued login: %d", w.Code)
	}
	s.sessions.mu.Lock()
	count := len(s.sessions.entries)
	key := sha256.Sum256([]byte(oldest))
	entry := s.sessions.entries[key]
	entry.ExpiresAt = now.Add(-time.Second)
	s.sessions.entries[key] = entry
	s.sessions.mu.Unlock()
	if count != 256 {
		t.Fatal("session store exceeded its limit")
	}
	cookie, _ := loginBrowser(t, s, "operator", "a long test password", nil)
	if w := browserRequest(s, http.MethodGet, "/api/v1/overview", "", cookie, ""); w.Code != http.StatusOK {
		t.Fatalf("reclaimed slot unusable: %d", w.Code)
	}
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	if len(s.sessions.entries) != 256 {
		t.Fatalf("session count after reclaim=%d", len(s.sessions.entries))
	}
	if _, exists := s.sessions.entries[key]; exists {
		t.Fatal("expired session retained")
	}
}

func TestAmbiguousSessionCookiesCannotAuthorizeWrites(t *testing.T) {
	backend := &fakeBackend{}
	s := newTestServer(t, backend)
	cookie, identity := loginBrowser(t, s, "operator", "a long test password", nil)
	for _, values := range [][]string{
		{cookie.Value, cookie.Value},
		{strings.Repeat("A", 43), cookie.Value},
		{cookie.Value, strings.Repeat("A", 43)},
		{strings.Repeat("_", 42) + "."},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2223/api/v1/sessions/s1/disconnect", strings.NewReader(`{"reason":"cookie ambiguity test"}`))
		r.Header.Set("Origin", "http://127.0.0.1:2223")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", identity.CSRF)
		for _, value := range values {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || backend.disconnectID != "" {
			t.Fatalf("ambiguous cookie authorized mutation: %d", w.Code)
		}
	}
	if w := browserRequest(s, http.MethodGet, "/api/v1/overview", "", cookie, ""); w.Code != http.StatusOK {
		t.Fatal("ambiguous requests revoked another valid session")
	}
}

type pausedLoginBackend struct {
	AuthBackend
	checked chan struct{}
	resume  <-chan struct{}
}

func (a *pausedLoginBackend) AuthenticateAccount(ctx context.Context, username, password string) (store.Account, error) {
	account, err := a.AuthBackend.AuthenticateAccount(ctx, username, password)
	close(a.checked)
	select {
	case <-a.resume:
		return account, err
	case <-ctx.Done():
		return store.Account{}, ctx.Err()
	}
}

func TestAccountChangeDuringLoginCannotCreateUsableOldSession(t *testing.T) {
	for _, change := range []string{"password", "disabled", "demoted"} {
		t.Run(change, func(t *testing.T) {
			s, db := authFixture(t)
			resume := make(chan struct{})
			defer func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
			}()
			paused := &pausedLoginBackend{AuthBackend: db, checked: make(chan struct{}), resume: resume}
			s.auth = paused
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				result <- browserRequest(s, http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"`+authPassword+`"}`, nil, "")
			}()
			select {
			case <-paused.checked:
			case <-time.After(3 * time.Second):
				t.Fatal("password validation did not complete")
			}
			account, err := db.GetAccount(context.Background(), "operator")
			if err != nil {
				close(resume)
				t.Fatal(err)
			}
			no := false
			var enabled, isAdmin *bool
			password := ""
			switch change {
			case "password":
				password = "replacement administrator password"
			case "disabled":
				enabled = &no
			case "demoted":
				isAdmin = &no
			}
			_, err = db.UpdateAccountWithRole(context.Background(), "operator", password, enabled, isAdmin, account.Generation, store.AdminAction{Operator: "test", Reason: "concurrent authorization change"})
			close(resume)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-result:
				// A login may finish with its earlier snapshot; no protected request
				// may use it once the account's current generation/role changed.
				if w.Code == http.StatusOK {
					cookies := w.Result().Cookies()
					if len(cookies) != 1 {
						t.Fatal("successful login missing cookie")
					}
					if after := browserRequest(s, http.MethodGet, "/api/v1/overview", "", cookies[0], ""); after.Code != http.StatusUnauthorized {
						t.Fatalf("stale login authorized after %s: %d", change, after.Code)
					}
				} else if w.Code != http.StatusUnauthorized {
					t.Fatalf("unexpected login status %d", w.Code)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("login did not resume")
			}
		})
	}
}
