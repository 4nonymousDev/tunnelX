package adminapi

import (
	"context"
	"strings"
	"testing"

	"tunnelx/internal/store"
)

type accountsFake struct {
	fakeBackend
	created CreateAccountRequest
	updated UpdateAccountRequest
}

func (f *accountsFake) ListAccounts(context.Context) ([]store.Account, error) {
	return []store.Account{{Username: "alice", Enabled: true, Generation: 1, MaxDevices: 10}}, nil
}
func (f *accountsFake) CreateAccount(_ context.Context, r CreateAccountRequest) (store.Account, error) {
	f.created = r
	return store.Account{Username: r.Username, Enabled: true, Generation: 1}, nil
}
func (f *accountsFake) UpdateAccount(_ context.Context, name string, r UpdateAccountRequest) (store.Account, error) {
	f.updated = r
	return store.Account{Username: name, Generation: r.ExpectedGeneration + 1}, nil
}
func TestAccountManagementRequiresAdminAndKeepsPasswordsOutOfResponses(t *testing.T) {
	f := &accountsFake{}
	s, err := New(f, &testAuthBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, path := range []string{"/api/v1/accounts", "/api/v1/accounts/alice", "/api/v1/accounts/alice/devices"} {
		if w := request(s, "GET", path, "", "", ""); w.Code != 401 {
			t.Fatalf("unauthenticated accounts endpoint: %s %d", path, w.Code)
		}
	}
	body := `{"username":" Alice ","password":"  spaces are part of my password  ","reason":" administrator created "}`
	w := request(s, "POST", "/api/v1/accounts", body, "secret", "application/json")
	if w.Code != 201 || strings.Contains(w.Body.String(), "password") || f.created.Password != "  spaces are part of my password  " || f.created.Username != "alice" || f.created.Reason != "administrator created" {
		t.Fatalf("unexpected account registration behavior: status=%d", w.Code)
	}
	w = request(s, "PATCH", "/api/v1/accounts/alice", `{"password":"another long password","expected_generation":1,"reason":"reset"}`, "secret", "application/json")
	if w.Code != 200 || f.updated.Password != "another long password" || strings.Contains(w.Body.String(), "password") {
		t.Fatalf("unexpected reset response: %d", w.Code)
	}
}

func TestAccountManagementRejectsAmbiguousChanges(t *testing.T) {
	f := &accountsFake{}
	s, err := New(f, &testAuthBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, body := range []string{
		`{"username":"_alice","password":"a long enough password","reason":"create"}`,
		`{"username":"alice","password":"a long enough password","max_devices":51,"reason":"create"}`,
		`{"username":"alice","password":"a long enough password","reason":""}`,
	} {
		if w := request(s, "POST", "/api/v1/accounts", body, "secret", "application/json"); w.Code != 400 {
			t.Fatalf("invalid create accepted: %d", w.Code)
		}
	}
	for _, body := range []string{
		`{"enabled":false,"reason":"disable"}`,
		`{"expected_generation":1,"reason":"empty change"}`,
		`{"enabled":false,"expected_generation":1,"reason":""}`,
		`{"enabled":false,"expected_generation":1,"reason":"disable","unexpected":true}`,
	} {
		if w := request(s, "PATCH", "/api/v1/accounts/alice", body, "secret", "application/json"); w.Code != 400 {
			t.Fatalf("invalid change accepted: %d", w.Code)
		}
	}
	if w := request(s, "DELETE", "/api/v1/accounts/alice", "", "secret", ""); w.Code != 405 {
		t.Fatal("unexpected account deletion endpoint")
	}
}
