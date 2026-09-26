package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/server"
	"tunnelx/internal/store"
	"tunnelx/internal/updatepolicy"
)

func TestBlockedClientRetainsRequirementAndOriginalData(t *testing.T) {
	dir := t.TempDir()
	host, key, auth := filepath.Join(dir, "host"), filepath.Join(dir, "key"), filepath.Join(dir, "authorized_keys")
	writePEMKey(t, host)
	public := writePEMKey(t, key)
	if err := os.WriteFile(auth, public, 0600); err != nil {
		t.Fatal(err)
	}
	beforeKey, _ := os.ReadFile(key)
	db, err := store.Open(filepath.Join(dir, "tunnel-server.db"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(-time.Hour)
	if _, err = db.SetClientUpdatePolicy(context.Background(), updatepolicy.Policy{MinimumVersion: "0.2.1", EnforceAfter: &deadline}, 1, store.AdminAction{}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	addr := reservePort(t)
	srv, err := server.New(server.Config{Addr: addr, HostKeyPath: host, AuthorizedKeys: auth, DataDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.ListenAndServe()
	waitDial(t, addr)
	cfg := &config.Config{ID: "original-device", Name: "original name", ServerAddr: addr, KeyPath: key, Tunnels: []config.Tunnel{{ID: "original-tunnel", Kind: config.KindExport, LocalPort: 8080}}}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	if err = cfg.Save(); err != nil {
		t.Fatal(err)
	}
	beforeConfig, _ := os.ReadFile(cfg.Path())
	m := New(cfg, logbuf.New(nil), "0.2.0", nil)
	m.SetPrompts(func(string, string) bool { return true }, nil, nil, nil)
	m.Start()
	defer m.Stop()
	waitConn(t, m, ConnFailed)
	p := m.UpdatePolicy()
	if p == nil || !p.Blocked || p.MinimumVersion != "0.2.1" || !strings.Contains(m.ConnStatus().Reason, "0.2.1") {
		t.Fatalf("update reason lost: %+v %+v", p, m.ConnStatus())
	}
	m.Stop()
	if m.UpdatePolicy() == nil {
		t.Fatal("stopping hid required update")
	}
	afterKey, _ := os.ReadFile(key)
	afterConfig, _ := os.ReadFile(cfg.Path())
	if string(beforeKey) != string(afterKey) || string(beforeConfig) != string(afterConfig) {
		t.Fatal("update rejection changed key or config")
	}
	m.ClearUpdatePolicy()
	if m.UpdatePolicy() != nil {
		t.Fatal("switching servers retained the previous policy")
	}
}
