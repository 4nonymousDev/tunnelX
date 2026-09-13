package tunnel

import "time"

// State 是隧道的 UI 可见状态。
// State is the tunnel state exposed to the UI.
type State int

const (
	// StateStopped 用户主动停止。重连后不自动拉起——尊重用户意图。
	// StateStopped means the user explicitly stopped it; reconnects respect that intent.
	StateStopped State = iota
	// StateRunning 一切正常。
	// StateRunning means normal operation.
	StateRunning
	// StateReconnecting 可自愈失败，退避重试中。UI 显示"N 秒后重试"。
	// StateReconnecting is a recoverable failure under backoff; the UI shows a countdown.
	StateReconnecting
	// StateError 不可自愈，已放弃，需用户处理。UI 并列显示原因。
	// StateError is unrecoverable and requires user action; the UI displays the reason.
	StateError
	// StatePeerOffline 对端 Exporter 离线（仅 Import 模式）。
	// 本地连接与服务器均正常，是对面那台机器掉线了。区分此状态是为了让用户
	// 一眼看出问题不在自己这边。
	// StatePeerOffline means the peer exporter is offline in Import mode. The local
	// connection and server are healthy, making clear that the fault is remote.
	StatePeerOffline
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "已停止"
	case StateRunning:
		return "运行中"
	case StateReconnecting:
		return "重连中"
	case StateError:
		return "错误"
	case StatePeerOffline:
		return "对端离线"
	default:
		return "未知"
	}
}

// Status 是隧道对外暴露的完整状态快照。
// Status is the complete externally visible tunnel-state snapshot.
type Status struct {
	State State
	// Reason 在 StateError / StatePeerOffline 时说明原因，直接显示给用户。
	// Reason is shown directly to the user for StateError and StatePeerOffline.
	Reason string
	// RetryAt 在 StateReconnecting 时给出下次重试时刻，UI 据此显示倒计时。
	// RetryAt is the next retry time used by the UI countdown in StateReconnecting.
	RetryAt time.Time
	// RemotePort 是服务端为 Export 隧道分配的实际端口。
	// 分配前为 0。
	// RemotePort is the actual server port assigned to an Export tunnel, or zero before assignment.
	RemotePort int
}
