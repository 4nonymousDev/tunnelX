package adminapi

import (
	"context"
	"time"
)

// TrafficBackend is optional for embedders that only implement management.
type TrafficBackend interface {
	Traffic(context.Context) (TrafficSnapshot, error)
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
	SampledAt time.Time      `json:"sampled_at"`
	Since     time.Time      `json:"since"`
	Links     []TrafficLink  `json:"links"`
	Exporters []TrafficTotal `json:"exporters"`
	Importers []TrafficTotal `json:"importers"`
}
