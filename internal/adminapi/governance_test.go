package adminapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"tunnelx/internal/store"
)

type governanceFake struct {
	fakeBackend
	bound BindIdentityRequest
	peer  string
}

func (f *governanceFake) GetIdentity(context.Context, string) (store.IdentityBinding, error) {
	return store.IdentityBinding{}, ErrNotFound
}

func (f *governanceFake) ListIdentities(context.Context, string, int) ([]store.IdentityBinding, error) {
	return []store.IdentityBinding{}, nil
}
func (f *governanceFake) ListIdentityClaims(context.Context, string, int) ([]store.IdentityClaim, error) {
	return []store.IdentityClaim{}, nil
}
func (f *governanceFake) BindIdentity(ctx context.Context, r BindIdentityRequest) (store.IdentityBinding, error) {
	f.bound = r
	f.peer = RequestMetadata(ctx).RemoteAddr
	return store.IdentityBinding{ClientID: r.ClientID, Fingerprint: r.Fingerprint, Generation: 1}, nil
}
func (f *governanceFake) RevokeIdentity(context.Context, string, int64, string) error { return nil }
func (f *governanceFake) ListAdminOperations(context.Context, int) ([]store.AdminOperation, error) {
	return []store.AdminOperation{}, nil
}
func (f *governanceFake) ReconcileAdminOperation(context.Context, string, string) error { return nil }

func TestIdentityManagementRequiresAuthenticationAndExplicitValidInput(t *testing.T) {
	f := &governanceFake{}
	s, err := New(f, &testAuthBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fp := "SHA256:" + strings.Repeat("A", 43)
	body := `{"client_id":"existing-device","fingerprint":"` + fp + `","expected_generation":0,"reason":" verified directly "}`
	for _, path := range []string{"/api/v1/identities", "/api/v1/identity-claims", "/api/v1/operations"} {
		if w := request(s, "GET", path, "", "", ""); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	if w := request(s, "POST", "/api/v1/identities", body, "secret", "application/json"); w.Code != 200 {
		t.Fatalf("bind: %d %s", w.Code, w.Body.String())
	}
	if f.bound.ClientID != "existing-device" || f.bound.Reason != "verified directly" || f.peer == "" {
		t.Fatalf("binding/transport missing: %+v peer=%q", f.bound, f.peer)
	}
	for _, invalid := range []string{strings.Replace(body, `"expected_generation":0`, `"expected_generation":-1`, 1), strings.Replace(body, "existing-device", `bad\u0000id`, 1), strings.Replace(body, "verified directly", "", 1)} {
		if w := request(s, "POST", "/api/v1/identities", invalid, "secret", "application/json"); w.Code != 400 {
			t.Fatalf("invalid request status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestSSESubscriberCapReturnsCapacityAfterCancellation(t *testing.T) {
	h := newEventHub()
	defer h.close()
	var cancels []func()
	for i := 0; i < 16; i++ {
		_, cancel, ok := h.subscribe()
		if !ok {
			t.Fatal("capacity unavailable")
		}
		cancels = append(cancels, cancel)
	}
	if _, _, ok := h.subscribe(); ok {
		t.Fatal("subscriber limit exceeded")
	}
	cancels[0]()
	if _, cancel, ok := h.subscribe(); !ok {
		t.Fatal("cancel did not return capacity")
	} else {
		cancel()
	}
}

func TestPendingSideEffectReturnsAcceptedNotFalseFailure(t *testing.T) {
	w := request(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBackendError(w, "rid", &PendingOperationError{ID: "known-op"})
	}), "POST", "/", "", "", "")
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "known-op") || !strings.Contains(w.Body.String(), "needs_reconcile") {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
}
