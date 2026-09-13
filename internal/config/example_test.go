package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExampleConfigParses 验证仓库里的 config.example.json 能被正确解析。
//
// 示例配置是用户手工编辑时的唯一参照。若它本身解析不了，或字段名与代码对不上，
// 用户照抄后会遇到"配置了却不生效"这类无报错的怪问题。
// TestExampleConfigParses verifies that the repository's config.example.json parses correctly.
//
// The example is the sole reference for manual editing. If it cannot be parsed or its field
// names differ from the code, copied configurations silently fail to take effect.
func TestExampleConfigParses(t *testing.T) {
	// 从包目录回到仓库根。
	// Move from the package directory back to the repository root.
	path := filepath.Join("..", "..", "config.example.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取示例配置: %v", err)
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("示例配置解析失败: %v", err)
	}

	if c.ServerAddr == "" {
		t.Error("示例配置缺少 server_addr")
	}
	if c.KeyPath == "" {
		t.Error("示例配置缺少 key_path")
	}

	// 两种类型都要有示例——用户最容易困惑的正是 import 侧的字段。
	// Include both types because import-side fields are the most confusing to users.
	var exports, imports int
	for _, tn := range c.Tunnels {
		switch tn.Kind {
		case KindExport:
			exports++
			if tn.LocalPort == 0 {
				t.Errorf("导出隧道 %q 缺少 local_port", tn.Name)
			}
			if tn.LocalHost == "" {
				t.Errorf("导出隧道 %q 缺少 local_host", tn.Name)
			}
		case KindImport:
			imports++
			if tn.ListenPort == 0 {
				t.Errorf("导入隧道 %q 缺少 listen_port", tn.Name)
			}
			if tn.PeerSrcPort == 0 {
				t.Errorf("导入隧道 %q 缺少 peer_src_port", tn.Name)
			}
			if tn.PeerID == "" {
				t.Errorf("导入隧道 %q 缺少 peer_id", tn.Name)
			}
		default:
			t.Errorf("隧道 %q 的 kind 无效: %q", tn.Name, tn.Kind)
		}
	}

	if exports == 0 {
		t.Error("示例配置应包含至少一条导出隧道")
	}
	if imports == 0 {
		t.Error("示例配置应包含至少一条导入隧道")
	}
	t.Logf("示例配置：%d 条导出、%d 条导入", exports, imports)

	// id 必须留空：多台机器复制同一份配置时，重复的 id 会被服务端拒绝。
	// id must remain empty: duplicated IDs from copying the configuration are rejected by the server.
	if c.ID != "" {
		t.Errorf("示例配置的 id 应留空，当前为 %q", c.ID)
	}

	// 导入隧道的本地端口不可重复，否则后启动的那条会绑定失败。
	// Import tunnels must use unique local ports or the later one will fail to bind.
	seen := map[int]string{}
	for _, tn := range c.Tunnels {
		if tn.Kind != KindImport {
			continue
		}
		if prev, dup := seen[tn.ListenPort]; dup {
			t.Errorf("导入隧道 %q 与 %q 的 listen_port 重复: %d",
				tn.Name, prev, tn.ListenPort)
		}
		seen[tn.ListenPort] = tn.Name
	}
}
