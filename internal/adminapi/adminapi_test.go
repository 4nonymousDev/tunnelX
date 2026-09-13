package adminapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	disconnectID, reason     string
	noteFP, note, noteReason string
	blockedFP                string
	audit                    []AuditEvent
}

func (f *fakeBackend) Overview(context.Context) (Overview, error) {
	return Overview{OnlineUsers: 2}, nil
}
func (f *fakeBackend) ListSessions(context.Context, ListQuery) (Page[Session], error) {
	return Page[Session]{Items: []Session{}}, nil
}
func (f *fakeBackend) GetSession(_ context.Context, id string) (Session, error) {
	if id == "missing" {
		return Session{}, ErrNotFound
	}
	return Session{ID: id}, nil
}
func (f *fakeBackend) DisconnectSession(_ context.Context, id, reason string) error {
	f.disconnectID = id
	f.reason = reason
	return nil
}
func (f *fakeBackend) ListClients(context.Context, ListQuery) (Page[Client], error) {
	return Page[Client]{Items: []Client{}}, nil
}
func (f *fakeBackend) GetClient(_ context.Context, fp string) (Client, error) {
	return Client{Fingerprint: fp}, nil
}
func (f *fakeBackend) UpdateClientNote(_ context.Context, fp, note, reason string) error {
	f.noteFP = fp
	f.note = note
	f.noteReason = reason
	return nil
}
func (f *fakeBackend) BlockClient(_ context.Context, fp string, _ BlockRequest) error {
	f.blockedFP = fp
	return nil
}
func (f *fakeBackend) UnblockClient(context.Context, string, string) error { return nil }
func (f *fakeBackend) ListAuditEvents(context.Context, ListQuery) (Page[AuditEvent], error) {
	return Page[AuditEvent]{Items: f.audit}, nil
}
func (f *fakeBackend) ListAdminActions(context.Context, ListQuery) (Page[AdminAction], error) {
	return Page[AdminAction]{Items: []AdminAction{}}, nil
}

func newTestServer(t *testing.T, f *fakeBackend) *Server {
	t.Helper()
	s, e := New(f, "secret")
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func request(s http.Handler, method, target, body, token, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAuthenticationAndResponseHeaders(t *testing.T) {
	s := newTestServer(t, &fakeBackend{})
	for _, token := range []string{"", "wrong"} {
		w := request(s, http.MethodGet, "/api/v1/overview", "", token, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status %d", token, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
			t.Fatalf("missing API headers: %v", w.Header())
		}
	}
	w := request(s, http.MethodGet, "/api/v1/overview", "", "secret", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"online_users":2`) {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestJSONValidationAndDisconnect(t *testing.T) {
	f := &fakeBackend{}
	s := newTestServer(t, f)
	w := request(s, http.MethodPost, "/api/v1/sessions/s1/disconnect", `{"reason":"  maintenance  "}`, "secret", "application/json; charset=utf-8")
	if w.Code != http.StatusNoContent || f.disconnectID != "s1" || f.reason != "maintenance" {
		t.Fatalf("status=%d id=%q reason=%q", w.Code, f.disconnectID, f.reason)
	}
	for _, tc := range []struct {
		body, ct string
		status   int
	}{{`{"reason":" "}`, "application/json", 400}, {`{"reason":"ok","extra":1}`, "application/json", 400}, {`{"reason":"ok"}`, "text/plain", 415}} {
		w = request(s, http.MethodPost, "/api/v1/sessions/s1/disconnect", tc.body, "secret", tc.ct)
		if w.Code != tc.status {
			t.Fatalf("body %s: got %d want %d", tc.body, w.Code, tc.status)
		}
	}
	large := `{"reason":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	w = request(s, http.MethodPost, "/api/v1/sessions/s1/disconnect", large, "secret", "application/json")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestEncodedFingerprintAndLimits(t *testing.T) {
	f := &fakeBackend{}
	s := newTestServer(t, f)
	fp := "SHA256:" + strings.Repeat("A", 20) + "/" + strings.Repeat("B", 22)
	w := request(s, http.MethodPatch, "/api/v1/clients/"+url.PathEscape(fp), `{"note":" desk ","reason":" inventory "}`, "secret", "application/json")
	if w.Code != http.StatusNoContent || f.noteFP != fp || f.note != "desk" || f.noteReason != "inventory" {
		t.Fatalf("status=%d fp=%q note=%q reason=%q body=%s", w.Code, f.noteFP, f.note, f.noteReason, w.Body.String())
	}
	w = request(s, http.MethodGet, "/api/v1/clients?limit=201", "", "secret", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("limit status %d", w.Code)
	}
	w = request(s, http.MethodGet, "/api/v1/clients/not-a-fingerprint", "", "secret", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("fingerprint status %d", w.Code)
	}
}

func TestCursorRoundTripAndRejectsTrailingData(t *testing.T) {
	want := Cursor{Time: time.Unix(1700000000, 123456000).UTC(), ID: 42}
	got, e := DecodeCursor(EncodeCursor(want))
	if e != nil || !got.Time.Equal(want.Time) || got.ID != want.ID {
		t.Fatalf("roundtrip got=%+v err=%v", got, e)
	}
	if _, e = DecodeCursor("e30"); e == nil {
		t.Fatal("accepted incomplete cursor")
	}
}

func TestCSVFormulaInjectionProtection(t *testing.T) {
	f := &fakeBackend{audit: []AuditEvent{{ID: 1, Time: time.Unix(1, 0), Kind: "connection", ClientID: "=cmd|' /C calc'!A0", Reason: " @formula", Result: "ok"}}}
	s := newTestServer(t, f)
	w := request(s, http.MethodGet, "/api/v1/audit/events?format=csv", "", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"'=cmd|' /C calc'!A0"`) || !strings.Contains(body, `"' @formula"`) {
		t.Fatalf("unsafe/unexpected csv: %s", body)
	}
}

func TestStaticFallbackAndAPINeverFallsBack(t *testing.T) {
	s := newTestServer(t, &fakeBackend{})
	w := request(s, http.MethodGet, "/clients/detail", "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("spa response %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("static headers: %v", w.Header())
	}
	w = request(s, http.MethodGet, "/api/missing", "", "secret", "")
	if w.Code != 404 || strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("api fallback: %d %s", w.Code, w.Body.String())
	}
}

func TestSSEPublishAndClose(t *testing.T) {
	s := newTestServer(t, &fakeBackend{})
	s.heartbeat = time.Hour
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	connected, e := reader.ReadString('\n')
	if e != nil || !strings.Contains(connected, ": connected") {
		t.Fatalf("initial SSE: %q err=%v", connected, e)
	}
	_, _ = reader.ReadString('\n')
	if !s.Publish("sessions.changed", map[string]int{"revision": 2}) {
		t.Fatal("publish failed")
	}
	eventLine, e := reader.ReadString('\n')
	if e != nil {
		t.Fatal(e)
	}
	dataLine, e := reader.ReadString('\n')
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(eventLine, "event: sessions.changed") || !strings.Contains(dataLine, `"revision":2`) {
		t.Fatalf("event SSE: %q %q", eventLine, dataLine)
	}
	_ = s.Close()
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	h := newEventHub()
	ch, _, ok := h.subscribe()
	if !ok {
		t.Fatal("subscribe")
	}
	for i := 0; i < 17; i++ {
		h.publish(Event{Type: "changed", Data: i})
	}
	for range ch {
	}
	h.mu.Lock()
	n := len(h.subs)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("slow subscriber retained: %d", n)
	}
}
