package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestV2MigrationPreservesExistingIdentityAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{"id":"existing-device","name":"existing-name","key_path":"old-key","server_addr":"server:2222","tunnels":[{"id":"existing-export","kind":"export","local_port":8080},{"id":"existing-import","kind":"import","peer_id":"peer","peer_src_port":8080,"listen_port":8081}]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old-key"), []byte("unchanged-key"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "existing-device" || second.ID != first.ID || second.KeyPath != "old-key" || second.SchemaVersion != 2 {
		t.Fatalf("identity changed: %+v", second)
	}
	if second.Tunnels[0].ID != "existing-export" || second.Tunnels[1].ID != "existing-import" || second.Tunnels[1].PeerSrcPort != 8080 || second.Tunnels[1].PeerFingerprint != "" {
		t.Fatalf("tunnels changed: %+v", second.Tunnels)
	}
	backup, err := os.ReadFile(path + ".pre-v2.bak")
	if err != nil || string(backup) != string(raw) {
		t.Fatalf("backup changed: %v", err)
	}
	key, _ := os.ReadFile(filepath.Join(dir, "old-key"))
	if string(key) != "unchanged-key" {
		t.Fatal("private key changed")
	}
}

func TestV2MigrationDoesNotOverwriteConflictingBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := []byte(`{"id":"existing-device","key_path":"old-key"}`)
	_ = os.WriteFile(path, raw, 0600)
	_ = os.WriteFile(path+".pre-v2.bak", []byte("other original"), 0600)
	if _, err := LoadPath(path); err == nil {
		t.Fatal("conflicting backup silently replaced")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("failed migration modified source")
	}
}
