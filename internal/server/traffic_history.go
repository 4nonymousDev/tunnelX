package server

import (
	"context"
	"errors"
	"time"
	"tunnelx/internal/adminapi"
)

func (b *adminBackend) TrafficHistory(ctx context.Context, from, to time.Time) (adminapi.TrafficHistory, error) {
	s := (*Server)(b)
	v := adminapi.TrafficHistory{From: from, To: to, Exporters: []adminapi.TrafficTotal{}, Importers: []adminapi.TrafficTotal{}}
	if s.store == nil {
		return v, errors.New("traffic history requires a data directory")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	totals, saved, err := s.store.TrafficHistory(queryCtx, from, to)
	if err != nil {
		return v, err
	}
	v.PersistedAt = saved
	for _, total := range totals {
		item := adminapi.TrafficTotal{TrafficDevice: adminapi.TrafficDevice{ID: total.ClientID, Name: total.Name}, Bytes: total.Bytes}
		if total.Role == "exporter" {
			v.Exporters = append(v.Exporters, item)
		} else {
			v.Importers = append(v.Importers, item)
		}
	}
	return v, nil
}
