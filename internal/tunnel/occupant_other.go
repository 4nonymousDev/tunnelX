//go:build !windows

package tunnel

// 非 Windows 平台不查占用者：目标平台是 Windows，
// 此处仅为让代码在开发者的其他平台上可编译。
// Non-Windows platforms do not inspect the owning process. Windows is the target;
// this stub merely keeps the package buildable on developers' other platforms.
func occupantHint(port int) string { return "" }
