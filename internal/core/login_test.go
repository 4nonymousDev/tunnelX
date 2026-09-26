package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/config"
	"tunnelx/internal/keygen"
	"tunnelx/internal/logbuf"
)

func TestLoginSnapshotAndConfirmationRemainAvailableUntilCancellation(t *testing.T) {
	dir := t.TempDir()
	key, err := keygen.Generate(dir, "key", "")
	if err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	host, _ := ssh.NewSignerFromKey(priv)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var passwords atomic.Int32
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
		serverCfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			passwords.Add(1)
			return &ssh.Permissions{}, nil
		}}
		serverCfg.AddHostKey(host)
		conn, _, _, err := ssh.NewServerConn(raw, serverCfg)
		if err == nil {
			conn.Close()
		}
	}()
	cfg := &config.Config{ID: "old-device", Name: "old-name", ServerAddr: listener.Addr().String(), KeyPath: key.KeyPath}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	service, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	events, unsubscribe := service.Subscribe(8)
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- service.Login(ctx, "alice", "never-save-this-password") }()
	var confirmationID uint64
	deadline := time.After(2 * time.Second)
	for confirmationID == 0 {
		select {
		case event := <-events:
			if event.Confirmation != nil {
				confirmationID = event.Confirmation.ID
			}
		case <-deadline:
			t.Fatal("host confirmation missing")
		}
	}
	snapshots := make(chan Snapshot, 1)
	go func() { snapshots <- service.Snapshot(logbuf.Debug) }()
	select {
	case snapshot := <-snapshots:
		data, _ := json.Marshal(snapshot)
		if snapshot.Config.ID != "old-device" || len(snapshot.Pending) != 1 || strings.Contains(string(data), "never-save-this-password") {
			t.Fatal("unsafe or missing login snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot blocked behind login")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled login succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("login did not stop after cancellation")
	}
	for deadline := time.Now().Add(time.Second); ; {
		if len(service.Snapshot(logbuf.Debug).Pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled prompt remains visible")
		}
		time.Sleep(time.Millisecond)
	}
	if err := service.Confirm(confirmationID, true); err == nil {
		t.Fatal("canceled prompt accepted")
	}
	known, _ := cfg.KnownHostsPath()
	data, _ := os.ReadFile(known)
	if len(data) != 0 || passwords.Load() != 0 {
		t.Fatal("canceled login disclosed credentials or trusted host")
	}
}
