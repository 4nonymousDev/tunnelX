package adminapi

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxBodyBytes = 64 << 10

var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
var eventPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var hashedAssetPattern = regexp.MustCompile(`(?:^|[-.])[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

type Server struct {
	backend   Backend
	token     []byte
	hub       *eventHub
	assets    fs.FS
	heartbeat time.Duration
}

type Option func(*Server) error

func WithAssets(assets fs.FS) Option {
	return func(s *Server) error {
		if assets == nil {
			return errors.New("nil assets")
		}
		s.assets = assets
		return nil
	}
}
func WithHeartbeat(d time.Duration) Option {
	return func(s *Server) error {
		if d <= 0 {
			return errors.New("heartbeat must be positive")
		}
		s.heartbeat = d
		return nil
	}
}

func New(backend Backend, bearerToken string, options ...Option) (*Server, error) {
	if backend == nil {
		return nil, errors.New("adminapi: nil backend")
	}
	if bearerToken == "" {
		return nil, errors.New("adminapi: empty bearer token")
	}
	s := &Server{backend: backend, token: []byte(bearerToken), hub: newEventHub(), assets: embeddedAssets(), heartbeat: 20 * time.Second}
	for _, option := range options {
		if err := option(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Publish emits a best-effort change notification. A slow subscriber is
// disconnected rather than blocking the caller.
func (s *Server) Publish(eventType string, data any) bool {
	if !eventPattern.MatchString(eventType) {
		return false
	}
	return s.hub.publish(Event{Type: eventType, Data: data})
}
func (s *Server) Close() error { s.hub.close(); return nil }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	w.Header().Set("X-Request-ID", requestID)
	setSecurityHeaders(w)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if !s.authorized(r.Header.Get("Authorization")) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required", requestID)
			return
		}
		s.serveAPI(w, r, requestID)
		return
	}
	s.serveStatic(w, r, requestID)
}

func (s *Server) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	candidate := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	want := sha256.Sum256(s.token)
	return subtle.ConstantTimeCompare(candidate[:], want[:]) == 1
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, rid string) {
	escaped := r.URL.EscapedPath()
	switch {
	case escaped == "/api/v1/overview":
		if !allow(w, r, rid, http.MethodGet) {
			return
		}
		v, e := s.backend.Overview(r.Context())
		s.respond(w, rid, v, e)
	case escaped == "/api/v1/sessions":
		if !allow(w, r, rid, http.MethodGet) {
			return
		}
		q, e := parseListQuery(r, false)
		if e != nil {
			bad(w, rid, e)
			return
		}
		v, e := s.backend.ListSessions(r.Context(), q)
		s.respond(w, rid, v, e)
	case strings.HasPrefix(escaped, "/api/v1/sessions/"):
		s.serveSession(w, r, rid, strings.TrimPrefix(escaped, "/api/v1/sessions/"))
	case escaped == "/api/v1/clients":
		if !allow(w, r, rid, http.MethodGet) {
			return
		}
		q, e := parseListQuery(r, false)
		if e != nil {
			bad(w, rid, e)
			return
		}
		v, e := s.backend.ListClients(r.Context(), q)
		s.respond(w, rid, v, e)
	case strings.HasPrefix(escaped, "/api/v1/clients/"):
		s.serveClient(w, r, rid, strings.TrimPrefix(escaped, "/api/v1/clients/"))
	case escaped == "/api/v1/audit/events":
		if !allow(w, r, rid, http.MethodGet) {
			return
		}
		q, e := parseListQuery(r, true)
		if e != nil {
			bad(w, rid, e)
			return
		}
		v, e := s.backend.ListAuditEvents(r.Context(), q)
		if e != nil {
			s.respond(w, rid, nil, e)
			return
		}
		if r.URL.Query().Get("format") == "csv" {
			writeAuditCSV(w, v.Items)
			return
		}
		s.respond(w, rid, v, nil)
	case escaped == "/api/v1/admin-actions":
		if !allow(w, r, rid, http.MethodGet) {
			return
		}
		q, e := parseListQuery(r, true)
		if e != nil {
			bad(w, rid, e)
			return
		}
		v, e := s.backend.ListAdminActions(r.Context(), q)
		s.respond(w, rid, v, e)
	case escaped == "/api/v1/events":
		if !allow(w, r, rid, http.MethodGet) {
			return
		}
		s.serveEvents(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found", rid)
	}
}

func (s *Server) serveSession(w http.ResponseWriter, r *http.Request, rid, tail string) {
	if strings.HasSuffix(tail, "/disconnect") {
		id, e := decodeSegment(strings.TrimSuffix(tail, "/disconnect"))
		if e != nil || id == "" {
			bad(w, rid, errors.New("invalid session id"))
			return
		}
		if !allow(w, r, rid, http.MethodPost) {
			return
		}
		var b reasonBody
		if !decodeJSON(w, r, rid, &b) {
			return
		}
		reason, e := validText(b.Reason, 1, 500, "reason")
		if e != nil {
			bad(w, rid, e)
			return
		}
		e = s.backend.DisconnectSession(r.Context(), id, reason)
		s.respondEmpty(w, rid, e)
		return
	}
	id, e := decodeSegment(tail)
	if e != nil || id == "" {
		bad(w, rid, errors.New("invalid session id"))
		return
	}
	if !allow(w, r, rid, http.MethodGet) {
		return
	}
	v, e := s.backend.GetSession(r.Context(), id)
	s.respond(w, rid, v, e)
}

func (s *Server) serveClient(w http.ResponseWriter, r *http.Request, rid, tail string) {
	isBlock := strings.HasSuffix(tail, "/block")
	raw := tail
	if isBlock {
		raw = strings.TrimSuffix(tail, "/block")
	}
	fp, e := decodeSegment(raw)
	if e != nil || !fingerprintPattern.MatchString(fp) {
		bad(w, rid, errors.New("invalid SSH SHA256 fingerprint"))
		return
	}
	if isBlock {
		switch r.Method {
		case http.MethodPost:
			var b blockBody
			if !decodeJSON(w, r, rid, &b) {
				return
			}
			reason, e := validText(b.Reason, 1, 500, "reason")
			if e != nil {
				bad(w, rid, e)
				return
			}
			if b.ExpiresAt != nil && !b.ExpiresAt.After(time.Now()) {
				bad(w, rid, errors.New("expires_at must be in the future"))
				return
			}
			e = s.backend.BlockClient(r.Context(), fp, BlockRequest{Reason: reason, ExpiresAt: b.ExpiresAt})
			s.respondEmpty(w, rid, e)
		case http.MethodDelete:
			var b reasonBody
			if !decodeJSON(w, r, rid, &b) {
				return
			}
			reason, e := validText(b.Reason, 1, 500, "reason")
			if e != nil {
				bad(w, rid, e)
				return
			}
			e = s.backend.UnblockClient(r.Context(), fp, reason)
			s.respondEmpty(w, rid, e)
		default:
			w.Header().Set("Allow", "POST, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", rid)
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		v, e := s.backend.GetClient(r.Context(), fp)
		s.respond(w, rid, v, e)
	case http.MethodPatch:
		var b noteBody
		if !decodeJSON(w, r, rid, &b) {
			return
		}
		note, e := validText(b.Note, 0, 200, "note")
		if e != nil {
			bad(w, rid, e)
			return
		}
		reason, e := validText(b.Reason, 1, 500, "reason")
		if e != nil {
			bad(w, rid, e)
			return
		}
		e = s.backend.UpdateClientNote(r.Context(), fp, note, reason)
		s.respondEmpty(w, rid, e)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", rid)
	}
}

type reasonBody struct {
	Reason string `json:"reason"`
}
type noteBody struct {
	Note   string `json:"note"`
	Reason string `json:"reason"`
}
type blockBody struct {
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, rid string, dst any) bool {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", rid)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if e = dec.Decode(dst); e != nil {
		status := http.StatusBadRequest
		code := "invalid_json"
		if strings.Contains(e.Error(), "request body too large") {
			status = http.StatusRequestEntityTooLarge
			code = "body_too_large"
		}
		writeError(w, status, code, "invalid JSON body", rid)
		return false
	}
	if e = dec.Decode(&struct{}{}); e != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "JSON body must contain one value", rid)
		return false
	}
	return true
}

func validText(v string, min, max int, name string) (string, error) {
	v = strings.TrimSpace(v)
	n := utf8.RuneCountInString(v)
	if n < min || n > max {
		return "", fmt.Errorf("%s must contain %d to %d characters", name, min, max)
	}
	return v, nil
}
func decodeSegment(v string) (string, error) {
	if v == "" || strings.Contains(v, "/") {
		return "", errors.New("invalid path segment")
	}
	return url.PathUnescape(v)
}

func parseListQuery(r *http.Request, audit bool) (ListQuery, error) {
	v := r.URL.Query()
	q := ListQuery{Limit: 50, Fingerprint: v.Get("fingerprint"), ClientID: v.Get("client_id"), Result: v.Get("result")}
	if q.Fingerprint != "" && !fingerprintPattern.MatchString(q.Fingerprint) {
		return q, errors.New("invalid SSH SHA256 fingerprint")
	}
	if utf8.RuneCountInString(q.ClientID) > 128 {
		return q, errors.New("client_id is too long")
	}
	if utf8.RuneCountInString(q.Result) > 64 {
		return q, errors.New("result is too long")
	}
	if raw := v.Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 200 {
			return q, errors.New("limit must be between 1 and 200")
		}
		q.Limit = n
	}
	if raw := v.Get("cursor"); raw != "" {
		c, e := DecodeCursor(raw)
		if e != nil {
			return q, e
		}
		q.Cursor = &c
	}
	for key, dst := range map[string]**time.Time{"from": &q.From, "to": &q.To} {
		if raw := v.Get(key); raw != "" {
			t, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return q, fmt.Errorf("%s must be RFC3339", key)
			}
			*dst = &t
		}
	}
	if q.From != nil && q.To != nil && q.From.After(*q.To) {
		return q, errors.New("from must not be after to")
	}
	if audit {
		f := v.Get("format")
		if f != "" && f != "csv" {
			return q, errors.New("format must be csv")
		}
	} else if v.Get("format") != "" {
		return q, errors.New("format is not supported")
	}
	return q, nil
}

func allow(w http.ResponseWriter, r *http.Request, rid string, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", rid)
	return false
}

func (s *Server) respond(w http.ResponseWriter, rid string, v any, e error) {
	if e != nil {
		writeBackendError(w, rid, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) respondEmpty(w http.ResponseWriter, rid string, e error) {
	if e != nil {
		writeBackendError(w, rid, e)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func bad(w http.ResponseWriter, rid string, e error) {
	writeError(w, http.StatusBadRequest, "invalid_request", e.Error(), rid)
}
func writeBackendError(w http.ResponseWriter, rid string, e error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	msg := "internal server error"
	if errors.Is(e, ErrNotFound) {
		status = http.StatusNotFound
		code = "not_found"
		msg = "resource not found"
	} else if errors.Is(e, ErrConflict) {
		status = http.StatusConflict
		code = "conflict"
		msg = "operation conflicts with current state"
	} else {
		var be *BackendError
		if errors.As(e, &be) {
			status = http.StatusBadRequest
			code = be.Code
			msg = be.Message
		}
	}
	writeError(w, status, code, msg, rid)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code, msg, rid string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}, "request_id": rid})
}
func newRequestID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 16)
}

func writeAuditCSV(w http.ResponseWriter, items []AuditEvent) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="tunnelx-audit.csv"`)
	writeCSVRow(w, []string{"id", "time", "kind", "session_id", "fingerprint", "client_id", "remote_ip", "target_session_id", "target_fingerprint", "target_client_id", "target_tunnel_id", "target_tunnel_name", "result", "reason", "bytes_sent", "bytes_received"})
	for _, v := range items {
		row := []string{strconv.FormatInt(v.ID, 10), v.Time.UTC().Format(time.RFC3339Nano), v.Kind, v.SessionID, v.Fingerprint, v.ClientID, v.RemoteIP, v.TargetSessionID, v.TargetFingerprint, v.TargetClientID, v.TargetTunnelID, v.TargetTunnelName, v.Result, v.Reason, strconv.FormatInt(v.BytesSent, 10), strconv.FormatInt(v.BytesReceived, 10)}
		for i := range row {
			row[i] = safeCSV(row[i])
		}
		writeCSVRow(w, row)
	}
}
func writeCSVRow(w io.Writer, row []string) {
	for i, cell := range row {
		if i > 0 {
			_, _ = io.WriteString(w, ",")
		}
		_, _ = io.WriteString(w, `"`+strings.ReplaceAll(cell, `"`, `""`)+`"`)
	}
	_, _ = io.WriteString(w, "\r\n")
}
func safeCSV(v string) string {
	trimmed := strings.TrimLeft(v, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming unsupported", w.Header().Get("X-Request-ID"))
		return
	}
	ch, cancel, ok := s.hub.subscribe()
	if !ok {
		return
	}
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.WriteString(w, ": connected\n\n")
	f.Flush()
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			b, _ := json.Marshal(e.Data)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
			f.Flush()
		case <-ticker.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			f.Flush()
		}
	}
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, rid string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		allow(w, r, rid, http.MethodGet, http.MethodHead)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	data, e := fs.ReadFile(s.assets, name)
	if e != nil {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not_found", "resource not found", rid)
			return
		}
		name = "index.html"
		data, e = fs.ReadFile(s.assets, name)
	}
	if e != nil {
		writeError(w, http.StatusNotFound, "not_found", "page not found", rid)
		return
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(name, "assets/") && hashedAssetPattern.MatchString(path.Base(name)) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		w.Header().Set("Content-Type", t)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
}
