// Package keyperm 检查并修复私钥文件的访问权限。
// 背景：ssh.exe 会在读取私钥前检查文件 ACL，权限过宽时拒绝加载并打印
// "UNPROTECTED PRIVATE KEY FILE"。golang.org/x/crypto/ssh 不做此检查，
// 因此该报错在本项目中不会出现——但它警告的风险是真实的：
//
//	Authenticated Users 可读意味着本机任何登录用户都能拷走私钥，
//	进而以你的身份连上服务器。
//
// 本项目中该风险被放大：exe 设计为"拷过去双击就用"，很可能落在默认 ACL
// 宽松的目录；目标机是临时的/别人的开发机，可能确有其他用户；而这把私钥
// 通向公网服务器，服务器背后是整个内网。
// 且私钥与 exe 同目录一并拷贝，本检查是私钥泄露的唯一防线。
// 因此本包自行检查，但做得比 OpenSSH 好用：OpenSSH 只会拒绝然后甩一句
// "Try removing permissions for..."；本项目有 GUI，直接提供一键修复。
// Package keyperm checks and repairs access permissions on private-key files.
// Background: ssh.exe checks a private key's ACL before reading it and rejects overly
// broad permissions with "UNPROTECTED PRIVATE KEY FILE". golang.org/x/crypto/ssh does
// not perform this check, but the risk remains real: any local user with read access
// can copy the key and connect as its owner. This project commonly runs from copied,
// permissive directories on shared machines, and the key reaches a public server and
// the private network behind it. The package therefore performs its own check and
// offers a one-click GUI repair instead of only reporting an OpenSSH-style error.
package keyperm

// Result 是权限检查结果。
// Result describes a permission check.
type Result struct {
	// OK 为 true 表示权限安全，可直接放行。
	// OK means the permissions are safe and the key may be used.
	OK bool
	// Readers 列出除当前用户与 SYSTEM 之外的可读主体，用于在提示中告诉用户
	// "谁能读到这把私钥"。
	// Readers lists readable principals other than the current user and SYSTEM for a useful warning.
	Readers []string
	// Err 非 nil 表示检查本身失败（如文件不存在、API 调用出错）。
	// 检查失败不应阻断连接——放行并在日志中留痕即可。
	// Err reports a failure of the check itself; such failures are logged but do not block a connection.
	Err error
}

// Check 检查私钥文件的访问权限。
// Windows 检查 ACL；Unix 平台检查组用户与其他用户的权限位。
// Check inspects the private-key file permissions: ACLs on Windows and group/other mode bits on Unix.
func Check(path string) Result {
	return check(path)
}

// Fix 收紧私钥文件权限：断开继承，仅保留当前用户与 SYSTEM 的访问权。
// 对应 UI 提示中的"自动修复权限"按钮——一次点击解决，用户无需自行查
// icacls 命令。
// Fix tightens the permissions by disabling inheritance and retaining access only for the current user and SYSTEM.
// It backs the UI's one-click repair button, so users do not need to invoke icacls themselves.
func Fix(path string) error {
	return fix(path)
}
