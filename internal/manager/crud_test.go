package manager

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
)

// newTestManager 构造一个未连接的 Manager，配置写入临时目录。
// newTestManager constructs a disconnected Manager whose configuration is written to a temporary directory.
func newTestManager(t *testing.T, tunnels ...config.Tunnel) (*Manager, string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)

	cfg := &config.Config{
		ID:         "test-id",
		Name:       "测试机",
		ServerAddr: "1.2.3.4:2222",
		ServerUser: "m1",
		KeyPath:    "tunnel_key",
		Tunnels:    tunnels,
	}
	cfg.SetPathForTest(path)

	return New(cfg, logbuf.New(nil), "test", func() {}), path
}

// readTunnels 从落盘的配置里读回隧道列表。
// readTunnels reads the tunnel list back from persisted configuration.
func readTunnels(t *testing.T, path string) []config.Tunnel {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置: %v", err)
	}
	var c config.Config
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("解析配置: %v", err)
	}
	return c.Tunnels
}

// TestAddTunnelPersists 验证新增隧道会落盘——否则重启即丢失。
// TestAddTunnelPersists verifies that a newly added tunnel is persisted rather than lost on restart.
func TestAddTunnelPersists(t *testing.T) {
	m, path := newTestManager(t)

	err := m.AddTunnel(config.Tunnel{
		Kind: config.KindImport, Name: "接入nginx", Enabled: true,
		PeerID: "peer-1", PeerName: "办公室PC", PeerSrcPort: 9999,
		ListenPort: 9999,
	})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	if got := len(m.Tunnels()); got != 1 {
		t.Errorf("内存中隧道数 = %d, 期望 1", got)
	}

	saved := readTunnels(t, path)
	if len(saved) != 1 {
		t.Fatalf("落盘隧道数 = %d, 期望 1", len(saved))
	}
	if saved[0].PeerID != "peer-1" {
		t.Errorf("落盘 PeerID = %q, 期望 peer-1", saved[0].PeerID)
	}
	if saved[0].ListenPort != 9999 {
		t.Errorf("落盘 ListenPort = %d, 期望 9999", saved[0].ListenPort)
	}
}

// TestAddTunnelRejectsDuplicatePort 验证本地端口冲突在添加时即被拒绝。
// 否则冲突要到运行期绑定失败才暴露，且两条隧道谁成功取决于启动顺序。
// TestAddTunnelRejectsDuplicatePort verifies that a local-port conflict is rejected when added.
// Otherwise it surfaces only as a runtime bind failure and the winner depends on startup order.
func TestAddTunnelRejectsDuplicatePort(t *testing.T) {
	m, _ := newTestManager(t, config.Tunnel{
		Kind: config.KindImport, Name: "已有", Enabled: true,
		PeerID: "peer-1", PeerSrcPort: 80, ListenPort: 8080,
	})

	err := m.AddTunnel(config.Tunnel{
		Kind: config.KindImport, Name: "冲突的", Enabled: true,
		PeerID: "peer-2", PeerSrcPort: 443, ListenPort: 8080, // 同一本地端口 / Same local port.
		// The same local port.
	})
	if err == nil {
		t.Fatal("重复的本地端口应被拒绝")
	}
	if !strings.Contains(err.Error(), "8080") {
		t.Errorf("错误信息应指出冲突端口, 得到: %v", err)
	}
	if !strings.Contains(err.Error(), "已有") {
		t.Errorf("错误信息应指出被谁占用, 得到: %v", err)
	}
	t.Logf("拒绝原因: %v", err)
}

func TestExportsAllowSamePortOnDifferentHosts(t *testing.T) {
	m, _ := newTestManager(t, config.Tunnel{
		ID: "local-web", Kind: config.KindExport, Name: "本机 Web",
		LocalHost: "127.0.0.1", LocalPort: 80,
	})

	if err := m.AddTunnel(config.Tunnel{
		ID: "file-server", Kind: config.KindExport, Name: "文件服务",
		LocalHost: "192.0.2.10", LocalPort: 80,
	}); err != nil {
		t.Fatalf("不同地址上的相同导出端口应被允许: %v", err)
	}
	if got := len(m.Tunnels()); got != 2 {
		t.Fatalf("导出隧道数 = %d，期望 2", got)
	}
}

func TestAddTunnelRejectsDuplicateTunnelID(t *testing.T) {
	m, _ := newTestManager(t, config.Tunnel{
		ID: "same", Kind: config.KindExport, Name: "已有", LocalPort: 80,
	})
	err := m.AddTunnel(config.Tunnel{
		ID: "same", Kind: config.KindExport, Name: "重复", LocalPort: 81,
	})
	if err == nil || !strings.Contains(err.Error(), "隧道标识") {
		t.Fatalf("重复隧道 ID 应被明确拒绝，得到: %v", err)
	}
}

// TestUpdateTunnelAllowsSamePort 验证编辑时不会与自身比较而误报端口冲突。
// TestUpdateTunnelAllowsSamePort verifies that edits do not compare against themselves and falsely report a port conflict.
func TestUpdateTunnelAllowsSamePort(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	m, path := newTestManager(t, config.Tunnel{
		Kind: config.KindImport, Name: "原名", Enabled: true,
		PeerID: "peer-1", PeerSrcPort: 80, ListenPort: port,
	})

	// 只改名字，端口不变——不应报"端口已被占用"。
	// Only the name changes; retaining the port must not report it as occupied.
	err = m.UpdateTunnel(0, config.Tunnel{
		Kind: config.KindImport, Name: "新名", Enabled: true,
		PeerID: "peer-1", PeerSrcPort: 80, ListenPort: port,
	})
	if err != nil {
		t.Fatalf("仅改名不应失败: %v", err)
	}

	saved := readTunnels(t, path)
	if len(saved) != 1 {
		t.Fatalf("落盘隧道数 = %d, 期望 1", len(saved))
	}
	if saved[0].Name != "新名" {
		t.Errorf("落盘名称 = %q, 期望 新名", saved[0].Name)
	}
}

// TestRemoveTunnelPersists 验证删除后落盘且序号正确。
// TestRemoveTunnelPersists verifies persistence and correct indexing after deletion.
func TestRemoveTunnelPersists(t *testing.T) {
	m, path := newTestManager(t,
		config.Tunnel{Kind: config.KindExport, Name: "A", LocalPort: 1001},
		config.Tunnel{Kind: config.KindExport, Name: "B", LocalPort: 1002},
		config.Tunnel{Kind: config.KindExport, Name: "C", LocalPort: 1003},
	)

	// Delete the middle entry.
	if err := m.RemoveTunnel(1); err != nil { // 删中间那条 / Remove the middle entry.
		t.Fatalf("删除失败: %v", err)
	}

	saved := readTunnels(t, path)
	if len(saved) != 2 {
		t.Fatalf("落盘隧道数 = %d, 期望 2", len(saved))
	}
	if saved[0].Name != "A" || saved[1].Name != "C" {
		t.Errorf("剩余隧道 = %q, %q, 期望 A, C", saved[0].Name, saved[1].Name)
	}

	// 内存与配置须保持一致，否则后续按序号操作会错位。
	// Memory and persisted configuration must agree or later index-based operations drift.
	live := m.Tunnels()
	if len(live) != 2 {
		t.Fatalf("内存隧道数 = %d, 期望 2", len(live))
	}
	if live[1].Config().Name != "C" {
		t.Errorf("内存中第 2 条 = %q, 期望 C", live[1].Config().Name)
	}
}

// TestToggleEnabled 验证停用/启用会落盘——重启后应保持用户的选择。
// TestToggleEnabled verifies that enable/disable changes persist across restarts.
func TestToggleEnabled(t *testing.T) {
	m, path := newTestManager(t, config.Tunnel{
		Kind: config.KindExport, Name: "nginx", Enabled: true, LocalPort: 9999,
	})

	cur := m.Tunnels()[0].Config()
	cur.Enabled = false
	if err := m.UpdateTunnel(0, cur); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	saved := readTunnels(t, path)
	if saved[0].Enabled {
		t.Error("停用后落盘的 Enabled 仍为 true")
	}
}

// TestValidateRejectsBadInput 验证非法输入被挡在保存之前。
// TestValidateRejectsBadInput verifies that invalid input is rejected before saving.
func TestValidateRejectsBadInput(t *testing.T) {
	m, _ := newTestManager(t)

	cases := []struct {
		name string
		tc   config.Tunnel
	}{
		{"端口为 0", config.Tunnel{Kind: config.KindExport, LocalPort: 0}},
		{"端口超范围", config.Tunnel{Kind: config.KindExport, LocalPort: 70000}},
		{"导入未指定对端", config.Tunnel{
			Kind: config.KindImport, ListenPort: 8080, PeerID: "",
		}},
		{"未知类型", config.Tunnel{Kind: "bogus", LocalPort: 80}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := m.AddTunnel(c.tc); err == nil {
				t.Error("非法配置应被拒绝")
			} else {
				t.Logf("拒绝原因: %v", err)
			}
		})
	}

	if got := len(m.Tunnels()); got != 0 {
		t.Errorf("非法配置不应被加入, 当前隧道数 = %d", got)
	}
}

// TestIndexOf 验证 UI 持有的隧道指针能正确映射回序号。
// TestIndexOf verifies that a tunnel pointer held by the UI maps back to the correct index.
func TestIndexOf(t *testing.T) {
	m, _ := newTestManager(t,
		config.Tunnel{Kind: config.KindExport, Name: "A", LocalPort: 1001},
		config.Tunnel{Kind: config.KindExport, Name: "B", LocalPort: 1002},
	)

	tunnels := m.Tunnels()
	for i, tun := range tunnels {
		if got := m.IndexOf(tun); got != i {
			t.Errorf("IndexOf(%q) = %d, 期望 %d", tun.Config().Name, got, i)
		}
	}

	// 删除后，被删的那条应查不到。
	// The deleted tunnel must no longer be found.
	removed := tunnels[0]
	if err := m.RemoveTunnel(0); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if got := m.IndexOf(removed); got != -1 {
		t.Errorf("已删除的隧道 IndexOf = %d, 期望 -1", got)
	}
}
