package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
)

func TestHandshakeMonitorDetectsSourceAcrossPortsAndFlushesTail(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newHandshakeMonitor(16, time.Minute, 3)
	a := handshakeAttempt{Remote: "198.51.100.1:1000", Result: "failed", Reason: "timeout"}
	first := m.record(now, a)
	if first == nil || first.suspicious || first.failures != 1 {
		t.Fatalf("single timeout must not be labelled malicious: %+v", first)
	}
	a.Remote = "198.51.100.1:2000"
	if e := m.record(now.Add(time.Second), a); e != nil {
		t.Fatalf("repeated source should be sampled: %+v", e)
	}
	a.Remote = "198.51.100.1:3000"
	alert := m.record(now.Add(2*time.Second), a)
	if alert == nil || alert.event != "alert" || !alert.suspicious || alert.failures != 3 || alert.suppressed != 1 {
		t.Fatalf("threshold should bypass source sampling: %+v", alert)
	}
	a.Remote = "198.51.100.2:1000"
	if e := m.record(now.Add(2*time.Second), a); e == nil || e.suspicious || e.failures != 1 {
		t.Fatalf("another IP inherited suspicion: %+v", e)
	}
	a.Remote = "198.51.100.1:4000"
	if e := m.record(now.Add(3*time.Second), a); e != nil {
		t.Fatalf("post-alert sample should be suppressed: %+v", e)
	}
	tail := m.flush(now.Add(8*time.Second), false)
	if len(tail) != 1 || tail[0].event != "summary" || tail[0].suppressed != 1 || tail[0].failures != 4 || tail[0].attempt.Remote != a.Remote {
		t.Fatalf("quiet source's final suppressed event was lost: %+v", tail)
	}
	if tail = m.flush(now.Add(9*time.Second), false); len(tail) != 0 {
		t.Fatalf("summary repeated without new events: %+v", tail)
	}
	e := m.record(now.Add(time.Minute), a)
	if e == nil || e.suspicious || e.failures != 1 {
		t.Fatalf("expired failure window must reset suspicion: %+v", e)
	}
}

func TestHandshakeMonitorDoesNotBlamePeersForServerCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newHandshakeMonitor(8, time.Minute, 1)
	for i, reason := range []string{"connection_limit", "handshake_limit", "global_rate_limit", "source_tracking_limit", "setup_failed", "server_shutdown"} {
		a := handshakeAttempt{Remote: fmt.Sprintf("198.51.100.%d:1234", i+1), Result: "rejected", Reason: reason}
		if e := m.record(now, a); e == nil || e.suspicious || e.failures != 0 {
			t.Fatalf("server-side condition %q blamed peer: %+v", reason, e)
		}
	}
	for i, reason := range []string{"source_handshake_limit", "source_rate_limit"} {
		a := handshakeAttempt{Remote: fmt.Sprintf("203.0.113.%d:1234", i+1), Result: "rejected", Reason: reason}
		if e := m.record(now, a); e == nil || !e.suspicious || e.failures != 1 {
			t.Fatalf("source abuse not counted: %+v", e)
		}
	}
}

func TestHandshakeMonitorBoundsFloodAndPreservesDelayedAlert(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newHandshakeMonitor(32, time.Minute, 2)
	for i := 0; i < handshakeLogBudget; i++ {
		a := handshakeAttempt{Remote: fmt.Sprintf("198.51.100.%d:1234", i+1), Result: "failed", Reason: "peer_closed"}
		if e := m.record(now, a); e == nil {
			t.Fatal("initial detail budget unexpectedly exhausted")
		}
	}
	a := handshakeAttempt{Remote: "203.0.113.1:1234", Result: "failed", Reason: "timeout"}
	for i := 0; i < 2; i++ {
		if e := m.record(now, a); e != nil {
			t.Fatalf("distributed flood exceeded global detail budget: %+v", e)
		}
	}
	// A new window and a successful connection must not overwrite an alert
	// already waiting for output. Its original evidence is preserved.
	m.budget, m.reset = 0, now.Add(2*time.Minute)
	a.Result, a.Reason = "success", "authenticated"
	if e := m.record(now.Add(time.Minute), a); e != nil {
		t.Fatalf("exhausted budget emitted a detail: %+v", e)
	}
	entries := m.flush(now.Add(2*time.Minute), false)
	if len(entries) != 1 || entries[0].event != "alert" || entries[0].attempt.Reason != "timeout" || entries[0].failures != 2 || !entries[0].windowStart.Equal(now) || entries[0].suppressed != 3 {
		t.Fatalf("deferred alert evidence was lost: %+v", entries)
	}
	for i := 0; i < 100; i++ {
		m.record(now.Add(2*time.Minute), handshakeAttempt{Remote: fmt.Sprintf("192.0.2.%d:1234", i+1), Result: "failed", Reason: "peer_closed"})
	}
	if len(m.sources) > 32 || m.untracked == 0 {
		t.Fatalf("source table is unbounded or overflow is invisible: sources=%d untracked=%d", len(m.sources), m.untracked)
	}
	entries = m.flush(now.Add(2*time.Minute), true)
	var overflow bool
	for _, e := range entries {
		overflow = overflow || e.event == "overflow" && e.untracked > 0
	}
	if !overflow {
		t.Fatal("source overflow missing from shutdown summary")
	}
	m.flush(now.Add(4*time.Minute), false)
	if len(m.sources) != 0 {
		t.Fatalf("inactive sources were not reclaimed: %d", len(m.sources))
	}
}

func TestHandshakeLogEscapesAndBoundsUntrustedFields(t *testing.T) {
	m := newHandshakeMonitor(1, time.Minute, 10)
	now := time.Unix(1000, 0)
	a := handshakeAttempt{Remote: "198.51.100.1:1234", Result: "failed", Reason: "protocol_error", User: "root\nforged=true\x1b[31m", Error: strings.Repeat("x", 10000)}
	e := m.record(now, a)
	if len(m.sources["198.51.100.1"].latest.Error) > maxHandshakeLogText+3 {
		t.Fatal("untrusted error retained without a memory bound")
	}
	var line string
	e.write(func(f string, args ...any) { line = fmt.Sprintf(f, args...) })
	if strings.ContainsAny(line, "\n\r\x1b") || !strings.Contains(line, `root\nforged=true\x1b[31m`) || strings.Contains(line, strings.Repeat("x", 600)) {
		t.Fatalf("log injection or unbounded error: %q", line)
	}
}

func TestHandshakeMonitorPrioritizesQueuedAlertsOverNewTraffic(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newHandshakeMonitor(32, time.Minute, 2)
	for i := 0; i < handshakeLogBudget; i++ {
		m.record(now, handshakeAttempt{Remote: fmt.Sprintf("198.51.100.%d:1234", i+1), Result: "success", Reason: "authenticated"})
	}
	for i := 0; i < 2; i++ {
		m.record(now, handshakeAttempt{Remote: "203.0.113.1:1234", Result: "failed", Reason: "timeout"})
	}
	// A continuous flood arriving ahead of the timer must not take its refill.
	if e := m.record(now.Add(6*time.Second), handshakeAttempt{Remote: "192.0.2.1:1234", Result: "success", Reason: "authenticated"}); e != nil {
		t.Fatalf("new traffic consumed budget ahead of a pending alert: %+v", e)
	}
	entries := m.flush(now.Add(6*time.Second), false)
	if len(entries) == 0 || entries[0].event != "alert" || entries[0].attempt.Remote != "203.0.113.1:1234" {
		t.Fatalf("pending alert did not get priority: %+v", entries)
	}
}

func TestHandshakeMonitorConcurrentTraffic(t *testing.T) {
	m := newHandshakeMonitor(16, time.Minute, 10)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				m.record(time.Now(), handshakeAttempt{Remote: fmt.Sprintf("198.51.100.%d:%d", worker+1, 1000+i), Result: "failed", Reason: "timeout"})
				if i%10 == 0 {
					m.flush(time.Now(), false)
				}
			}
		}(worker)
	}
	workers.Wait()
	if len(m.sources) != 8 {
		t.Fatalf("concurrent sources=%d want=8", len(m.sources))
	}
	for ip, source := range m.sources {
		if source.failures != 100 {
			t.Errorf("%s lost concurrent outcomes: %d", ip, source.failures)
		}
	}
}

type handshakeLogCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *handshakeLogCapture) log(f string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(f, args...))
}

func (c *handshakeLogCapture) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// SSH can write on both sides before reading. One io.Copy buffer provides
// bounded read-ahead over net.Pipe, without opening any network sockets.
type handshakeTestConn struct {
	net.Conn
	reader *io.PipeReader
}

func bufferedHandshakeConn(raw net.Conn) net.Conn {
	reader, writer := io.Pipe()
	go func() {
		_, err := io.Copy(writer, raw)
		_ = writer.CloseWithError(err)
	}()
	return handshakeTestConn{raw, reader}
}

func (c handshakeTestConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c handshakeTestConn) Close() error {
	_ = c.reader.Close()
	return c.Conn.Close()
}

func TestHandshakeLogsConnectionsAndCountsEachRejectionOnce(t *testing.T) {
	for _, kind := range []string{"timeout", "unauthorized", "blocked", "success", "protocol"} {
		t.Run(kind, func(t *testing.T) {
			cfg := managementConfig(t)
			cfg.Addr, cfg.AdminAddr, cfg.HandshakeTimeout = "127.0.0.1:0", "", 500*time.Millisecond
			logs := new(handshakeLogCapture)
			s, err := New(cfg, logs.log)
			if err != nil {
				t.Fatal(err)
			}
			serverPipe, clientPipe := net.Pipe()
			serverRaw, raw := bufferedHandshakeConn(serverPipe), bufferedHandshakeConn(clientPipe)
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.handleConn(sourceConn{serverRaw, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}})
			}()
			t.Cleanup(func() {
				_ = raw.Close()
				_ = serverRaw.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("handshake handler did not stop")
				}
				_ = s.Close()
			})
			keyPath := filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")
			if kind == "unauthorized" {
				keyPath = filepath.Join(t.TempDir(), "other_key")
				genKey(t, keyPath)
			}
			signer := loadSigner(t, keyPath)
			fp := ssh.FingerprintSHA256(signer.PublicKey())
			if kind == "blocked" {
				if err := (*adminBackend)(s).BlockClient(context.Background(), fp, adminapi.BlockRequest{Reason: "handshake diagnostics test"}); err != nil {
					t.Fatal(err)
				}
			}
			_ = raw.SetDeadline(time.Now().Add(2 * time.Second))
			want := ""
			switch kind {
			case "timeout", "protocol":
				reader := bufio.NewReader(raw)
				if _, err := reader.ReadString('\n'); err != nil {
					t.Fatal(err)
				}
				want = "reason=timeout"
				if kind == "protocol" {
					_, _ = io.WriteString(raw, strings.Repeat("X", 300))
					want = "reason=protocol_error"
				}
				_, _ = io.Copy(io.Discard, reader)
			default:
				client, _, _, err := ssh.NewClientConn(raw, "in-memory", &ssh.ClientConfig{User: "diagnostic-user", ClientVersion: "SSH-2.0-log-test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
				if kind == "success" {
					if err != nil {
						t.Fatal(err)
					}
					_ = client.Close()
					want = "reason=authenticated"
				} else {
					if err == nil {
						client.Close()
						t.Fatal("untrusted key accepted")
					}
					want = "reason=unauthorized_key"
					if kind == "blocked" {
						want = "reason=blocked_key"
					}
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for !strings.Contains(logs.joined(), want) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			text := logs.joined()
			if !strings.Contains(text, want) || !strings.Contains(text, `remote="127.0.0.1:`) || !strings.Contains(text, "suspicious=false") {
				t.Fatalf("missing handshake diagnostics: %s", text)
			}
			if kind == "success" || kind == "unauthorized" || kind == "blocked" {
				for _, field := range []string{`client_version="SSH-2.0-log-test"`, `user="diagnostic-user"`, `fingerprint="` + fp + `"`, `method="publickey"`} {
					if !strings.Contains(text, field) {
						t.Errorf("missing %s in %s", field, text)
					}
				}
			}
			n, _, err := s.store.TodayRejected(context.Background(), time.Now())
			wantRejected := int64(1)
			if kind == "success" {
				wantRejected = 0
			}
			if err != nil || n != wantRejected {
				t.Fatalf("rejection counted more than once: count=%d want=%d err=%v", n, wantRejected, err)
			}
		})
	}
}

func TestHandshakeAdmissionRejectionReasons(t *testing.T) {
	for _, reason := range []string{"connection_limit", "handshake_limit", "global_rate_limit", "source_tracking_limit", "source_handshake_limit", "source_rate_limit"} {
		t.Run(reason, func(t *testing.T) {
			limits := Limits{}
			limits.defaults()
			a := newAdmission(limits)
			switch reason {
			case "connection_limit":
				a.active = limits.MaxConnections
			case "handshake_limit":
				a.handshakes = limits.MaxHandshakes
			case "global_rate_limit":
				a.global = newBucket(0, 0)
			case "source_tracking_limit":
				a.limits.MaxSources = 1
				a.sources["192.0.2.1"] = &sourceBudget{last: time.Now()}
			case "source_handshake_limit":
				a.sources["198.51.100.1"] = &sourceBudget{active: limits.MaxHandshakesPerIP, last: time.Now()}
			case "source_rate_limit":
				a.sources["198.51.100.1"] = &sourceBudget{bucket: newBucket(0, 0), last: time.Now()}
			}
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			finish, release, got := a.acceptWithReason(sourceConn{local, &net.TCPAddr{IP: net.IPv4(198, 51, 100, 1), Port: 1234}})
			if got != reason || finish != nil || release != nil {
				t.Fatalf("rejection reason=%q want=%q", got, reason)
			}
		})
	}
}
