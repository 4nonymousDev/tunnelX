// Package proto 定义控制通道的消息格式。
// 编码为 JSON Lines：每条消息一行 JSON，\n 分隔。SSH channel 提供的是字节流
// 而非消息流，必须自行分帧，否则会读到粘包/半包。
// Package proto defines the control-channel message format.
// Messages use JSON Lines: one JSON object per line, separated by \n. An SSH channel
// provides a byte stream rather than a message stream, so explicit framing is required
// to avoid combined or partial messages.
package proto

import (
	"fmt"
	"time"
)

// ChannelType 是控制通道的 SSH channel 类型名。
// ChannelType is the SSH channel type name used by the control channel.
const ChannelType = "tunnel-ctrl@tunnelx"

// Version 是当前协议版本。连接后第一条消息即交换版本，不匹配立即报错终止。
// Version is the current protocol version. Peers exchange it in the first message and
// terminate immediately on a mismatch.
const Version = 2

const (
	OpenChannelType    = "tunnel-open-v2@tunnelx"
	ForwardChannelType = "tunnel-forward-v2@tunnelx"
	MaxOpenBytes       = 4096
)

// Target identifies one committed publication on one authenticated SSH session.
type Target struct {
	ClientID    string `json:"client_id"`
	Fingerprint string `json:"fingerprint"`
	TunnelID    string `json:"tunnel_id"`
	SessionID   string `json:"session_id"`
	Generation  uint64 `json:"generation"`
}

type OpenRequest struct {
	V      int    `json:"v"`
	Target Target `json:"target"`
}

func (t Target) Valid() bool {
	return t.ClientID != "" && t.Fingerprint != "" && t.TunnelID != "" && t.SessionID != "" && t.Generation > 0
}

// 消息类型。
// Message types.
const (
	TypeHello     = "hello"
	TypeHelloOK   = "hello_ok"
	TypePublish   = "publish"
	TypePublishOK = "publish_ok"
	TypeRegistry  = "registry"
	TypeError     = "error"
)

// 客户端角色。
// Client roles.
const (
	RoleExporter = "exporter"
	RoleImporter = "importer"
)

// Envelope 用于窥探消息类型，再按 Type 解码为具体结构。
// Envelope identifies the message type before decoding it into a concrete structure.
type Envelope struct {
	V    int    `json:"v"`
	Type string `json:"type"`
}

// Hello 是连接后的第一条消息（C→S）。
// Importer 同样要发送 Hello，尽管它只是注册表的消费者——成本为零（消息本来就要
// 发），且让服务端得以知道当前有哪些 Importer 在线，为将来的审计留下基础。
// Hello is the first message sent after connecting (client to server).
// Importers also send Hello even though they only consume the registry. It adds no
// meaningful cost and lets the server track online importers for future auditing.
type Hello struct {
	V             int    `json:"v"`
	Type          string `json:"type"`
	Role          string `json:"role"` // RoleExporter | RoleImporter
	ID            string `json:"id"`   // UUID
	Name          string `json:"name"` // 显示名，默认取计算机名 / Display name, defaulting to the hostname.
	ClientVersion string `json:"client_version"`
}

// HelloOK 是握手成功响应（S→C）。
// HelloOK is the successful handshake response (server to client).
type HelloOK struct {
	V             int    `json:"v"`
	Type          string `json:"type"`
	ServerVersion string `json:"server_version"`
	SessionID     string `json:"session_id"`
	Fingerprint   string `json:"fingerprint"`
}

// TunnelSpec 是 Exporter 上报的单条隧道。
// TunnelSpec describes one tunnel reported by an exporter.
type TunnelSpec struct {
	TunnelID   string `json:"tunnel_id,omitempty"` // 导出隧道稳定 ID；新版本的匹配依据 / Stable exported-tunnel ID used for matching.
	SrcHost    string `json:"src_host,omitempty"`  // 本地目标地址，仅供识别与展示 / Local target address for identification and display only.
	SrcPort    int    `json:"src_port"`            // 本地源端口 / Local source port.
	RemotePort int    `json:"remote_port"`         // Deprecated wire field; must be zero in v2.
	Name       string `json:"name"`                // 隧道名，可选；空则 UI 显示端口号 / Optional name; the UI falls back to the port.
}

// Publish atomically replaces the session's exported tunnel metadata.
// The server returns exact session-bound targets in PublishOK before the
// exporter accepts forwarding channels for a newly committed generation.
type Publish struct {
	V       int          `json:"v"`
	Type    string       `json:"type"`
	Tunnels []TunnelSpec `json:"tunnels"`
}

// PublishOK 是上报成功响应（S→C）。
// PublishOK acknowledges a successful report (server to client).
type PublishOK struct {
	V       int      `json:"v"`
	Type    string   `json:"type"`
	Targets []Target `json:"targets"`
}

// RegistryEntry 是注册表中的一条在线隧道。
// RegistryEntry describes one online tunnel in the registry.
type RegistryEntry struct {
	Fingerprint      string    `json:"fingerprint"`
	SessionID        string    `json:"session_id"`
	Generation       uint64    `json:"generation"`
	IdentityVerified bool      `json:"identity_verified"`
	ID               string    `json:"id"`                  // Exporter UUID，重连匹配依据 / Exporter UUID used to match reconnects.
	Name             string    `json:"name"`                // 机器显示名 / Machine display name.
	TunnelID         string    `json:"tunnel_id,omitempty"` // 导出隧道稳定 ID / Stable exported-tunnel ID.
	SrcHost          string    `json:"src_host,omitempty"`  // Exporter 的目标地址 / Exporter's target address.
	SrcPort          int       `json:"src_port"`            // 显示、Importer 默认值与旧配置兼容匹配 / Display, importer default, and legacy match port.
	RemotePort       int       `json:"remote_port"`         // Deprecated display field; always zero in v2.
	TunnelName       string    `json:"tunnel_name"`         // 显示用，可为空 / Optional display name.
	Since            time.Time `json:"since"`               // 上线时间 / Time connected.
	ClientVersion    string    `json:"client_version"`
}

func (e RegistryEntry) Target() Target {
	return Target{ClientID: e.ID, Fingerprint: e.Fingerprint, TunnelID: e.TunnelID, SessionID: e.SessionID, Generation: e.Generation}
}

// Registry 是服务端下发的注册表（S→C，全量快照）。
// Importer 握手后收到一次，此后每次注册表变化服务端主动广播。因 Importer 不上报
// "正在连谁"，故为广播而非精准推送——各 Importer 自行比对"我连的 UUID 还在吗"。

// Registry is the full registry snapshot sent by the server (server to client).
// An importer receives it after the handshake and whenever the registry changes.
// Because importers do not report which peer they use, the server broadcasts and each
// importer checks whether its target UUID remains present.
type Registry struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	Entries []RegistryEntry `json:"entries"`
}

// Error 是错误响应（S→C）。
// 必须带机读的 Code：服务端错误同样要参与失败分类，客户端据此
// 判断"重试还是停下报错"。若靠匹配 Msg 文本判断，服务端改个措辞就会让分类逻辑
// 静默失效。
// 实现 error 接口，使其可经 fmt.Errorf("%w") 包装后被 errors.As 取回，
// 从而在失败分类中直接采信服务端给出的 Code。
// Error is an error response (server to client).
// It must include a machine-readable Code so the client can classify whether to retry
// or stop. Matching Msg text would silently break if server wording changed. Error
// implements the error interface so errors.As can recover it through %w wrapping and
// classification can trust the server-provided Code directly.
type Error struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Msg)
	}
	return e.Code
}

// 错误码。
// Error codes. In order: version mismatch and duplicate ID are not recoverable;
// port-allocation failure and server busy are retryable; bad request indicates a
// client bug and is not recoverable; internal errors are retryable.
const (
	CodeVersionMismatch    = "version_mismatch"  // 不可自愈：提示用户升级 / Unrecoverable: ask the user to upgrade.
	CodeDuplicateID        = "duplicate_id"      // 不可自愈：UUID 已被占用 / Unrecoverable: UUID already in use.
	CodePortAllocFailed    = "port_alloc_failed" // 可自愈：退避重试 / Recoverable: retry with backoff.
	CodeServerBusy         = "server_busy"       // 可自愈：退避重试 / Recoverable: retry with backoff.
	CodeBadRequest         = "bad_request"       // 不可自愈：客户端 bug / Unrecoverable: client bug.
	CodeInternalError      = "internal_error"    // 可自愈：退避重试 / Recoverable: retry with backoff.
	CodeIdentityUnverified = "identity_unverified"
	CodeIdentityMismatch   = "identity_mismatch"
	CodeAmbiguousTarget    = "ambiguous_target"
	CodePeerOffline        = "peer_offline"
	CodeStaleTarget        = "stale_target"
	CodePublishTooLarge    = "publish_too_large"
	CodeRegistryCapacity   = "registry_capacity"
)

// Retryable 报告该错误码是否可通过重试自愈。
// Retryable reports whether retrying can recover from the error code.
func Retryable(code string) bool {
	switch code {
	case CodePortAllocFailed, CodeServerBusy, CodeInternalError, CodePeerOffline, CodeStaleTarget, CodeIdentityUnverified:
		return true
	default:
		return false
	}
}
