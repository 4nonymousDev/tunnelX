//go:build windows

package tunnel

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// TestClassifyRealReservedPortBind 用真实的绑定失败驱动分类，
// 而非构造假错误——WSAEACCES 的包装层次（net.OpError → os.SyscallError →
// syscall.Errno）一旦变化，构造出来的假错误就测不出问题。
//
// 1300 落在本机 netsh 报告的保留区间内。若某台机器上该端口可用，
// 测试会跳过而非误报。
// TestClassifyRealReservedPortBind classifies a real bind failure so changes in error
// wrapping are covered. Port 1300 is reserved locally; the test skips if it is available.
func TestClassifyRealReservedPortBind(t *testing.T) {
	const port = 1300

	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		ln.Close()
		t.Skipf("端口 %d 在本机可绑定，跳过（此测试需要一个被保留的端口）", port)
	}

	f := Classify(err)
	if f.Retryable {
		t.Error("端口被保留是配置问题，不应判为可重试")
	}

	t.Logf("实际文案: %s", f.Reason)

	// 关键：不能再把 Winsock 原文直接甩给用户。
	// Crucially, raw Winsock text must not be exposed to the user.
	if strings.Contains(f.Reason, "forbidden by its access permissions") {
		t.Error("仍在透出 Winsock 原文，用户无法据此行动")
	}
	if !strings.Contains(f.Reason, "被 Windows 保留") {
		t.Errorf("未说明真实原因（端口被保留），实际: %s", f.Reason)
	}
	if !strings.Contains(f.Reason, "netsh") {
		t.Error("未给出查看保留区间的具体命令")
	}
	if !strings.Contains(f.Reason, fmt.Sprint(port)) {
		t.Error("未指明是哪个端口")
	}
}

// TestExplainBindErrorPrivilegedPort 验证 1024 以下走「需要管理员」分支——
// 同为 WSAEACCES，但处置方式与保留端口完全不同，不可混为一谈。
// TestExplainBindErrorPrivilegedPort verifies that ports below 1024 require administrator
// rights, a WSAEACCES case distinct from reserved ports.
func TestExplainBindErrorPrivilegedPort(t *testing.T) {
	got := explainBindError(80, wsaeacces)
	if !strings.Contains(got, "管理员") {
		t.Errorf("1024 以下应提示管理员权限，实际: %s", got)
	}
	if strings.Contains(got, "被 Windows 保留") {
		t.Errorf("1024 以下不该说成端口被保留: %s", got)
	}
}

// TestExplainBindErrorReservedPort 验证 1024 以上的 WSAEACCES 明确指向保留区间，
// 并且不误导用户去用管理员身份重跑——那样做没有任何作用。
// TestExplainBindErrorReservedPort verifies that WSAEACCES above 1024 points to a
// reserved range without uselessly recommending administrator privileges.
func TestExplainBindErrorReservedPort(t *testing.T) {
	got := explainBindError(1300, wsaeacces)
	if !strings.Contains(got, "被 Windows 保留") {
		t.Errorf("实际: %s", got)
	}
	if !strings.Contains(got, "以管理员身份运行也没用") {
		t.Errorf("应明确否掉「用管理员重跑」这条错误直觉: %s", got)
	}
}

// TestExplainBindErrorInUse 验证端口占用给出可执行的排查命令。
// TestExplainBindErrorInUse verifies that port occupancy includes an actionable diagnostic command.
func TestExplainBindErrorInUse(t *testing.T) {
	got := explainBindError(8080, wsaeaddrinuse)
	if !strings.Contains(got, "已被其他程序占用") {
		t.Errorf("实际: %s", got)
	}
	if !strings.Contains(got, "netstat") {
		t.Error("未给出查出占用者的命令")
	}
}

// TestPortFromAddr 验证端口提取——explainBindError 靠它区分两种 WSAEACCES。
// TestPortFromAddr verifies extraction used to distinguish the two WSAEACCES cases.
func TestPortFromAddr(t *testing.T) {
	if got := portFromAddr(&net.TCPAddr{Port: 8080}); got != 8080 {
		t.Errorf("TCPAddr: got %d", got)
	}
	if got := portFromAddr(nil); got != 0 {
		t.Errorf("nil: got %d", got)
	}
}
