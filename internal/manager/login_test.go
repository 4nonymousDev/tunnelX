package manager

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tunnelx/internal/config"
	"tunnelx/internal/keygen"
	"tunnelx/internal/logbuf"
)

func unavailableLoginServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestLoginFailurePreservesExistingIdentityKeyAndTunnels(t *testing.T) {
	dir := t.TempDir()
	key, err := keygen.Generate(dir, "original-key", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SchemaVersion: 2, ID: "original-device", Name: "original-name", ServerAddr: unavailableLoginServer(t), KeyPath: "original-key", Tunnels: []config.Tunnel{{ID: "original-tunnel", Kind: config.KindExport, LocalPort: 8080}}}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	beforeConfig, _ := os.ReadFile(cfg.Path())
	beforeKey, _ := os.ReadFile(key.KeyPath)
	m := New(cfg, logbuf.New(nil), "test", nil)
	if err := m.Login(context.Background(), "alice", "never-save-this-password"); err == nil {
		t.Fatal("unavailable server accepted login")
	}
	afterConfig, _ := os.ReadFile(cfg.Path())
	afterKey, _ := os.ReadFile(key.KeyPath)
	if string(beforeKey) != string(afterKey) || string(beforeConfig) != string(afterConfig) {
		t.Fatal("failed login changed existing data")
	}
	if cfg.ID != "original-device" || len(m.Tunnels()) != 1 || m.Tunnels()[0].Config().ID != "original-tunnel" {
		t.Fatal("identity or tunnels changed")
	}
	for _, entry := range m.log.Snapshot(logbuf.Debug) {
		if strings.Contains(entry.Msg, "never-save-this-password") {
			t.Fatal("password leaked to log")
		}
	}
}

func TestLoginGeneratesCredentialOnceAndPersistsDeviceBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.LoadPath(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServerAddr = unavailableLoginServer(t)
	cfg.KeyPath = ""
	originalID := cfg.ID
	m := New(cfg, logbuf.New(nil), "test", nil)
	for i := 0; i < 2; i++ {
		if err := m.Login(context.Background(), "alice", "never-save-this-password"); err == nil {
			t.Fatal("unavailable server accepted")
		}
	}
	loaded, err := config.LoadPath(cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != originalID || loaded.KeyPath != config.DefaultKeyName {
		t.Fatal("stable enrollment data missing")
	}
	first, _ := os.ReadFile(filepath.Join(dir, config.DefaultKeyName))
	if _, err := m.ensureLoginKey(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, config.DefaultKeyName))
	if len(first) == 0 || !reflect.DeepEqual(first, second) {
		t.Fatal("automatic key generation rotated existing credential")
	}
	data, _ := os.ReadFile(cfg.Path())
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "alice") {
		t.Fatal("credentials persisted")
	}
}

func TestAutomaticKeyNeverOverwritesOrphanPublicKey(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{ID: "original-device", KeyPath: "old-key"}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	pubPath := filepath.Join(dir, "old-key.pub")
	if err := os.WriteFile(pubPath, []byte("keep-original-public-key"), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(cfg, logbuf.New(nil), "test", nil)
	if _, err := m.ensureLoginKey(); err == nil {
		t.Fatal("orphan public key overwritten")
	}
	data, _ := os.ReadFile(pubPath)
	if string(data) != "keep-original-public-key" {
		t.Fatal("old public key changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "old-key")); !os.IsNotExist(err) {
		t.Fatal("unpaired credential created")
	}
}
