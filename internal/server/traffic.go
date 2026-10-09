package server

import (
	"context"
	"crypto/rand"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"tunnelx/internal/adminapi"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

type trafficPairKey struct{ exporter, importer string }
type trafficPair struct {
	exporter, importer adminapi.TrafficDevice
	bytes              atomic.Int64
	previous           int64
	active             int
}
type trafficTotalKey struct{ role, id string }
type trafficBucketKey struct {
	trafficTotalKey
	at time.Time
}
type trafficMeter struct {
	mu                             sync.Mutex
	pairs                          map[trafficPairKey]*trafficPair
	totals                         map[trafficTotalKey]adminapi.TrafficTotal
	since, sampledAt               time.Time
	links                          []adminapi.TrafficLink
	dirty                          map[trafficTotalKey]bool
	persistent, persistenceHealthy bool
	persistedAt                    *time.Time
	runID                          string
	buckets                        map[trafficBucketKey]int64
	dirtyBuckets                   map[trafficBucketKey]bool
}

func newTrafficMeter(now time.Time) *trafficMeter {
	return &trafficMeter{pairs: make(map[trafficPairKey]*trafficPair), totals: make(map[trafficTotalKey]adminapi.TrafficTotal), dirty: make(map[trafficTotalKey]bool), runID: rand.Text(), buckets: make(map[trafficBucketKey]int64), dirtyBuckets: make(map[trafficBucketKey]bool), persistenceHealthy: true, since: now, sampledAt: now, links: []adminapi.TrafficLink{}}
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
			if delta > 0 && m.persistent {
				bucket := trafficBucketKey{k, now.UTC().Truncate(store.TrafficBucketDuration)}
				m.buckets[bucket] += delta
				m.dirtyBuckets[bucket] = true
			}
			v := m.totals[k]
			if delta != 0 || v.Name != device.Name {
				v.TrafficDevice = device
				v.Bytes += delta
				m.totals[k] = v
				m.dirty[k] = true
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
	v.Persistent, v.PersistenceHealthy = m.persistent, m.persistenceHealthy
	if m.persistedAt != nil {
		saved := *m.persistedAt
		v.PersistedAt = &saved
	}
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

func (s *Server) flushTraffic() error {
	if s.store == nil {
		return nil
	}
	m := s.traffic
	m.mu.Lock()
	batch := make([]store.TrafficTotal, 0, len(m.dirty))
	for key := range m.dirty {
		v := m.totals[key]
		batch = append(batch, store.TrafficTotal{Role: key.role, ClientID: key.id, Name: v.Name, Bytes: v.Bytes})
	}
	at := m.sampledAt
	buckets := make([]store.TrafficBucket, 0, len(m.dirtyBuckets))
	for key := range m.dirtyBuckets {
		buckets = append(buckets, store.TrafficBucket{Role: key.role, ClientID: key.id, RunID: m.runID, At: key.at, Bytes: m.buckets[key]})
	}
	m.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	ctx, cancel := s.dbContext()
	err := s.store.SaveTraffic(ctx, batch, buckets, at)
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persistenceHealthy = err == nil
	if err == nil {
		m.persistedAt = &at
		for _, v := range batch {
			key := trafficTotalKey{v.Role, v.ClientID}
			if current := m.totals[key]; current.Bytes == v.Bytes && current.Name == v.Name {
				delete(m.dirty, key)
			}
		}
		for _, v := range buckets {
			key := trafficBucketKey{trafficTotalKey{v.Role, v.ClientID}, v.At}
			if m.buckets[key] == v.Bytes {
				delete(m.dirtyBuckets, key)
			}
		}
		for key := range m.buckets {
			if key.at.Before(m.sampledAt.UTC().Truncate(store.TrafficBucketDuration)) && !m.dirtyBuckets[key] {
				delete(m.buckets, key)
			}
		}
	}
	return err
}

// Separate disk writes from the one-second sampler so slow storage cannot
// block forwarding or live snapshots. A failed batch stays dirty for retry.
func (s *Server) runTrafficPersistence() {
	defer close(s.trafficSaveDone)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.flushTraffic(); err != nil {
				s.limitedLog("历史流量保存失败: %v", err)
			}
		case <-s.trafficStop:
			<-s.trafficDone
			s.trafficSaveErr = s.flushTraffic()
			return
		}
	}
}
