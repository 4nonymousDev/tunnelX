package adminapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tunnelx/internal/updatepolicy"
)

type updatePolicyFake struct {
	fakeBackend
	saved UpdatePolicyRequest
	calls int
}

func (f *updatePolicyFake) GetClientUpdatePolicy(context.Context) (updatepolicy.Policy, error) {
	return updatepolicy.Policy{Generation: 1}, nil
}
func (f *updatePolicyFake) SetClientUpdatePolicy(_ context.Context, r UpdatePolicyRequest) (updatepolicy.Policy, error) {
	f.saved = r
	f.calls++
	return updatepolicy.Policy{MinimumVersion: r.MinimumVersion, EnforceAfter: r.EnforceAfter, Generation: 2}, nil
}

func TestUpdatePolicyAuthorizationValidationAndDefaultGrace(t *testing.T) {
	f := &updatePolicyFake{}
	s, err := New(f, &testAuthBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const path = "/api/v1/client-update-policy"
	valid := `{"minimum_version":"0.2.1","expected_generation":1,"reason":"security fix"}`
	if w := request(s, "GET", path, "", "", ""); w.Code != 401 {
		t.Fatalf("anonymous read=%d", w.Code)
	}
	if w := request(s, "PUT", path, valid, "", "application/json"); w.Code != 401 {
		t.Fatalf("anonymous write=%d", w.Code)
	}
	before := time.Now().Add(updatepolicy.DefaultGracePeriod)
	if w := request(s, "PUT", path, valid, "secret", "application/json"); w.Code != 200 {
		t.Fatalf("valid update=%d %s", w.Code, w.Body.String())
	}
	if f.saved.EnforceAfter == nil || f.saved.EnforceAfter.Before(before) || f.saved.EnforceAfter.After(time.Now().Add(updatepolicy.DefaultGracePeriod)) {
		t.Fatalf("default deadline=%v", f.saved.EnforceAfter)
	}
	for _, body := range []string{
		`{"minimum_version":"bad","expected_generation":1,"reason":"test"}`,
		`{"minimum_version":"0.2.1","reason":"test"}`,
		`{"minimum_version":"0.2.1","expected_generation":1,"reason":""}`,
		`{"minimum_version":"0.2.1","enforce_after":"invalid","expected_generation":1,"reason":"test"}`,
		`{"minimum_version":"","message":"ambiguous","expected_generation":1,"reason":"test"}`,
		`{"minimum_version":"0.2.1","download_url":"https://example.invalid/file","expected_generation":1,"reason":"test"}`,
	} {
		if w := request(s, "PUT", path, body, "secret", "application/json"); w.Code != 400 {
			t.Fatalf("invalid accepted=%d %s", w.Code, w.Body.String())
		}
	}
	if f.calls != 1 {
		t.Fatalf("invalid requests reached mutation: %d", f.calls)
	}
	for _, origin := range []string{"http://127.0.0.1:2223", "https://untrusted.invalid"} {
		r := httptest.NewRequest("PUT", path, strings.NewReader(valid))
		r.Host = "127.0.0.1:2223"
		r.Header.Set("Origin", "http://127.0.0.1:2223")
		r.Header.Set("Content-Type", "application/json")
		authorizeTestRequest(s, r)
		if origin == "http://127.0.0.1:2223" {
			r.Header.Del("X-CSRF-Token")
		} else {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("missing CSRF/foreign origin allowed: %d", w.Code)
		}
	}
	if f.calls != 1 {
		t.Fatal("unauthorized write reached backend")
	}
	if w := request(s, "DELETE", path, "", "secret", ""); w.Code != 405 {
		t.Fatalf("method=%d", w.Code)
	}
}
