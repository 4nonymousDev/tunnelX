//go:build !windows

package tunnel

import (
	"errors"
	"fmt"
	"syscall"
)

// explainBindError 在非 Windows 平台只补充「需要 root」这一条——
// 其余情形 Unix 的错误文本（address already in use 等）本身已足够清楚，
// 无需改写。
// On non-Windows platforms explainBindError only clarifies that privileged ports need
// root; ordinary Unix errors such as "address already in use" are already clear.
func explainBindError(port int, err error) string {
	var se syscall.Errno
	if errors.As(err, &se) && se == syscall.EACCES && port < 1024 {
		return fmt.Sprintf("本地端口 %d 需要 root 权限（1024 以下为特权端口）。"+
			"请改用 1024 以上的端口。", port)
	}
	return fmt.Sprintf("%v", err)
}
