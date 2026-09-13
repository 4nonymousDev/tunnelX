package manager

import (
	"fmt"
	"sort"
	"strings"

	"tunnelx/internal/config"
	"tunnelx/internal/tunnel"
)

// RemoveTunnels 批量删除隧道。indices 为 Tunnels() 中的下标，顺序不限。
// RemoveTunnels removes tunnels in bulk. indices are indexes into Tunnels() and may be in any order.
//
// 不能让 UI 循环调用 RemoveTunnel 代替：每删一条，其后所有隧道的下标都会左移，
// 循环里第二次及以后的下标便全部失准，删掉的是无辜的邻居。此处一次性摘除，
// 从大到小执行，下标在整个过程中保持有效。
// The UI cannot emulate this by calling RemoveTunnel in a loop: each removal shifts
// every later index to the left, so all indexes from the second iteration onward
// become stale and can remove innocent neighbors. Removing in descending order keeps
// every index valid throughout the operation.
//
// 越界一律先拒绝、不做任何删除：批量操作删到一半才失败，留下的既不是原样、
// 也不是用户预期，比彻底不删更难收拾。
// Any out-of-range index rejects the entire request before deletion. A half-failed
// batch leaves state neither unchanged nor as requested, which is harder to recover from.
func (m *Manager) RemoveTunnels(indices []int) error {
	if len(indices) == 0 {
		return nil
	}

	m.mu.Lock()

	// 去重 + 校验，全部通过后才动手。
	// Deduplicate and validate first; mutate state only after every index passes.
	seen := make(map[int]bool, len(indices))
	uniq := make([]int, 0, len(indices))
	for _, i := range indices {
		if i < 0 || i >= len(m.tunnels) {
			m.mu.Unlock()
			return fmt.Errorf("隧道序号越界: %d", i)
		}
		if seen[i] {
			continue
		}
		seen[i] = true
		uniq = append(uniq, i)
	}

	// 降序删除，使每次删除都不影响尚未处理的下标。
	// Delete in descending order so each deletion leaves pending indexes unchanged.
	sort.Sort(sort.Reverse(sort.IntSlice(uniq)))

	removed := make([]*tunnel.Tunnel, 0, len(uniq))
	labels := make([]string, 0, len(uniq))
	for _, i := range uniq {
		t := m.tunnels[i]
		removed = append(removed, t)
		labels = append(labels, t.Label())
		m.tunnels = append(m.tunnels[:i], m.tunnels[i+1:]...)
		m.cfg.Tunnels = append(m.cfg.Tunnels[:i], m.cfg.Tunnels[i+1:]...)
	}
	m.mu.Unlock()

	// 停隧道要在锁外——Stop 会等待其 goroutine 收尾，而那些 goroutine
	// 可能正回调进来（onTunnelChange）并尝试取锁。
	// Stop tunnels outside the lock: Stop waits for their goroutines, which may be
	// calling back through onTunnelChange and trying to acquire this lock.
	for _, t := range removed {
		t.Stop()
	}

	// 只保存一次。逐条保存不仅慢，中途失败还会留下"内存已删、磁盘删了一半"
	// 的错位状态。
	// Save only once. Per-item saves are slower and can leave memory and disk out of
	// sync if a save fails midway.
	if err := m.cfg.Save(); err != nil {
		return fmt.Errorf("保存配置: %w", err)
	}

	// labels 是降序摘下的，倒回来按列表原顺序念，读日志时更好对照。
	// labels were collected in descending order; reverse them to preserve list order in logs.
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	m.log.Infof("app", "已删除 %d 条隧道: %s", len(labels), strings.Join(labels, "、"))

	if m.onTunnelChange != nil {
		m.onTunnelChange()
	}
	return nil
}

// AddTunnels 批量新增隧道，全部合法才落地，否则一条都不加。
// AddTunnels adds tunnels atomically: all must be valid before any are committed.
//
// "要么全成、要么全不成"是刻意的：用户从在线列表勾选 5 个端口一次性添加，
// 若默默只进来 3 个，还得自己逐条比对少了谁。整批退回并指名道姓说明哪一条
// 有问题，用户改完重来即可。
// The all-or-nothing behavior is intentional. If a user selects five online ports,
// silently adding only three would force a manual comparison. Rejecting the whole
// batch while identifying the bad entry makes correction straightforward.
func (m *Manager) AddTunnels(tcs []config.Tunnel) error {
	if len(tcs) == 0 {
		return nil
	}

	m.mu.Lock()

	// 先在快照上把整批校验一遍。校验必须把同批的其他条目也算进去——
	// 逐条 AddTunnel 看不见这一点：它只比对已落地的隧道，
	// 而同批的兄弟此刻都还没落地，两条抢同一本地端口便能一路放行到运行期。
	// Validate the entire batch against a snapshot that includes earlier accepted
	// entries from this batch. Calling AddTunnel one by one only sees committed
	// tunnels and would miss conflicts between siblings until runtime.
	pending := append([]config.Tunnel(nil), m.cfg.Tunnels...)
	for i := range tcs {
		config.EnsureTunnelID(&tcs[i])
		tc := tcs[i]
		if err := validateAgainst(pending, tc, -1); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("「%s」: %w", tunnelDesc(tc), err)
		}
		pending = append(pending, tc)
	}

	added := make([]*tunnel.Tunnel, 0, len(tcs))
	for _, tc := range tcs {
		t := tunnel.New(tc, m.log, m.onTunnelChange)
		added = append(added, t)
		m.tunnels = append(m.tunnels, t)
		m.cfg.Tunnels = append(m.cfg.Tunnels, tc)
	}
	ctx, conn, ctrl := m.runCtx, m.conn, m.ctrl
	m.mu.Unlock()

	if err := m.cfg.Save(); err != nil {
		return fmt.Errorf("保存配置: %w", err)
	}
	m.log.Infof("app", "已添加 %d 条隧道", len(added))

	// 已连接时立即启动，与单条添加一致——用户刚添加就期望它生效。
	// Start immediately when connected, matching single-item additions and user expectations.
	if ctx != nil && conn != nil && ctrl != nil {
		for i, t := range added {
			if tcs[i].Enabled {
				t.Start(ctx, conn.Client(), ctrl, ctrl)
			}
		}
	}
	if m.onTunnelChange != nil {
		m.onTunnelChange()
	}
	return nil
}

// UsedListenPorts 返回已被导入隧道占用的本地监听端口集合。
// 供 UI 在批量添加前为每条候选挑一个不冲突的默认端口。
// UsedListenPorts returns local listening ports occupied by import tunnels.
// The UI uses it to choose a conflict-free default port for each batch candidate.
//
// 只算导入隧道：导出隧道的 LocalPort 是"连出去"的目标（那是别的程序在监听），
// 本程序并不在该端口上监听，算进来会平白无故地把可用端口顺延掉。
// Count imports only. An export's LocalPort is an outbound target listened on by
// another program; this process does not bind it, so counting it would skip valid ports.
func (m *Manager) UsedListenPorts() map[int]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	used := make(map[int]bool, len(m.cfg.Tunnels))
	for _, tc := range m.cfg.Tunnels {
		if tc.Kind == config.KindImport && tc.ListenPort > 0 {
			used[tc.ListenPort] = true
		}
	}
	return used
}

// tunnelDesc 给出一条隧道在错误信息里的称呼。
// 名称可以为空（UI 允许不填），此时退回到端口——总得让用户认出是哪一条。
// tunnelDesc returns a human-readable tunnel label for errors. Names may be empty,
// so it falls back to the port to ensure the user can identify the entry.
func tunnelDesc(tc config.Tunnel) string {
	if tc.Name != "" {
		return tc.Name
	}
	if tc.Kind == config.KindImport {
		return fmt.Sprintf("%s:%d", tc.PeerName, tc.PeerSrcPort)
	}
	return fmt.Sprint(tc.LocalPort)
}

// validateAgainst 是 validate 的无锁内核，针对给定的隧道集合校验一条配置。
// 拆出来是为了让 AddTunnels 能在"现有 + 本批已接受"的并集上校验，
// 而 validate 持有 RLock、只认已落地的那些。
// validateAgainst is validate's lock-free core and checks one configuration against
// a supplied tunnel set. This lets AddTunnels validate against existing plus accepted
// batch entries, while validate holds RLock and sees only committed tunnels.
func validateAgainst(existing []config.Tunnel, tc config.Tunnel, skip int) error {
	for i, ex := range existing {
		if i != skip && tc.ID != "" && ex.ID == tc.ID {
			return fmt.Errorf("隧道标识与「%s」重复", tunnelDesc(ex))
		}
	}

	switch tc.Kind {
	case config.KindExport:
		if tc.LocalPort < 1 || tc.LocalPort > 65535 {
			return fmt.Errorf("本地端口须在 1–65535 之间")
		}
	case config.KindImport:
		if tc.ListenPort < 1 || tc.ListenPort > 65535 {
			return fmt.Errorf("监听端口须在 1–65535 之间")
		}
		if tc.PeerID == "" {
			return fmt.Errorf("未指定对端")
		}
		for i, ex := range existing {
			if i == skip || ex.Kind != config.KindImport {
				continue
			}
			if ex.ListenPort == tc.ListenPort {
				return fmt.Errorf("本地端口 %d 已被隧道「%s」占用",
					tc.ListenPort, ex.Name)
			}
		}
		// 试绑一次，把"端口被系统保留/被别的程序占用"提前暴露在这里。
		// 否则要等到隧道真正启动才失败，而那时错误只出现在日志里，
		// 用户刚点完"确定"、以为一切正常。
		// Probe-bind once so system reservations or another process's ownership are
		// reported here instead of surfacing only in logs after the user clicks OK.
		if err := tunnel.CheckListenPort(tc.ListenPort); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知的隧道类型: %s", tc.Kind)
	}
	return nil
}
