package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

type trafficTestBackend struct{ fakeBackend }

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
