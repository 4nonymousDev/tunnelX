package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"tunnelx/internal/store"
)

type CreateAccountRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	MaxDevices int    `json:"max_devices"`
	IsAdmin    bool   `json:"is_admin"`
	Reason     string `json:"reason"`
}
type UpdateAccountRequest struct {
	Password           string `json:"password"`
	Enabled            *bool  `json:"enabled"`
	IsAdmin            *bool  `json:"is_admin"`
	ExpectedGeneration int64  `json:"expected_generation"`
	Reason             string `json:"reason"`
}
type AccountBackend interface {
	ListAccounts(context.Context) ([]store.Account, error)
	CreateAccount(context.Context, CreateAccountRequest) (store.Account, error)
	UpdateAccount(context.Context, string, UpdateAccountRequest) (store.Account, error)
	ListAccountDevices(context.Context, string) ([]store.AccountDevice, error)
}

func (s *Server) serveAccounts(w http.ResponseWriter, r *http.Request, rid string) bool {
	path := r.URL.EscapedPath()
	if path != "/api/v1/accounts" && !strings.HasPrefix(path, "/api/v1/accounts/") {
		return false
	}
	b, ok := s.backend.(AccountBackend)
	if !ok {
		writeError(w, 503, "unsupported", "account management unavailable", rid)
		return true
	}
	if path == "/api/v1/accounts" {
		switch r.Method {
		case http.MethodGet:
			items, err := b.ListAccounts(r.Context())
			s.respond(w, rid, map[string]any{"items": items}, err)
		case http.MethodPost:
			var body CreateAccountRequest
			if !decodeJSON(w, r, rid, &body) {
				return true
			}
			body.Username = strings.ToLower(strings.TrimSpace(body.Username))
			if body.MaxDevices == 0 {
				body.MaxDevices = 10
			}
			reason, err := validText(body.Reason, 1, 500, "reason")
			if err != nil || !store.ValidAccountUsername(body.Username) || body.MaxDevices < 1 || body.MaxDevices > 50 {
				bad(w, rid, errors.New("valid username, max_devices (1–50) and reason required"))
				return true
			}
			body.Reason = reason
			account, err := b.CreateAccount(r.Context(), body)
			s.respondCreated(w, rid, account, err)
		default:
			allow(w, r, rid, http.MethodGet, http.MethodPost)
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/accounts/"), "/")
	if len(parts) > 2 || !store.ValidAccountUsername(parts[0]) {
		bad(w, rid, errors.New("invalid account path"))
		return true
	}
	if len(parts) == 2 {
		if parts[1] != "devices" {
			writeError(w, 404, "not_found", "resource not found", rid)
			return true
		}
		if !allow(w, r, rid, http.MethodGet) {
			return true
		}
		items, err := b.ListAccountDevices(r.Context(), parts[0])
		s.respond(w, rid, map[string]any{"items": items}, err)
		return true
	}
	if !allow(w, r, rid, http.MethodPatch) {
		return true
	}
	var body UpdateAccountRequest
	if !decodeJSON(w, r, rid, &body) {
		return true
	}
	reason, err := validText(body.Reason, 1, 500, "reason")
	if err != nil || body.ExpectedGeneration < 1 || (body.Password == "" && body.Enabled == nil && body.IsAdmin == nil) {
		bad(w, rid, errors.New("password, enabled or is_admin, current generation and reason required"))
		return true
	}
	body.Reason = reason
	account, err := b.UpdateAccount(r.Context(), parts[0], body)
	s.respond(w, rid, account, err)
	return true
}
