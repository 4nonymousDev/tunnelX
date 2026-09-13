package logbuf

import "fmt"

// sprintf 在无参数时跳过格式化，避免消息中的 % 被误解释。
// sprintf skips formatting without arguments so literal percent signs are not misinterpreted.
func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
