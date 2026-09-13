package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSettingsRoundTrip 验证连接设置的修改能完整落盘并读回。
// UI 的「设置」表单直接改这几个字段，若有任一字段的 JSON 标签写错，
// 表现为"改了保存、重启又变回去"，而这类问题不会有任何报错。
// TestSettingsRoundTrip verifies that connection-setting changes are fully persisted and reloaded.
// The UI settings form edits these fields directly; an incorrect JSON tag would silently make
// a saved change revert after restart.
func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	cfg := &Config{
		ID:   "stable-id",
		Name: "旧名称",
		Tunnels: []Tunnel{{
			Kind: KindExport, Name: "nginx", Enabled: true,
			LocalHost: "127.0.0.1", LocalPort: 9999,
		}},
	}
	cfg.SetPathForTest(path)

	// 模拟用户在设置表单里的修改。
	// Simulate changes made through the settings form.
	cfg.ServerAddr = "example.com:2222"
	cfg.KeyPath = "tunnel_key"
	cfg.Name = "新名称"

	if err := cfg.Save(); err != nil {
		t.Fatalf("保存: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("解析: %v\n内容:\n%s", err, data)
	}

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"ServerAddr", back.ServerAddr, "example.com:2222"},
		{"KeyPath", back.KeyPath, "tunnel_key"},
		{"Name", back.Name, "新名称"},
		{"ID", back.ID, "stable-id"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s 往返后 = %q, 期望 %q", c.field, c.got, c.want)
		}
	}

	// 改设置不应波及已配置的隧道。
	// Changing settings must not affect configured tunnels.
	if len(back.Tunnels) != 1 {
		t.Fatalf("隧道数 = %d, 期望 1", len(back.Tunnels))
	}
	if back.Tunnels[0].LocalPort != 9999 {
		t.Errorf("隧道端口被改动: %d", back.Tunnels[0].LocalPort)
	}
}

// TestIDStableAcrossSaves 验证本机标识在多次保存后保持不变。
// ID 是 Importer 侧 peer_id 的取值来源。若它随保存变化，
// 对端已配置的导入隧道会突然找不到本机，且症状是"对端离线"，极难联想到原因。
// TestIDStableAcrossSaves verifies that the local machine ID remains stable across saves.
// Importers use it as peer_id; if it changed, configured remote imports would lose this
// machine and misleadingly appear as an offline peer.
func TestIDStableAcrossSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	cfg := &Config{ID: "must-not-change", Name: "机器"}
	cfg.SetPathForTest(path)

	for i := 0; i < 3; i++ {
		cfg.ServerAddr = "addr-" + string(rune('A'+i))
		if err := cfg.Save(); err != nil {
			t.Fatalf("第 %d 次保存: %v", i+1, err)
		}
	}

	data, _ := os.ReadFile(path)
	var back Config
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if back.ID != "must-not-change" {
		t.Errorf("多次保存后 ID = %q, 期望保持不变", back.ID)
	}
}
