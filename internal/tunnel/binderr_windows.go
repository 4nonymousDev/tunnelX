//go:build windows

package tunnel

import (
	"errors"
	"fmt"
	"syscall"
)

// Winsock 错误码。x/sys/windows 有同名常量，但本包只需这三个，
// 且用 syscall.Errno 比较即可，不值得为此引入依赖。
// Winsock error codes. This package needs only these values and can compare them as
// syscall.Errno, so importing x/sys/windows solely for its constants is unnecessary.
const (
	wsaeacces     syscall.Errno = 10013 // WSAEACCES：权限不足或端口被系统保留 / Permission denied or system-reserved port.
	wsaeaddrinuse syscall.Errno = 10048 // WSAEADDRINUSE：端口已被占用 / Port already in use.
)

// explainBindError 把 Windows 上晦涩的绑定错误翻译成可据以行动的说明。
//
// 直接透出 Winsock 原文会误导用户：WSAEACCES 的字面意思是「权限不足」，
// 于是人们第一反应是用管理员身份重跑——但绝大多数情况下真正的原因是该端口
// 落在了 Windows 的保留区间内（Hyper-V / WSL / Docker 会预留成片端口），
// 那时以管理员身份运行同样绑不上。
// explainBindError turns obscure Windows bind errors into actionable guidance.
// Raw WSAEACCES text suggests insufficient privileges, but it usually means Hyper-V,
// WSL, or Docker reserved the port range, which administrator privileges cannot fix.
func explainBindError(port int, err error) string {
	var se syscall.Errno
	if !errors.As(err, &se) {
		return fmt.Sprintf("%v", err)
	}

	switch se {
	case wsaeaddrinuse:
		return fmt.Sprintf("本地端口 %d 已被其他程序占用。"+
			"可在命令行执行 netstat -ano | findstr :%d 查出占用者，"+
			"或在设置里改用其他端口。", port, port)

	case wsaeacces:
		// 1024 以下属系统保留，需要管理员权限——这一条确实是权限问题。
		// Ports below 1024 are system-reserved and genuinely require administrator rights.
		if port < 1024 {
			return fmt.Sprintf("本地端口 %d 需要管理员权限（1024 以下为系统保留端口）。"+
				"请以管理员身份运行，或改用 1024 以上的端口。", port)
		}
		return fmt.Sprintf("本地端口 %d 被 Windows 保留，无法绑定"+
			"（Hyper-V / WSL / Docker 会预留成片端口，此时以管理员身份运行也没用）。"+
			"可在命令行执行 netsh interface ipv4 show excludedportrange protocol=tcp "+
			"查看保留区间，改用区间之外的端口。", port)
	}

	return fmt.Sprintf("%v", err)
}
