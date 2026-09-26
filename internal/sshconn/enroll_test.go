package sshconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"tunnelx/internal/enrollment"
	"tunnelx/internal/keygen"
)

func testEnrollmentServer(t *testing.T, firstTimeout time.Duration, response enrollment.Response) (string, ssh.Signer, *atomic.Int32, <-chan error) {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	host, _ := ssh.NewSignerFromKey(private)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	attempts := &atomic.Int32{}
	result := make(chan error, 8)
	go func() {
		for number := 0; ; number++ {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
			if number == 0 && firstTimeout > 0 {
				_ = raw.SetDeadline(time.Now().Add(firstTimeout))
			}
			go func() {
				defer raw.Close()
				cfg := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
					attempts.Add(1)
					if meta.User() != "alice" || string(password) != "login-secret-value" {
						return nil, errors.New("bad credentials")
					}
					return &ssh.Permissions{}, nil
				}}
				cfg.AddHostKey(host)
				conn, chans, reqs, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				newChannel, ok := <-chans
				if !ok {
					return
				}
				if newChannel.ChannelType() != enrollment.ChannelType {
					result <- errors.New("wrong channel")
					return
				}
				channel, requests, err := newChannel.Accept()
				if err != nil {
					result <- err
					return
				}
				defer channel.Close()
				go ssh.DiscardRequests(requests)
				var request enrollment.Request
				if err := enrollment.Read(channel, &request); err != nil {
					result <- err
					return
				}
				key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(request.PublicKey))
				if err != nil || len(options) != 0 || len(rest) != 0 || request.ClientID != "existing-device" {
					result <- errors.New("bad device request")
					return
				}
				var sig ssh.Signature
				if err := ssh.Unmarshal(request.Signature, &sig); err != nil {
					result <- err
					return
				}
				err = key.Verify(enrollment.SigningPayload(conn.SessionID(), conn.User(), request.ClientID, key), &sig)
				result <- err
				if err == nil {
					_ = enrollment.Write(channel, response)
				}
			}()
		}
	}()
	return listener.Addr().String(), host, attempts, result
}

func enrollmentDialer(t *testing.T, addr string) Dialer {
	t.Helper()
	dir := t.TempDir()
	key, err := keygen.Generate(dir, "existing-key", "")
	if err != nil {
		t.Fatal(err)
	}
	return Dialer{Addr: addr, KeyPath: key.KeyPath, KnownHosts: filepath.Join(dir, "known_hosts")}
}

func TestEnrollmentProofAndHostTrustBeforePassword(t *testing.T) {
	addr, _, attempts, result := testEnrollmentServer(t, 0, enrollment.Response{OK: true})
	d := enrollmentDialer(t, addr)
	d.Prompt = func(host, fingerprint string) bool {
		if attempts.Load() != 0 {
			t.Error("password sent before host trust")
		}
		return true
	}
	before, _ := os.ReadFile(d.KeyPath)
	if err := d.Enroll(context.Background(), " Alice ", "login-secret-value", "existing-device", "device"); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(d.KeyPath)
	if string(before) != string(after) || attempts.Load() != 1 {
		t.Fatal("key changed or password retried")
	}
	entries, _ := os.ReadDir(filepath.Dir(d.KeyPath))
	for _, entry := range entries {
		data, _ := os.ReadFile(filepath.Join(filepath.Dir(d.KeyPath), entry.Name()))
		if strings.Contains(string(data), "login-secret-value") {
			t.Fatal("password written to disk")
		}
	}
}

func TestEnrollmentRejectsUntrustedAndChangedHostWithoutPassword(t *testing.T) {
	addr, _, attempts, _ := testEnrollmentServer(t, 0, enrollment.Response{OK: true})
	d := enrollmentDialer(t, addr)
	d.Prompt = func(string, string) bool { return false }
	if err := d.Enroll(context.Background(), "alice", "login-secret-value", "existing-device", ""); err == nil {
		t.Fatal("rejected host accepted")
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	otherHost, _ := ssh.NewSignerFromKey(private)
	if err := os.WriteFile(d.KnownHosts, []byte(knownhosts.Line([]string{knownhosts.Normalize(addr)}, otherHost.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d.Prompt = func(string, string) bool { t.Error("changed host prompted"); return true }
	if err := d.Enroll(context.Background(), "alice", "login-secret-value", "existing-device", ""); err == nil {
		t.Fatal("changed host accepted")
	}
	if attempts.Load() != 0 {
		t.Fatal("password disclosed before trust")
	}
}

func TestEnrollmentCancellationDoesNotTrustLatePrompt(t *testing.T) {
	addr, _, attempts, _ := testEnrollmentServer(t, 0, enrollment.Response{OK: true})
	d := enrollmentDialer(t, addr)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.Prompt = func(string, string) bool { close(entered); <-release; close(finished); return true }
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- d.Enroll(ctx, "alice", "login-secret-value", "existing-device", "") }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close login")
	}
	close(release)
	<-finished
	data, _ := os.ReadFile(d.KnownHosts)
	if len(data) != 0 || attempts.Load() != 0 {
		t.Fatal("late prompt authorized login")
	}
}

func TestEnrollmentFirstTrustTimeoutRetriesBeforePasswordOnly(t *testing.T) {
	addr, _, attempts, result := testEnrollmentServer(t, 50*time.Millisecond, enrollment.Response{OK: true})
	d := enrollmentDialer(t, addr)
	var prompts atomic.Int32
	d.Prompt = func(string, string) bool { prompts.Add(1); time.Sleep(150 * time.Millisecond); return true }
	if err := d.Enroll(context.Background(), "alice", "login-secret-value", "existing-device", ""); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 || attempts.Load() != 1 {
		t.Fatalf("prompts=%d passwords=%d", prompts.Load(), attempts.Load())
	}
}

func TestEnrollmentNeverEchoesPeerError(t *testing.T) {
	addr, _, _, _ := testEnrollmentServer(t, 0, enrollment.Response{Error: "login-secret-value"})
	d := enrollmentDialer(t, addr)
	d.Prompt = func(string, string) bool { return true }
	if err := d.Enroll(context.Background(), "alice", "login-secret-value", "existing-device", ""); err == nil || strings.Contains(err.Error(), "login-secret-value") {
		t.Fatalf("unsafe error: %v", err)
	}
}
