package adminapi

import (
	"context"
	"errors"
	"net/http"
	"time"
	"tunnelx/internal/updatepolicy"
)

type UpdatePolicyRequest struct {
	MinimumVersion     string     `json:"minimum_version"`
	EnforceAfter       *time.Time `json:"enforce_after"`
	Message            string     `json:"message"`
	ExpectedGeneration int64      `json:"expected_generation"`
	Reason             string     `json:"reason"`
}

type UpdatePolicyBackend interface {
	GetClientUpdatePolicy(context.Context) (updatepolicy.Policy, error)
	SetClientUpdatePolicy(context.Context, UpdatePolicyRequest) (updatepolicy.Policy, error)
}

func (s *Server) serveUpdatePolicy(w http.ResponseWriter, r *http.Request, rid string) bool {
	if r.URL.EscapedPath() != "/api/v1/client-update-policy" {
		return false
	}
	b, ok := s.backend.(UpdatePolicyBackend)
	if !ok {
		writeError(w, 503, "unsupported", "update policy unavailable", rid)
		return true
	}
	switch r.Method {
	case http.MethodGet:
		p, err := b.GetClientUpdatePolicy(r.Context())
		s.respond(w, rid, p, err)
	case http.MethodPut:
		var body UpdatePolicyRequest
		if !decodeJSON(w, r, rid, &body) {
			return true
		}
		reason, err := validText(body.Reason, 1, 500, "reason")
		if err != nil || body.ExpectedGeneration < 1 {
			bad(w, rid, errors.New("current generation and reason required"))
			return true
		}
		// Omitting a deadline for a newly scheduled requirement means seven
		// days from this request. Reconnecting never restarts this fixed window.
		if body.MinimumVersion != "" && body.EnforceAfter == nil {
			deadline := time.Now().UTC().Add(updatepolicy.DefaultGracePeriod)
			body.EnforceAfter = &deadline
		}
		p := updatepolicy.Policy{MinimumVersion: body.MinimumVersion, EnforceAfter: body.EnforceAfter, Message: body.Message}
		if err := p.Validate(); err != nil {
			bad(w, rid, err)
			return true
		}
		body.Reason = reason
		p, err = b.SetClientUpdatePolicy(r.Context(), body)
		s.respond(w, rid, p, err)
	default:
		allow(w, r, rid, http.MethodGet, http.MethodPut)
	}
	return true
}
