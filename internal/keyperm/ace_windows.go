//go:build windows

package keyperm

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// unsafePointerToSID 取出 ACCESS_ALLOWED_ACE 中内联存放的 SID。
// ACCESS_ALLOWED_ACE 的内存布局是「头部 + Mask + SID 起始字节」，Go 的结构体
// 定义中 SidStart 只是 SID 的第一个字节，实际 SID 长度可变并紧随其后。因此必须
// 按地址取，不能按值拷贝——拷贝只会得到 SID 的头 4 字节。
// unsafePointerToSID returns the SID stored inline in an ACCESS_ALLOWED_ACE.
// SidStart is only its first byte; the variable-length SID follows it in memory,
// so it must be addressed in place rather than copied as a value.
func unsafePointerToSID(ace *windows.ACCESS_ALLOWED_ACE) unsafe.Pointer {
	return unsafe.Pointer(uintptr(unsafe.Pointer(ace)) +
		unsafe.Offsetof(ace.SidStart))
}

// hiddenWindow 让 icacls 在后台运行，不弹出控制台窗口。
// 这是本工具沿袭现有脚本的一点：PowerShell 版靠 -WindowStyle Hidden 藏窗口
// （start_forwarding_dbg.ps1:23）。区别在于此处只藏一个短命的辅助进程，
// 而非把主进程连同其 stderr 一起藏掉——后者正是现有脚本排障困难的根源
// 。
// hiddenWindow runs icacls in the background without opening a console window.
// Unlike the legacy script, it hides only this short-lived helper and leaves the
// main process and its stderr visible for troubleshooting.
func hiddenWindow() *windows.SysProcAttr {
	return &windows.SysProcAttr{HideWindow: true}
}
