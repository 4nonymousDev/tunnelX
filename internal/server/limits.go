package server

import (
	"net"
	"sync"
	"time"
	"tunnelx/internal/session"
)

// Limits are finite budgets, configurable without changing client data.
type Limits struct {
	MaxConnections, MaxHandshakes, MaxHandshakesPerIP, MaxSources int
	SessionsPerKey, ExportsPerSession, ExportsTotal               int
	ChannelsPerSession, ChannelsTotal                             int
	PublishBytes, RegistryBytes                                   int
}

func (l *Limits) defaults() {
	values := []struct {
		p *int
		v int
	}{
		{&l.MaxConnections, 64}, {&l.MaxHandshakes, 16}, {&l.MaxHandshakesPerIP, 4}, {&l.MaxSources, 4096},
		{&l.SessionsPerKey, 8}, {&l.ExportsPerSession, 64}, {&l.ExportsTotal, 1024},
		{&l.ChannelsPerSession, 32}, {&l.ChannelsTotal, 256},
		{&l.PublishBytes, 128 << 10}, {&l.RegistryBytes, 1 << 20},
	}
	for _, v := range values {
		if *v.p <= 0 {
			*v.p = v.v
		}
	}
}
func (l Limits) sessions() session.Limits {
	return session.Limits{Sessions: l.MaxConnections, PerFingerprint: l.SessionsPerKey, ExportsPerSession: l.ExportsPerSession, ExportsTotal: l.ExportsTotal, ChannelsPerSession: l.ChannelsPerSession, ChannelsTotal: l.ChannelsTotal, RegistryBytes: l.RegistryBytes}
}

type rateBucket struct {
	mu          sync.Mutex
	tokens      float64
	last        time.Time
	rate, burst float64
}

func newBucket(rate, burst float64) *rateBucket {
	return &rateBucket{tokens: burst, last: time.Now(), rate: rate, burst: burst}
}
func (b *rateBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type sourceBudget struct {
	active int
	bucket *rateBucket
	last   time.Time
}
type admission struct {
	mu                 sync.Mutex
	limits             Limits
	active, handshakes int
	sources            map[string]*sourceBudget
	lastSweep          time.Time
	global             *rateBucket
}

func newAdmission(l Limits) *admission {
	return &admission{limits: l, sources: map[string]*sourceBudget{}, global: newBucket(20, 40)}
}
func (a *admission) accept(raw net.Conn) (finish, release func(), ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active >= a.limits.MaxConnections || a.handshakes >= a.limits.MaxHandshakes || !a.global.allow() {
		return nil, nil, false
	}
	now := time.Now()
	if now.Sub(a.lastSweep) >= time.Minute {
		for ip, s := range a.sources {
			if s.active == 0 && now.Sub(s.last) > time.Minute {
				delete(a.sources, ip)
			}
		}
		a.lastSweep = now
	}
	ip := remoteHost(raw.RemoteAddr())
	source := a.sources[ip]
	if source == nil {
		if len(a.sources) >= a.limits.MaxSources {
			return nil, nil, false
		}
		source = &sourceBudget{bucket: newBucket(4, 8)}
		a.sources[ip] = source
	}
	source.last = now
	if source.active >= a.limits.MaxHandshakesPerIP || !source.bucket.allow() {
		return nil, nil, false
	}
	a.active++
	a.handshakes++
	source.active++
	var hsOnce, allOnce sync.Once
	finish = func() {
		hsOnce.Do(func() { a.mu.Lock(); a.handshakes--; source.active--; source.last = time.Now(); a.mu.Unlock() })
	}
	release = func() { allOnce.Do(func() { finish(); a.mu.Lock(); a.active--; a.mu.Unlock() }) }
	return finish, release, true
}
