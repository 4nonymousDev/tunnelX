package server

import (
	"context"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"tunnelx/internal/adminapi"
	"tunnelx/internal/session"
)

type trafficPairKey struct{ exporter, importer string }
type trafficPair struct {
	exporter, importer adminapi.TrafficDevice
	bytes              atomic.Int64
	previous           int64
	active             int
}
type trafficTotalKey struct{ role, id string }
type trafficMeter struct {
	mu               sync.Mutex
	pairs            map[trafficPairKey]*trafficPair
	totals           map[trafficTotalKey]adminapi.TrafficTotal
	since, sampledAt time.Time
	links            []adminapi.TrafficLink
}

func newTrafficMeter(now time.Time) *trafficMeter {
	return &trafficMeter{pairs: make(map[trafficPairKey]*trafficPair), totals: make(map[trafficTotalKey]adminapi.TrafficTotal), since: now, sampledAt: now, links: []adminapi.TrafficLink{}}
}

func trafficDevice(s session.Session) adminapi.TrafficDevice {
	id := s.ClientID
	if id == "" {
		id = s.Fingerprint
	}
	name := s.Name
	if name == "" {
		name = id
	}
	return adminapi.TrafficDevice{ID: id, Name: name}
}

func (m *trafficMeter) begin(exporter, importer session.Session) (*trafficPair, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, i := trafficDevice(exporter), trafficDevice(importer)
	key := trafficPairKey{e.ID, i.ID}
	p := m.pairs[key]
	if p == nil {
		p = &trafficPair{}
		m.pairs[key] = p
	}
	p.exporter, p.importer = e, i
	p.active++
	return p, func() { m.mu.Lock(); p.active--; m.mu.Unlock() }
}

// Only the actual written bytes count, including a partial write with an error.
// This wrapper deliberately exposes no ReaderFrom method to bypass Write.
type trafficWriter struct {
	writer  io.Writer
	counter *atomic.Int64
}

func (w trafficWriter) Write(b []byte) (int, error) {
	n, err := w.writer.Write(b)
	w.counter.Add(int64(n))
	return n, err
}

func (m *trafficMeter) sample(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	elapsed := now.Sub(m.sampledAt).Seconds()
	if elapsed < 0 {
		return
	}
	links := make([]adminapi.TrafficLink, 0, len(m.pairs))
	for key, p := range m.pairs {
		current := p.bytes.Load()
		delta := current - p.previous
		p.previous = current
		for role, device := range map[string]adminapi.TrafficDevice{"exporter": p.exporter, "importer": p.importer} {
			k := trafficTotalKey{role, device.ID}
			v := m.totals[k]
			if delta != 0 || v.Name != device.Name {
				v.TrafficDevice = device
				v.Bytes += delta
				m.totals[k] = v
			}
		}
		if p.active > 0 || delta > 0 {
			// A fast shutdown can share the previous clock reading. Still account
			// for the last bytes without dividing by zero or losing the totals.
			rate := float64(0)
			if elapsed > 0 {
				rate = float64(delta) / elapsed
			}
			links = append(links, adminapi.TrafficLink{Exporter: p.exporter, Importer: p.importer, BytesPerSecond: rate, Connections: p.active})
		}
		if p.active == 0 {
			delete(m.pairs, key)
		}
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Exporter.ID != links[j].Exporter.ID {
			return links[i].Exporter.ID < links[j].Exporter.ID
		}
		return links[i].Importer.ID < links[j].Importer.ID
	})
	m.links, m.sampledAt = links, now
}

func (m *trafficMeter) snapshot() adminapi.TrafficSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := adminapi.TrafficSnapshot{SampledAt: m.sampledAt, Since: m.since, Links: append([]adminapi.TrafficLink{}, m.links...), Exporters: []adminapi.TrafficTotal{}, Importers: []adminapi.TrafficTotal{}}
	for key, total := range m.totals {
		if key.role == "exporter" {
			v.Exporters = append(v.Exporters, total)
		} else {
			v.Importers = append(v.Importers, total)
		}
	}
	for _, totals := range [][]adminapi.TrafficTotal{v.Exporters, v.Importers} {
		sort.Slice(totals, func(i, j int) bool {
			if totals[i].Bytes == totals[j].Bytes {
				return totals[i].ID < totals[j].ID
			}
			return totals[i].Bytes > totals[j].Bytes
		})
	}
	return v
}

func (b *adminBackend) Traffic(ctx context.Context) (adminapi.TrafficSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return adminapi.TrafficSnapshot{}, err
	}
	return (*Server)(b).traffic.snapshot(), nil
}

func (s *Server) runTraffic() {
	defer close(s.trafficDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.traffic.sample(time.Now())
		case <-s.trafficStop:
			s.traffic.sample(time.Now())
			return
		}
	}
}
