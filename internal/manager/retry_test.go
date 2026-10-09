package manager

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/config"
	"tunnelx/internal/control"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/proto"
	"tunnelx/internal/server"
)

func TestDuplicateIDGraceDeadline(t *testing.T) {
	start := time.Unix(1, 0)
	r := duplicateIDRetry{policy: defaultDuplicateIDPolicy()}
	duplicate := &helloFailure{&proto.Error{Code: proto.CodeDuplicateID}}
	fault, delay, handled := r.next(duplicate, start)
	if !handled || !fault.Retryable || delay < 5*time.Second || delay > 10*time.Second {
		t.Fatalf("first conflict: fault=%+v delay=%s handled=%v", fault, delay, handled)
	}
	deadline := r.deadline
	// A network failure between duplicate replies cannot give a persistent
	// conflict a fresh recovery window.
	if _, _, handled := r.next(errors.New("network unavailable"), start.Add(time.Minute)); handled {
		t.Fatal("network error treated as a registration conflict")
	}
	fault, delay, _ = r.next(duplicate, deadline.Add(-time.Millisecond))
	if !fault.Retryable || delay != time.Millisecond || r.deadline != deadline {
		t.Fatalf("deadline moved or wait exceeded grace: fault=%+v delay=%s", fault, delay)
	}
	fault, _, _ = r.next(duplicate, deadline)
	if fault.Retryable || !strings.Contains(fault.Reason, "其他 TunnelX 实例") {
		t.Fatalf("persistent conflict did not fail with guidance: %+v", fault)
	}
	r.reset()
	fault, _, _ = r.next(duplicate, deadline)
	if !fault.Retryable || !r.deadline.Equal(deadline.Add(2*time.Minute)) {
		t.Fatal("successful hello did not permit a fresh recovery window")
	}
	if _, _, handled := r.next(&proto.Error{Code: proto.CodeDuplicateID}, start); handled {
		t.Fatal("duplicate outside hello must keep its original error classification")
	}
	if proto.Retryable(proto.CodeDuplicateID) {
		t.Fatal("generic protocol duplicate classification changed")
	}
}

func startRetryServer(t *testing.T, configure ...func(*server.Config)) *config.Config {
	t.Helper()
	dir := t.TempDir()
	hostKey := filepath.Join(dir, "host_key")
	keyPath := filepath.Join(dir, "client_key")
	authorized := filepath.Join(dir, "authorized_keys")
	writePEMKey(t, hostKey)
	pub := writePEMKey(t, keyPath)
	key, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorized, pub, 0o600); err != nil {
		t.Fatal(err)
	}
	addr := reservePort(t)
	serverConfig := server.Config{
		Addr: addr, HostKeyPath: hostKey, AuthorizedKeys: authorized,
		IdentityBindings: map[string]string{"retry-device": ssh.FingerprintSHA256(key)},
	}
	for _, apply := range configure {
		apply(&serverConfig)
	}
	srv, err := server.New(serverConfig, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	go srv.ListenAndServe()
	t.Cleanup(func() { _ = srv.Close() })
	waitDial(t, addr)
	cfg := &config.Config{ID: "retry-device", Name: "retry-client", ServerAddr: addr, KeyPath: keyPath}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	return cfg
}

func dialRetryHolder(t *testing.T, cfg *config.Config) (*ssh.Client, *control.Client) {
	t.Helper()
	keyData, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ssh.Dial("tcp", cfg.ServerAddr, &ssh.ClientConfig{
		User: config.SSHUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctrl, err := control.Dial(conn, cfg, proto.RoleExporter, "test", logbuf.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctrl.Close() })
	return conn, ctrl
}

func retryManager(t *testing.T, cfg *config.Config, policy duplicateIDPolicy) *Manager {
	t.Helper()
	m := New(cfg, logbuf.New(nil), "test", nil)
	m.duplicatePolicy = policy
	m.SetPrompts(func(string, string) bool { return true }, nil, nil, nil)
	t.Cleanup(m.Stop)
	return m
}

func TestDuplicateIDRecoversAfterHolderDisconnects(t *testing.T) {
	cfg := startRetryServer(t)
	holder, ctrl := dialRetryHolder(t, cfg)
	m := retryManager(t, cfg, duplicateIDPolicy{time.Second, 10 * time.Millisecond, 20 * time.Millisecond})
	m.Start()
	waitConn(t, m, ConnRetrying)
	for _, entry := range m.log.Snapshot(logbuf.Info) {
		if entry.Source == "conn" && strings.HasPrefix(entry.Msg, "已连接 ") {
			t.Fatal("SSH authentication logged as successful device connection before hello")
		}
	}
	if err := ctrl.Publish(); err != nil {
		t.Fatalf("rejected reconnect disrupted healthy holder: %v", err)
	}
	_ = holder.Close()
	waitConn(t, m, ConnConnected)
	var controlReady, connected bool
	for _, entry := range m.log.Snapshot(logbuf.Info) {
		if entry.Source == "ctrl" && strings.HasPrefix(entry.Msg, "控制通道已建立") {
			controlReady = true
		}
		if entry.Source == "conn" && strings.HasPrefix(entry.Msg, "已连接 ") {
			if !controlReady {
				t.Fatal("connection success preceded device registration")
			}
			connected = true
		}
	}
	if !connected {
		t.Fatal("device registration succeeded without a connection success log")
	}
}

func TestPersistentDuplicateFailsWithoutClosingHolder(t *testing.T) {
	cfg := startRetryServer(t)
	_, ctrl := dialRetryHolder(t, cfg)
	m := retryManager(t, cfg, duplicateIDPolicy{180 * time.Millisecond, 50 * time.Millisecond, 60 * time.Millisecond})
	m.Start()
	deadline := time.Now().Add(3 * time.Second)
	for m.ConnStatus().State != ConnFailed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if status := m.ConnStatus(); status.State != ConnFailed || !strings.Contains(status.Reason, "已停止自动重试") {
		t.Fatalf("persistent conflict did not stop: %+v", status)
	}
	if err := ctrl.Publish(); err != nil {
		t.Fatalf("duplicate retries closed the original healthy holder: %v", err)
	}
}

func TestStopCancelsDuplicateGraceAndStartRestartsIt(t *testing.T) {
	cfg := startRetryServer(t)
	_, ctrl := dialRetryHolder(t, cfg)
	m := retryManager(t, cfg, duplicateIDPolicy{2 * time.Minute, time.Minute, time.Minute})
	for i := 0; i < 3; i++ {
		m.Start()
		waitConn(t, m, ConnRetrying)
		done := make(chan struct{})
		go func() { m.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Stop waited for duplicate retry timer")
		}
		if m.ConnStatus().State != ConnIdle {
			t.Fatal("Stop left a connection loop running")
		}
	}
	if err := ctrl.Publish(); err != nil {
		t.Fatalf("start/stop retries disrupted original holder: %v", err)
	}
}

func TestRestartRecoversAfterBlackholedServerSessionExpires(t *testing.T) {
	cfg := startRetryServer(t, func(c *server.Config) {
		c.HeartbeatInterval = 20 * time.Millisecond
		c.HeartbeatTimeout = 350 * time.Millisecond
	})
	proxyAddr, drop := startRetryBlackhole(t, cfg.ServerAddr)
	oldConfig := *cfg
	oldConfig.ServerAddr = proxyAddr
	oldClient, _ := dialRetryHolder(t, &oldConfig)
	drop()
	// The old client has exited, but its FIN cannot reach the server. The new
	// manager has no successful-connection history and must still retry hello.
	_ = oldClient.Close()
	m := retryManager(t, cfg, duplicateIDPolicy{2 * time.Second, 80 * time.Millisecond, 90 * time.Millisecond})
	m.Start()
	waitConn(t, m, ConnRetrying)
	waitConn(t, m, ConnConnected)
}

func startRetryBlackhole(t *testing.T, target string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var drop atomic.Bool
	ready := make(chan struct{})
	var clientSide, serverSide net.Conn
	var proxyErr error
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer close(ready)
		clientSide, proxyErr = listener.Accept()
		if proxyErr != nil {
			return
		}
		serverSide, proxyErr = net.Dial("tcp", target)
		if proxyErr != nil {
			_ = clientSide.Close()
			return
		}
		copyDirection := func(dst, src net.Conn) {
			defer workers.Done()
			buf := make([]byte, 32*1024)
			for {
				n, err := src.Read(buf)
				if n > 0 && !drop.Load() {
					if _, err := io.Copy(dst, bytes.NewReader(buf[:n])); err != nil {
						return
					}
				}
				if err != nil {
					return // Keep the opposite socket open, even after EOF.
				}
			}
		}
		workers.Add(2)
		go copyDirection(clientSide, serverSide)
		go copyDirection(serverSide, clientSide)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-ready
		if clientSide != nil {
			_ = clientSide.Close()
		}
		if serverSide != nil {
			_ = serverSide.Close()
		}
		workers.Wait()
	})
	return listener.Addr().String(), func() {
		<-ready
		if proxyErr != nil {
			t.Fatalf("blackhole proxy: %v", proxyErr)
		}
		drop.Store(true)
	}
}

func TestStopInterruptsPendingHello(t *testing.T) {
	dir := t.TempDir()
	hostPath, keyPath := filepath.Join(dir, "host"), filepath.Join(dir, "client")
	writePEMKey(t, hostPath)
	writePEMKey(t, keyPath)
	hostData, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.ParsePrivateKey(hostData)
	if err != nil {
		t.Fatal(err)
	}
	sshConfig := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	sshConfig.AddHostKey(hostKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	helloReceived := make(chan struct{})
	serverDone := make(chan struct{})
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		defer close(serverDone)
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		conn, chans, reqs, err := ssh.NewServerConn(raw, sshConfig)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(reqs)
		incoming, ok := <-chans
		if !ok {
			return
		}
		ch, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		go ssh.DiscardRequests(requests)
		if _, _, err := proto.NewConn(ch).Recv(); err != nil {
			return
		}
		close(helloReceived)
		_ = conn.Wait() // Deliberately never send hello_ok.
	}()
	cfg := &config.Config{ID: "pending-hello", ServerAddr: listener.Addr().String(), KeyPath: keyPath}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	m := retryManager(t, cfg, defaultDuplicateIDPolicy())
	m.Start()
	select {
	case <-helloReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("hello did not reach test server")
	}
	stopped := make(chan struct{})
	go func() { m.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for hello response timeout")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close SSH transport")
	}
}
