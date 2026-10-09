package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// TrafficBackend is optional for embedders that only implement management.
type TrafficBackend interface {
	Traffic(context.Context) (TrafficSnapshot, error)
}

type TrafficHistoryBackend interface {
	TrafficHistory(context.Context, time.Time, time.Time) (TrafficHistory, error)
}

type TrafficHistory struct {
	From        time.Time      `json:"from"`
	To          time.Time      `json:"to"`
	PersistedAt time.Time      `json:"persisted_at"`
	Exporters   []TrafficTotal `json:"exporters"`
	Importers   []TrafficTotal `json:"importers"`
}

func (s *Server) serveTrafficHistory(w http.ResponseWriter, r *http.Request, rid string) {
	if !allow(w, r, rid, http.MethodGet) {
		return
	}
	backend, ok := s.backend.(TrafficHistoryBackend)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported", "traffic history is unavailable", rid)
		return
	}
	from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, endErr := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil || endErr != nil || !from.Before(to) || from.Year() < 1970 || to.Year() > 9999 || !from.Equal(from.Truncate(15*time.Minute)) || !to.Equal(to.Truncate(15*time.Minute)) {
		bad(w, rid, fmt.Errorf("from and to must be RFC3339 times on 15-minute boundaries, with from before to"))
		return
	}
	value, err := backend.TrafficHistory(r.Context(), from, to)
	s.respond(w, rid, value, err)
}

type TrafficDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TrafficLink struct {
	Exporter       TrafficDevice `json:"exporter"`
	Importer       TrafficDevice `json:"importer"`
	BytesPerSecond float64       `json:"bytes_per_second"`
	Connections    int           `json:"connections"`
}

type TrafficTotal struct {
	TrafficDevice
	Bytes int64 `json:"bytes"`
}

type TrafficSnapshot struct {
	Persistent         bool           `json:"persistent"`
	PersistenceHealthy bool           `json:"persistence_healthy"`
	PersistedAt        *time.Time     `json:"persisted_at"`
	SampledAt          time.Time      `json:"sampled_at"`
	Since              time.Time      `json:"since"`
	Links              []TrafficLink  `json:"links"`
	Exporters          []TrafficTotal `json:"exporters"`
	Importers          []TrafficTotal `json:"importers"`
}
