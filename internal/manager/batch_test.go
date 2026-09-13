package manager

import (
	"os"
	"strings"
	"testing"

	"tunnelx/internal/config"
)

// TestRemoveTunnelsDescending 验证批量删除按下标正确摘除，
// 不受"删前面的会让后面下标左移"影响。逐条调用 RemoveTunnel 正是栽在这里：
// 删完 0 号，原本的 2 号已变成 1 号，再删 2 号就删错了人。
// TestRemoveTunnelsDescending verifies that bulk deletion removes the requested indexes
// despite left shifts after earlier deletions. Calling RemoveTunnel sequentially fails here:
// after deleting index 0, the original index 2 becomes 1 and deleting 2 targets the wrong item.
func TestRemoveTunnelsDescending(t *testing.T) {
	m, path := newTestManager(t,
		config.Tunnel{Kind: config.KindExport, Name: "A", LocalPort: 1001},
		config.Tunnel{Kind: config.KindExport, Name: "B", LocalPort: 1002},
		config.Tunnel{Kind: config.KindExport, Name: "C", LocalPort: 1003},
		config.Tunnel{Kind: config.KindExport, Name: "D", LocalPort: 1004},
	)

	// 故意乱序传入，且包含首尾——顺序不该影响结果。
	// Pass indexes deliberately out of order, including both ends; order must not matter.
	if err := m.RemoveTunnels([]int{2, 0}); err != nil {
		t.Fatalf("批量删除失败: %v", err)
	}

	saved := readTunnels(t, path)
	if len(saved) != 2 {
		t.Fatalf("落盘隧道数 = %d, 期望 2", len(saved))
	}
	if saved[0].Name != "B" || saved[1].Name != "D" {
		t.Errorf("剩余隧道 = %q, %q, 期望 B, D", saved[0].Name, saved[1].Name)
	}

	live := m.Tunnels()
	if len(live) != 2 {
		t.Fatalf("内存隧道数 = %d, 期望 2", len(live))
	}
	if live[0].Config().Name != "B" || live[1].Config().Name != "D" {
		t.Errorf("内存剩余 = %q, %q, 期望 B, D",
			live[0].Config().Name, live[1].Config().Name)
	}
}

// TestRemoveTunnelsDeduplicates 验证重复下标只删一次。
// UI 层理论上不会传重复值，但真传了也不该把无辜的邻居一并删掉。
// TestRemoveTunnelsDeduplicates verifies that duplicate indexes are removed only once.
// The UI should not send duplicates, but doing so must not delete an innocent neighbor.
func TestRemoveTunnelsDeduplicates(t *testing.T) {
	m, path := newTestManager(t,
		config.Tunnel{Kind: config.KindExport, Name: "A", LocalPort: 1001},
		config.Tunnel{Kind: config.KindExport, Name: "B", LocalPort: 1002},
	)

	if err := m.RemoveTunnels([]int{0, 0}); err != nil {
		t.Fatalf("批量删除失败: %v", err)
	}

	saved := readTunnels(t, path)
	if len(saved) != 1 || saved[0].Name != "B" {
		t.Fatalf("剩余隧道 = %v, 期望仅剩 B", saved)
	}
}

// TestRemoveTunnelsRejectsOutOfRange 验证越界下标在动手前就被拒绝。
// 关键在于"一条都不删"——批量操作若删了一半才发现下标非法，
// 用户面对的是一个既非原样、也非预期的中间状态。
// TestRemoveTunnelsRejectsOutOfRange verifies that invalid indexes are rejected before mutation.
// Nothing may be deleted: otherwise a late failure leaves an unexpected intermediate state.
func TestRemoveTunnelsRejectsOutOfRange(t *testing.T) {
	m, path := newTestManager(t,
		config.Tunnel{Kind: config.KindExport, Name: "A", LocalPort: 1001},
		config.Tunnel{Kind: config.KindExport, Name: "B", LocalPort: 1002},
	)

	err := m.RemoveTunnels([]int{0, 5})
	if err == nil {
		t.Fatal("越界下标应被拒绝")
	}
	t.Logf("拒绝原因: %v", err)

	// 必须原封不动。
	// State must remain exactly unchanged.
	if got := len(m.Tunnels()); got != 2 {
		t.Errorf("失败后内存隧道数 = %d, 期望 2（不应删除任何一条）", got)
	}
	// 一条都没删，就不该有任何一次保存——配置文件此刻应仍不存在。
	// 这比比对文件内容更严格：写了再写回原样也算副作用。
	// With no deletion there must be no save, so the configuration file remains absent.
	// This is stricter than content comparison because writing and restoring is still a side effect.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("拒绝后不应触发保存, 但配置文件已存在: %v", err)
	}
}

// TestRemoveTunnelsEmptyIsNoop 验证空选中不触发保存。
// TestRemoveTunnelsEmptyIsNoop verifies that an empty selection does not trigger a save.
func TestRemoveTunnelsEmptyIsNoop(t *testing.T) {
	m, _ := newTestManager(t,
		config.Tunnel{Kind: config.KindExport, Name: "A", LocalPort: 1001},
	)

	if err := m.RemoveTunnels(nil); err != nil {
		t.Fatalf("空选中不应报错: %v", err)
	}
	if got := len(m.Tunnels()); got != 1 {
		t.Errorf("隧道数 = %d, 期望 1", got)
	}
}

// TestAddTunnelsAllOrNothing 验证批量添加中有一条非法时，整批都不落地。
// 若允许"部分成功"，用户从在线列表勾了 5 个、只进来 3 个，
// 还得自己比对是哪两个没进来——不如整批退回并说明原因。
// TestAddTunnelsAllOrNothing verifies that one invalid entry prevents the entire batch.
// Partial success would force users to identify missing selections manually; rejecting
// the batch with a reason is clearer.
func TestAddTunnelsAllOrNothing(t *testing.T) {
	m, path := newTestManager(t)

	err := m.AddTunnels([]config.Tunnel{
		{
			Kind: config.KindImport, Name: "好的", Enabled: true,
			PeerID: "peer-1", PeerSrcPort: 80, ListenPort: 18080,
		},
		{
			Kind: config.KindImport, Name: "坏的", Enabled: true,
			PeerID: "", PeerSrcPort: 443, ListenPort: 18443, // 缺对端 / Missing peer.
			// Missing peer.
		},
	})
	if err == nil {
		t.Fatal("含非法项的批量添加应整批失败")
	}
	if !strings.Contains(err.Error(), "坏的") {
		t.Errorf("错误信息应指出是哪一条出了问题, 得到: %v", err)
	}
	t.Logf("拒绝原因: %v", err)

	if got := len(m.Tunnels()); got != 0 {
		t.Errorf("整批失败后内存隧道数 = %d, 期望 0", got)
	}
	// 整批被拒时一次保存都不该发生。
	// A rejected batch must not cause any save.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("整批失败后不应触发保存, 但配置文件已存在: %v", err)
	}
}

// TestAddTunnelsRejectsInBatchPortConflict 验证同一批内部的端口冲突也被拦下。
// 这是逐条 AddTunnel 覆盖不到的盲区：校验只看已落地的隧道，
// 而同批的其他条目此刻都还没落地。
// TestAddTunnelsRejectsInBatchPortConflict verifies conflicts within one batch are rejected.
// Sequential AddTunnel validation misses this because sibling entries are not yet committed.
func TestAddTunnelsRejectsInBatchPortConflict(t *testing.T) {
	m, _ := newTestManager(t)

	err := m.AddTunnels([]config.Tunnel{
		{
			Kind: config.KindImport, Name: "甲", Enabled: true,
			PeerID: "peer-1", PeerSrcPort: 80, ListenPort: 18080,
		},
		{
			Kind: config.KindImport, Name: "乙", Enabled: true,
			PeerID: "peer-2", PeerSrcPort: 443, ListenPort: 18080, // 与"甲"撞车 / Conflicts with “A.”
			// Conflicts with "A".
		},
	})
	if err == nil {
		t.Fatal("同批内的端口冲突应被拒绝")
	}
	if !strings.Contains(err.Error(), "18080") {
		t.Errorf("错误信息应指出冲突端口, 得到: %v", err)
	}
	t.Logf("拒绝原因: %v", err)

	if got := len(m.Tunnels()); got != 0 {
		t.Errorf("整批失败后隧道数 = %d, 期望 0", got)
	}
}

// TestAddTunnelsPersistsAll 验证一批全部合法时都落盘。
// TestAddTunnelsPersistsAll verifies that every entry in a valid batch is persisted.
func TestAddTunnelsPersistsAll(t *testing.T) {
	m, path := newTestManager(t)

	err := m.AddTunnels([]config.Tunnel{
		{
			Kind: config.KindImport, Name: "甲", Enabled: true,
			PeerID: "peer-1", PeerSrcPort: 80, ListenPort: 18081,
		},
		{
			Kind: config.KindImport, Name: "乙", Enabled: true,
			PeerID: "peer-2", PeerSrcPort: 443, ListenPort: 18082,
		},
	})
	if err != nil {
		t.Fatalf("批量添加失败: %v", err)
	}

	saved := readTunnels(t, path)
	if len(saved) != 2 {
		t.Fatalf("落盘隧道数 = %d, 期望 2", len(saved))
	}
	if saved[0].Name != "甲" || saved[1].Name != "乙" {
		t.Errorf("落盘顺序 = %q, %q, 期望 甲, 乙", saved[0].Name, saved[1].Name)
	}
}

// TestUsedListenPorts 验证已占用端口集合含全部导入隧道。
// UI 的"冲突自动顺延"靠它起步，漏一个就会分配出一个必然绑定失败的端口。
// TestUsedListenPorts verifies that the occupied-port set includes every import tunnel.
// The UI uses it for automatic conflict avoidance; missing one guarantees a bad assignment.
func TestUsedListenPorts(t *testing.T) {
	m, _ := newTestManager(t,
		config.Tunnel{
			Kind: config.KindImport, Name: "甲", PeerID: "p1",
			PeerSrcPort: 80, ListenPort: 8080,
		},
		config.Tunnel{
			Kind: config.KindExport, Name: "导出的", LocalPort: 9090,
		},
		config.Tunnel{
			Kind: config.KindImport, Name: "乙", PeerID: "p2",
			PeerSrcPort: 443, ListenPort: 8443,
		},
	)

	used := m.UsedListenPorts()
	if !used[8080] || !used[8443] {
		t.Errorf("导入隧道的监听端口应在集合内, 得到 %v", used)
	}
	// 导出隧道的 LocalPort 是"连出去"的目标，不是本机监听，不该算占用。
	// An export's LocalPort is an outbound target, not a local listener, so it is not occupied here.
	if used[9090] {
		t.Error("导出隧道的本地端口不应计入监听端口占用")
	}
}
