package server

import (
	"testing"
	"time"
)

// Start on port zero and wait for the actual listener. Readiness must not add
// synthetic connections to tests which assert authentication/audit counts.
func startTestListener(t *testing.T, s *Server) (string, func()) {
	t.Helper()
	done := make(chan struct{})
	var serveErr error
	go func() { defer close(done); serveErr = s.ListenAndServe() }()
	stop := func() {
		_ = s.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server shutdown timed out")
		}
	}
	t.Cleanup(stop)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		addr := ""
		if s.listener != nil {
			addr = s.listener.Addr().String()
		}
		s.mu.Unlock()
		if addr != "" {
			return addr, stop
		}
		select {
		case <-done:
			t.Fatalf("server exited before listening: %v", serveErr)
		case <-deadline.C:
			t.Fatal("server did not start listening")
		case <-tick.C:
		}
	}
}
