package sshkeepalive

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type probeReply struct {
	ok  bool
	err error
}

type probeSender struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	started   chan struct{}
	replies   chan probeReply
	closed    chan struct{}
	closeOnce sync.Once
}

func newProbeSender() *probeSender {
	return &probeSender{
		started: make(chan struct{}, 32),
		replies: make(chan probeReply, 32),
		closed:  make(chan struct{}),
	}
}

func (s *probeSender) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	if name != RequestType || !wantReply || payload != nil {
		return false, nil, errors.New("unexpected keepalive request")
	}
	s.mu.Lock()
	s.calls++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	s.started <- struct{}{}
	select {
	case reply := <-s.replies:
		return reply.ok, nil, reply.err
	case <-s.closed:
		return false, nil, io.EOF
	}
}

func (s *probeSender) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func startMonitor(s Sender, done <-chan struct{}, interval, timeout time.Duration) (<-chan error, <-chan struct{}) {
	failed := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		Run(s, done, interval, timeout, func(err error) {
			failed <- err
			_ = s.Close()
		})
	}()
	return failed, finished
}

func waitEvent(t *testing.T, event <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertJoined(t *testing.T, s *probeSender) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != 0 || s.maxActive > 1 {
		t.Fatalf("request worker not serialized/joined: active=%d, max=%d", s.active, s.maxActive)
	}
	select {
	case <-s.closed:
	default:
		t.Fatal("transport remains open")
	}
}

func TestTimeoutClosesAndJoinsOneBlockedProbe(t *testing.T) {
	s := newProbeSender()
	failed, finished := startMonitor(s, make(chan struct{}), 5*time.Millisecond, 60*time.Millisecond)
	waitEvent(t, s.started, "first probe")
	waitEvent(t, finished, "timeout cleanup")
	if err := <-failed; !errors.Is(err, ErrTimeout) {
		t.Fatalf("failure = %v, want timeout", err)
	}
	s.mu.Lock()
	calls := s.calls
	s.mu.Unlock()
	if calls != 1 {
		t.Fatalf("blocked probe spawned %d requests, want 1", calls)
	}
	assertJoined(t, s)
}

func TestDoneClosesAndJoinsBlockedProbe(t *testing.T) {
	s := newProbeSender()
	done := make(chan struct{})
	failed, finished := startMonitor(s, done, 5*time.Millisecond, time.Second)
	waitEvent(t, s.started, "first probe")
	close(done)
	waitEvent(t, finished, "cancel cleanup")
	select {
	case err := <-failed:
		t.Fatalf("cancellation reported failure: %v", err)
	default:
	}
	assertJoined(t, s)
}

func TestNegativeRepliesKeepConnectionAlive(t *testing.T) {
	s := newProbeSender()
	done := make(chan struct{})
	failed, finished := startMonitor(s, done, 10*time.Millisecond, 60*time.Millisecond)
	defer func() {
		close(done)
		waitEvent(t, finished, "healthy monitor cleanup")
	}()
	// Continue for several timeout periods. Unsupported requests produce a
	// negative SSH reply, which must reset the watchdog just like a positive one.
	for range 20 {
		waitEvent(t, s.started, "healthy probe")
		s.replies <- probeReply{ok: false}
	}
	select {
	case err := <-failed:
		t.Fatalf("negative SSH replies triggered failure: %v", err)
	default:
	}
}

func TestRequestErrorClosesTransport(t *testing.T) {
	s := newProbeSender()
	disconnected := errors.New("transport disconnected")
	s.replies <- probeReply{err: disconnected}
	failed, finished := startMonitor(s, make(chan struct{}), 5*time.Millisecond, time.Second)
	waitEvent(t, finished, "failed request cleanup")
	if err := <-failed; !errors.Is(err, disconnected) {
		t.Fatalf("failure = %v, want request error", err)
	}
	assertJoined(t, s)
}

func TestTimeoutStartsBeforeFirstProbe(t *testing.T) {
	s := newProbeSender()
	failed, finished := startMonitor(s, make(chan struct{}), time.Second, 10*time.Millisecond)
	waitEvent(t, finished, "initial watchdog timeout")
	if err := <-failed; !errors.Is(err, ErrTimeout) {
		t.Fatalf("failure = %v, want timeout", err)
	}
	s.mu.Lock()
	calls := s.calls
	s.mu.Unlock()
	if calls != 0 {
		t.Fatalf("requests = %d, want watchdog to expire before first probe", calls)
	}
	assertJoined(t, s)
}
