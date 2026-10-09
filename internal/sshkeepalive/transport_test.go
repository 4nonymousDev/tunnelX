package sshkeepalive

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// stalledWriteConn models a network write that cannot make progress. Its Close
// releases the blocked write, as a net.Conn must do, so the test exercises SSH's
// own SendRequest machinery instead of only a mock waiting for a reply.
type stalledWriteConn struct {
	net.Conn
	stall     atomic.Bool
	writing   chan struct{}
	closed    chan struct{}
	writeOnce sync.Once
	closeOnce sync.Once
}

func (c *stalledWriteConn) Write(p []byte) (int, error) {
	if c.stall.Load() {
		c.writeOnce.Do(func() { close(c.writing) })
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *stalledWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func newUnresponsiveSSHClient(t *testing.T) (*ssh.Client, *stalledWriteConn, <-chan struct{}) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	requestSeen := make(chan struct{}, 1)
	go func() {
		defer close(serverDone)
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		server, channels, requests, err := ssh.NewServerConn(raw, config)
		if err != nil {
			return
		}
		defer server.Close()
		go func() {
			for channel := range channels {
				_ = channel.Reject(ssh.UnknownChannelType, "unsupported")
			}
		}()
		for range requests {
			// Consume the request but withhold its reply to simulate an SSH peer
			// that stopped responding while the TCP connection remains open.
			select {
			case requestSeen <- struct{}{}:
			default:
			}
		}
	}()
	raw, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	transport := &stalledWriteConn{
		Conn: raw, writing: make(chan struct{}), closed: make(chan struct{}),
	}
	_ = transport.SetDeadline(time.Now().Add(2 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(transport, listener.Addr().String(), &ssh.ClientConfig{
		User: "heartbeat-test", HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		transport.Close()
		listener.Close()
		t.Fatal(err)
	}
	_ = transport.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, channels, requests)
	t.Cleanup(func() {
		_ = client.Close()
		_ = listener.Close()
		waitEvent(t, serverDone, "SSH server cleanup")
	})
	return client, transport, requestSeen
}

func TestRealSSHTimeoutUnblocksRequest(t *testing.T) {
	for _, scenario := range []string{"waiting_for_reply", "blocked_write"} {
		t.Run(scenario, func(t *testing.T) {
			client, transport, requestSeen := newUnresponsiveSSHClient(t)
			started := requestSeen
			if scenario == "blocked_write" {
				transport.stall.Store(true)
				started = transport.writing
			}
			failed, finished := startMonitor(client, make(chan struct{}), 5*time.Millisecond, 80*time.Millisecond)
			waitEvent(t, started, "blocked SSH request")
			waitEvent(t, finished, "timeout and SSH worker cleanup")
			if err := <-failed; !errors.Is(err, ErrTimeout) {
				t.Fatalf("failure = %v, want timeout", err)
			}
			if err := client.Wait(); err == nil {
				t.Fatal("SSH transport remains alive after heartbeat timeout")
			}
		})
	}
}

func TestRealSSHCancelUnblocksRequest(t *testing.T) {
	client, transport, _ := newUnresponsiveSSHClient(t)
	transport.stall.Store(true)
	done := make(chan struct{})
	failed, finished := startMonitor(client, done, 5*time.Millisecond, time.Second)
	waitEvent(t, transport.writing, "blocked SSH write")
	close(done)
	waitEvent(t, finished, "cancel and SSH worker cleanup")
	select {
	case err := <-failed:
		t.Fatalf("cancellation reported failure: %v", err)
	default:
	}
}
