package tunnel

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"tunnelx/internal/proto"
)

// Fault 是一次失败，携带分类结果。
// 分类的唯一判据是「能否靠等待自行恢复」。
// Fault is a classified failure. Classification asks only whether waiting can recover it.
type Fault struct {
	Err       error
	Retryable bool
	// Reason 是给用户看的原因，会直接显示在 UI 的错误状态旁。
	// 必须具体到可据以行动——现有脚本最大的问题就是只有退出码，
	// 排障只能靠 ping 猜。
	// Reason is shown directly beside the UI error state and must be actionable; the old
	// script exposed only an exit code, forcing users to guess with ping.
	Reason string
}

func (f *Fault) Error() string { return f.Reason }
func (f *Fault) Unwrap() error { return f.Err }

func retryable(err error, format string, args ...any) *Fault {
	return &Fault{Err: err, Retryable: true, Reason: fmt.Sprintf(format, args...)}
}

func fatal(err error, format string, args ...any) *Fault {
	return &Fault{Err: err, Retryable: false, Reason: fmt.Sprintf(format, args...)}
}

// NotConfigured 构造「配置缺项」类失败。
// 这类失败必然不可自愈：靠等待永远等不来一个用户没填的值，
// 重试只会刷日志。故一律判为 fatal，让 UI 直接显示错误并停下。
//
// 取 msg 而非 format+args：缺项提示都是固定文案，且含「」等标点，
// 走 Sprintf 只会让某天有人传入带 % 的消息时得到 %!f(MISSING)。
// NotConfigured constructs a missing-configuration failure. Waiting cannot supply an
// omitted value, so it is fatal and stops noisy retries. It accepts a fixed message,
// not format arguments, to avoid accidental Sprintf interpretation of percent signs.
func NotConfigured(msg string) *Fault {
	return &Fault{Err: errors.New(msg), Retryable: false, Reason: msg}
}

// Classify 判定一个错误是否应当重试。
// 归类原则：只有确知不可自愈的才判为 fatal。未知错误默认可重试——误判为可重试
// 的代价只是多几次无用退避，误判为致命则会让本可自愈的隧道永久停摆。
// Classify decides whether an error should be retried. Only known unrecoverable errors
// are fatal; an unknown error defaults to retryable because extra backoff is safer than
// permanently stopping a tunnel that could recover.
func Classify(err error) *Fault {
	if err == nil {
		return nil
	}

	var f *Fault
	if errors.As(err, &f) {
		return f
	}

	// 服务端返回的错误自带机读分类，直接采信。
	// Trust the machine-readable classification supplied by the server.
	var perr *proto.Error
	if errors.As(err, &perr) {
		return &Fault{
			Err:       err,
			Retryable: proto.Retryable(perr.Code),
			Reason:    serverReason(perr),
		}
	}

	// 主机密钥不匹配：可能是中间人攻击，绝不能自动重试或与首次连接拒绝混淆。
	// A host-key mismatch may be a MITM attack; never retry it or confuse it with first-contact rejection.
	var mismatchErr *hostKeyMismatchError
	if errors.As(err, &mismatchErr) {
		return fatal(err, "主机密钥与记录不符，可能存在中间人攻击。如服务器确已更换密钥，请手动删除 known_hosts 中的对应条目")
	}

	// 首次连接时用户没有信任主机密钥。它同样不可自动重试，但不代表
	// known_hosts 中存在冲突记录，更不应引导用户删除该文件。
	// Rejecting an unknown key on first contact is also non-retryable, but does not imply
	// a conflicting known_hosts entry and must not tell the user to delete one.
	var rejectedErr *hostKeyRejectedError
	if errors.As(err, &rejectedErr) {
		return fatal(err, "未信任服务器主机密钥，连接已取消。请重新连接并在核对指纹后选择信任")
	}

	// 私钥文件问题：不改配置永远不会好。
	// Private-key file problems cannot recover without a configuration change.
	if errors.Is(err, os.ErrNotExist) {
		return fatal(err, "私钥文件不存在")
	}
	if errors.Is(err, os.ErrPermission) {
		return fatal(err, "无权读取私钥文件")
	}

	// 本地端口绑定失败：需用户处理。
	// Winsock 的原文（"forbidden by its access permissions"）会把人引向
	// "用管理员身份重跑"，而真正的原因多半是端口落在 Windows 的保留区间内，
	// 那样跑多少次也没用——故翻译成可据以行动的说明，见 explainBindError。
	// Local bind failures require user action. Raw Winsock wording often wrongly suggests
	// administrator rights when Windows reserved the port, so explainBindError provides
	// actionable guidance instead.
	var addrErr *net.OpError
	if errors.As(err, &addrErr) && addrErr.Op == "listen" {
		return fatal(err, "本地端口绑定失败：%s",
			explainBindError(portFromAddr(addrErr.Addr), addrErr.Err))
	}

	// EOF / 连接被关闭：对端断开，属可自愈。
	// EOF or closure means the peer disconnected and is recoverable.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return retryable(err, "连接已断开")
	}

	// 网络层错误绝大多数可自愈（网络恢复、服务器重启后即好）。
	// Most network-layer errors recover when the network or server returns.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return retryable(err, "连接超时")
		}
		return retryable(err, "网络错误：%v", err)
	}

	// SSH 认证被拒。x/crypto/ssh 未为认证失败定义可断言的错误类型，只能匹配
	// 文本——因此放在所有结构化判断之后作为兜底。文本一旦随库版本变化，此处
	// 会退化为"可重试"，届时表现为认证失败被反复重试，需据此排查。
	// x/crypto/ssh has no typed authentication-rejection error, so this final fallback
	// matches text after all structured checks. Library wording changes would degrade it
	// to retryable and manifest as repeated authentication attempts.
	msg := err.Error()
	if strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") {
		return fatal(err, "认证被拒绝：私钥未被服务器授权，请确认公钥已加入服务器的 authorized_keys")
	}
	if strings.Contains(msg, "ssh: handshake failed") {
		return retryable(err, "SSH 握手失败：%v", err)
	}

	return retryable(err, "%v", err)
}

// portFromAddr 从地址中取出端口号，取不到时返回 0。
// explainBindError 需要端口来区分「1024 以下需管理员」与「落在保留区间」
// 这两种同为 WSAEACCES 却处置迥异的情形。
// portFromAddr extracts a port or returns zero. explainBindError uses it to distinguish
// privileged ports from reserved ranges, two WSAEACCES cases requiring different action.
func portFromAddr(a net.Addr) int {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.Port
	}
	if a == nil {
		return 0
	}
	_, portStr, err := net.SplitHostPort(a.String())
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

func serverReason(e *proto.Error) string {
	switch e.Code {
	case proto.CodeVersionMismatch:
		return "协议版本不兼容，请升级客户端"
	case proto.CodeDuplicateID:
		return "本机标识已被另一连接占用（是否复制了 config.json？）"
	default:
		if e.Msg != "" {
			return fmt.Sprintf("服务端错误：%s", e.Msg)
		}
		return fmt.Sprintf("服务端错误：%s", e.Code)
	}
}

// hostKeyMismatchError 标记 known_hosts 已有记录与服务器当前密钥不一致。
// 保持该类型独立，避免把首次连接时的拒绝误报为中间人攻击。
// hostKeyMismatchError marks a stored host key that differs from the server's current key;
// a separate type prevents first-contact rejection from being reported as a MITM attack.
type hostKeyMismatchError struct{ Err error }

func (e *hostKeyMismatchError) Error() string { return e.Err.Error() }
func (e *hostKeyMismatchError) Unwrap() error { return e.Err }

// hostKeyRejectedError 标记首次连接的主机密钥未获用户信任。
// hostKeyRejectedError marks a first-contact host key the user did not trust.
type hostKeyRejectedError struct{ Err error }

func (e *hostKeyRejectedError) Error() string { return e.Err.Error() }
func (e *hostKeyRejectedError) Unwrap() error { return e.Err }

// WrapHostKeyMismatch 将真实的主机密钥冲突标记为不可自愈。
// WrapHostKeyMismatch marks an actual host-key conflict as unrecoverable.
func WrapHostKeyMismatch(err error) error { return &hostKeyMismatchError{Err: err} }

// WrapHostKeyRejected 将首次连接未获信任标记为不可自愈。
// WrapHostKeyRejected marks an untrusted first-contact key as unrecoverable.
func WrapHostKeyRejected(err error) error { return &hostKeyRejectedError{Err: err} }
