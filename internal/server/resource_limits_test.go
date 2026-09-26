package server

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"tunnelx/internal/proto"
)

func waitAdmissionCounts(t *testing.T, a *admission, active, handshakes int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		gotActive, gotHandshakes := a.active, a.handshakes
		a.mu.Unlock()
		if gotActive == active && gotHandshakes == handshakes {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("admission active/handshakes = %d/%d, want %d/%d", gotActive, gotHandshakes, active, handshakes)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHandshakeDeadlineReleasesAdmission(t *testing.T) {
	srv, addr, keyPath := startServerInstance(t, func(c *Config) {
		c.HandshakeTimeout = 100 * time.Millisecond
		c.Limits.MaxConnections = 1
		c.Limits.MaxHandshakes = 1
		c.Limits.MaxHandshakesPerIP = 1
	})
	waitAdmissionCounts(t, srv.admission, 0, 0) // Readiness probe has finished.
	raw, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetReadDeadline(time.Now().Add(time.Second))
	reader := bufio.NewReader(raw)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "SSH-") {
		t.Fatalf("server banner %q: %v", line, err)
	}
	// The peer deliberately never sends its own SSH version or credentials.
	_, err = io.Copy(io.Discard, reader)
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("half-open handshake outlived the server deadline")
	}
	waitAdmissionCounts(t, srv.admission, 0, 0)
	client := dialWith(t, addr, keyPath)
	_, control := openTestControl(t, client, proto.RoleImporter, "cross-importer")
	defer control.Close()
	waitAdmissionCounts(t, srv.admission, 1, 0)
	_ = client.Close()
	waitAdmissionCounts(t, srv.admission, 0, 0)
}

type sourceConn struct {
	net.Conn
	remote net.Addr
}

func (c sourceConn) RemoteAddr() net.Addr { return c.remote }

func TestConcurrentAdmissionCannotExceedCaps(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		connections, handshakes, perIP, expected int
		sameIP                                   bool
	}{
		{"connections", 2, 4, 4, 2, false},
		{"handshakes", 4, 2, 4, 2, false},
		{"source", 4, 4, 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := Limits{MaxConnections: tc.connections, MaxHandshakes: tc.handshakes, MaxHandshakesPerIP: tc.perIP, MaxSources: 64}
			a := newAdmission(limits)
			var workers sync.WaitGroup
			var mu sync.Mutex
			var finishes, releases []func()
			start := make(chan struct{})
			for i := 0; i < 32; i++ {
				workers.Add(1)
				go func(i int) {
					defer workers.Done()
					local, peer := net.Pipe()
					defer local.Close()
					defer peer.Close()
					ip := i + 1
					if tc.sameIP {
						ip = 1
					}
					conn := sourceConn{local, &net.TCPAddr{IP: net.IPv4(198, 51, 100, byte(ip)), Port: 10000 + i}}
					<-start
					finish, release, accepted := a.accept(conn)
					if accepted {
						mu.Lock()
						finishes = append(finishes, finish)
						releases = append(releases, release)
						mu.Unlock()
					}
				}(i)
			}
			close(start)
			workers.Wait()
			if len(releases) != tc.expected {
				t.Fatalf("concurrent accepted = %d, want %d", len(releases), tc.expected)
			}
			waitAdmissionCounts(t, a, tc.expected, tc.expected)
			for i := range releases {
				workers.Add(2)
				go func(i int) { defer workers.Done(); finishes[i](); releases[i]() }(i)
				go func(i int) { defer workers.Done(); releases[i](); finishes[i]() }(i)
			}
			workers.Wait()
			waitAdmissionCounts(t, a, 0, 0)
			a.mu.Lock()
			defer a.mu.Unlock()
			for ip, source := range a.sources {
				if source.active != 0 {
					t.Errorf("source %s leaked %d handshakes", ip, source.active)
				}
			}
		})
	}
}
