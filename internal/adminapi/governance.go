package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"tunnelx/internal/store"
)

type requestInfoKey struct{}
type RequestInfo struct{ RemoteAddr, RequestID, Operator string }

func RequestMetadata(ctx context.Context) RequestInfo {
	v, _ := ctx.Value(requestInfoKey{}).(RequestInfo)
	return v
}

type PendingOperationError struct{ ID string }

func (e *PendingOperationError) Error() string {
	return "administrative operation requires reconciliation: " + e.ID
}

type BindIdentityRequest struct {
	ClientID           string `json:"client_id"`
	Fingerprint        string `json:"fingerprint"`
	ExpectedGeneration int64  `json:"expected_generation"`
	Reason             string `json:"reason"`
}

// A separate capability preserves compatibility for embedders implementing the
// original management Backend. The production backend implements both.
type GovernanceBackend interface {
	GetIdentity(context.Context, string) (store.IdentityBinding, error)
	ListIdentities(context.Context, string, int) ([]store.IdentityBinding, error)
	ListIdentityClaims(context.Context, string, int) ([]store.IdentityClaim, error)
	BindIdentity(context.Context, BindIdentityRequest) (store.IdentityBinding, error)
	RevokeIdentity(context.Context, string, int64, string) error
	ListAdminOperations(context.Context, int) ([]store.AdminOperation, error)
	ReconcileAdminOperation(context.Context, string, string) error
}

func (s *Server) serveGovernance(w http.ResponseWriter, r *http.Request, rid string) bool {
	p := r.URL.EscapedPath()
	if p != "/api/v1/identities" && p != "/api/v1/identity-claims" && p != "/api/v1/operations" && !strings.HasPrefix(p, "/api/v1/identities/") && !strings.HasPrefix(p, "/api/v1/operations/") {
		return false
	}
	b, ok := s.backend.(GovernanceBackend)
	if !ok {
		writeError(w, 503, "unsupported", "identity management unavailable", rid)
		return true
	}
	switch {
	case p == "/api/v1/identities" && r.Method == http.MethodPost:
		var body BindIdentityRequest
		if !decodeJSON(w, r, rid, &body) {
			return true
		}
		reason, err := validText(body.Reason, 1, 500, "reason")
		if err != nil || !store.ValidClientID(body.ClientID) || !store.ValidateFingerprint(body.Fingerprint) || body.ExpectedGeneration < 0 {
			bad(w, rid, errors.New("valid client_id, fingerprint, generation and reason required"))
			return true
		}
		body.Reason = reason
		v, err := b.BindIdentity(r.Context(), body)
		s.respond(w, rid, v, err)
	case p == "/api/v1/identities" || p == "/api/v1/identity-claims":
		if !allow(w, r, rid, http.MethodGet) {
			return true
		}
		q, err := parseListQuery(r, false)
		if err != nil {
			bad(w, rid, err)
			return true
		}
		if p == "/api/v1/identities" {
			if q.ClientID != "" {
				v, e := b.GetIdentity(r.Context(), q.ClientID)
				items := []store.IdentityBinding{}
				if errors.Is(e, ErrNotFound) {
					e = nil
				} else if e == nil {
					items = append(items, v)
				}
				s.respond(w, rid, map[string]any{"items": items}, e)
				return true
			}
			v, e := b.ListIdentities(r.Context(), q.Fingerprint, q.Limit)
			s.respond(w, rid, map[string]any{"items": v}, e)
		} else {
			v, e := b.ListIdentityClaims(r.Context(), q.Fingerprint, q.Limit)
			s.respond(w, rid, map[string]any{"items": v}, e)
		}
	case p == "/api/v1/operations":
		if !allow(w, r, rid, http.MethodGet) {
			return true
		}
		q, err := parseListQuery(r, false)
		if err != nil {
			bad(w, rid, err)
			return true
		}
		v, e := b.ListAdminOperations(r.Context(), q.Limit)
		s.respond(w, rid, map[string]any{"items": v}, e)
	case strings.HasPrefix(p, "/api/v1/identities/") && strings.HasSuffix(p, "/revoke"):
		if !allow(w, r, rid, http.MethodPost) {
			return true
		}
		id, err := decodeSegment(strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/identities/"), "/revoke"))
		if err != nil || !store.ValidClientID(id) {
			bad(w, rid, errors.New("invalid client id"))
			return true
		}
		var body struct {
			ExpectedGeneration int64  `json:"expected_generation"`
			Reason             string `json:"reason"`
		}
		if !decodeJSON(w, r, rid, &body) {
			return true
		}
		reason, err := validText(body.Reason, 1, 500, "reason")
		if err != nil || body.ExpectedGeneration < 1 {
			bad(w, rid, errors.New("generation and reason required"))
			return true
		}
		s.respondEmpty(w, rid, b.RevokeIdentity(r.Context(), id, body.ExpectedGeneration, reason))
	case strings.HasPrefix(p, "/api/v1/operations/") && strings.HasSuffix(p, "/reconcile"):
		if !allow(w, r, rid, http.MethodPost) {
			return true
		}
		id, err := decodeSegment(strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/operations/"), "/reconcile"))
		if err != nil || len(id) != 32 {
			bad(w, rid, errors.New("invalid operation id"))
			return true
		}
		var body reasonBody
		if !decodeJSON(w, r, rid, &body) {
			return true
		}
		reason, err := validText(body.Reason, 1, 500, "reason")
		if err != nil {
			bad(w, rid, err)
			return true
		}
		s.respondEmpty(w, rid, b.ReconcileAdminOperation(r.Context(), id, reason))
	default:
		writeError(w, 404, "not_found", "resource not found", rid)
	}
	return true
}
