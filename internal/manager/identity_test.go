package manager

import (
	"path/filepath"
	"testing"
	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/proto"
)

func TestConfirmedTargetMigrationPreservesKeyAndCannotReplacePin(t *testing.T) {
	cfg := &config.Config{ID: "old-device", KeyPath: "old-private-key", Tunnels: []config.Tunnel{{ID: "import", Kind: config.KindImport, PeerID: "peer", PeerSrcPort: 80, ListenPort: 18080}}}
	cfg.SetPathForTest(filepath.Join(t.TempDir(), "config.json"))
	m := New(cfg, logbuf.New(nil), "test", nil)
	target := proto.Target{ClientID: "peer", Fingerprint: "SHA256:approved", TunnelID: "web", SessionID: "online", Generation: 1}
	if err := m.bindResolvedTarget("import", target); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadPath(cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != cfg.ID || loaded.KeyPath != "old-private-key" || loaded.Tunnels[0].PeerFingerprint != target.Fingerprint || loaded.Tunnels[0].PeerTunnelID != "web" || loaded.Tunnels[0].PeerSrcPort != 80 {
		t.Fatalf("incorrect migration: %+v", loaded)
	}
	target.Fingerprint = "SHA256:replacement"
	if err := m.bindResolvedTarget("import", target); err == nil {
		t.Fatal("existing pin was silently replaced")
	}
	if cfg.Tunnels[0].PeerFingerprint != "SHA256:approved" {
		t.Fatal("rejected replacement changed memory")
	}
}
