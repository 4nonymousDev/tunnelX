//go:build windows

package tunnel

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// occupantHint 查出占用指定本地端口的进程，返回可直接拼进错误消息的补充说明。
// 连接断开时本地 listener 会被关闭，端口可能
// 被其他程序抢占。若只报"端口 80 被占用"，用户不知道该去处理谁——"用户自行
// 处理"这一前提就无从谈起。
// 查不到时返回空串：这只是补充信息，查询失败不应干扰主错误。
// occupantHint identifies the process holding a local port and returns text suitable
// for an error message. A listener may be claimed after disconnect; naming the owner
// makes the problem actionable. Failure to inspect is nonfatal and returns an empty hint.
func occupantHint(port int) string {
	pid, ok := findListenerPID(uint16(port))
	if !ok {
		return ""
	}
	if pid == uint32(os.Getpid()) {
		return "（占用者是本程序自身）"
	}
	if name, ok := processName(pid); ok {
		return fmt.Sprintf("（已被 %s (PID %d) 占用）", name, pid)
	}
	return fmt.Sprintf("（已被 PID %d 占用）", pid)
}

// x/sys/windows 未导出 GetExtendedTcpTable，故直接绑定 iphlpapi.dll。
// x/sys/windows does not export GetExtendedTcpTable, so bind iphlpapi.dll directly.
var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

// TCP_TABLE_CLASS 取值：只列出处于 LISTEN 状态并带所属 PID 的连接。
// TCP_TABLE_CLASS value that lists only LISTEN sockets with their owning PID.
const tcpTableOwnerPIDListener = 3

// findListenerPID 在 TCP 表中查找监听该端口的进程。
// findListenerPID finds the process listening on the port in the TCP table.
func findListenerPID(port uint16) (uint32, bool) {
	var size uint32

	// 先以 nil 缓冲探测所需大小；预期返回 ERROR_INSUFFICIENT_BUFFER。
	// Probe the required size with a nil buffer; ERROR_INSUFFICIENT_BUFFER is expected.
	ret, _, _ := procGetExtendedTcpTable.Call(
		0, uintptr(unsafe.Pointer(&size)), 0,
		uintptr(windows.AF_INET), tcpTableOwnerPIDListener, 0,
	)
	if size == 0 {
		return 0, false
	}
	if ret != uintptr(syscall.ERROR_INSUFFICIENT_BUFFER) && ret != 0 {
		return 0, false
	}

	buf := make([]byte, size)
	ret, _, _ = procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
		uintptr(windows.AF_INET), tcpTableOwnerPIDListener, 0,
	)
	if ret != 0 {
		return 0, false
	}

	table := (*mibTCPTableOwnerPID)(unsafe.Pointer(&buf[0]))
	if table.NumEntries == 0 {
		return 0, false
	}

	// Table 声明为长度 1 的数组，实际长度为 NumEntries——须按地址取切片，
	// 不能按值访问。
	// Table is declared as a one-element array but actually contains NumEntries; create
	// a slice from its address rather than accessing it by value.
	rows := unsafe.Slice(&table.Table[0], table.NumEntries)
	for i := range rows {
		// 表中端口为网络字节序，存放在低 16 位。
		// Ports are network-byte-order values stored in the low 16 bits.
		if ntohs(uint16(rows[i].LocalPort)) == port {
			return rows[i].OwningPid, true
		}
	}
	return 0, false
}

func processName(pid uint32) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		// 权限不足时拿不到名字（如占用者以其他用户身份运行），
		// 此时仅返回 PID 也足以让用户定位。
		// Insufficient rights may hide the process name; the PID alone is still useful.
		return "", false
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", false
	}

	full := windows.UTF16ToString(buf[:size])
	// 只取文件名，完整路径对用户识别无益且过长。
	// Return only the file name; a full path is unnecessarily long and no easier to identify.
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == '\\' || full[i] == '/' {
			return full[i+1:], true
		}
	}
	return full, true
}

func ntohs(v uint16) uint16 {
	return v<<8 | v>>8
}

// MIB_TCPTABLE_OWNER_PID 的 Go 映射。x/sys/windows 未导出此结构。
// Go mapping of MIB_TCPTABLE_OWNER_PID, which x/sys/windows does not export.
type mibTCPTableOwnerPID struct {
	NumEntries uint32
	Table      [1]mibTCPRowOwnerPID // 变长数组，实际长度为 NumEntries / Variable-length array whose actual length is NumEntries.
}

type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}
