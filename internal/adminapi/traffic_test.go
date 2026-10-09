package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

type trafficTestBackend struct{ fakeBackend }

func (*trafficTestBackend) TrafficHistory(_ context.Context, from, to time.Time) (TrafficHistory, error) {
	return TrafficHistory{From: from, To: to, PersistedAt: from, Exporters: []TrafficTotal{}, Importers: []TrafficTotal{}}, nil
}

func TestTrafficHistoryRangeValidation(t *testing.T) {
	s, err := New(&trafficTestBackend{}, &testAuthBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := "/api/v1/traffic/history"
	if w := request(s, http.MethodGet, path, "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	if w := request(s, http.MethodPost, path, "", "secret", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", w.Code)
	}
	for _, query := range []string{"", "?from=bad&to=bad", "?from=2026-10-09T00:00:00Z&to=2026-10-08T00:00:00Z", "?from=2026-10-09T00:01:00Z&to=2026-10-10T00:00:00Z"} {
		if w := request(s, http.MethodGet, path+query, "", "secret", ""); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid range %s: %d", query, w.Code)
		}
	}
	w := request(s, http.MethodGet, path+"?from=2026-10-09T00:00:00Z&to=2026-10-10T00:00:00Z", "", "secret", "")
	var v TrafficHistory
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.To.Sub(v.From) != 24*time.Hour || v.Exporters == nil || v.Importers == nil {
		t.Fatalf("history contract: %d %s", w.Code, w.Body.String())
	}
}

func (*trafficTestBackend) Traffic(context.Context) (TrafficSnapshot, error) {
	return TrafficSnapshot{Since: time.Unix(123, 0), SampledAt: time.Unix(125, 0), Links: []TrafficLink{{Exporter: TrafficDevice{ID: "e", Name: "Exporter"}, Importer: TrafficDevice{ID: "i", Name: "Importer"}, BytesPerSecond: 1024, Connections: 1}}, Exporters: []TrafficTotal{}, Importers: []TrafficTotal{}}, nil
}

func TestTrafficEndpointAuthenticationAndContract(t *testing.T) {
	s, err := New(&trafficTestBackend{}, &testAuthBackend{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if w := request(s, http.MethodGet, "/api/v1/traffic", "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated traffic: %d", w.Code)
	}
	if w := request(s, http.MethodPost, "/api/v1/traffic", "", "secret", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST traffic: %d", w.Code)
	}
	w := request(s, http.MethodGet, "/api/v1/traffic", "", "secret", "")
	var v TrafficSnapshot
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Links) != 1 || v.Links[0].BytesPerSecond != 1024 || v.Exporters == nil || v.Importers == nil {
		t.Fatalf("traffic contract: %d %s", w.Code, w.Body.String())
	}
}
