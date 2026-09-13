//go:build windows

package keyperm

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

// 无需关注的内建主体：这些主体持有访问权是正常且不可避免的。
// Built-in principals whose access is expected and unavoidable.
var benignSIDs = map[string]bool{
	"S-1-5-18":     true, // NT AUTHORITY\SYSTEM
	"S-1-5-32-544": true, // BUILTIN\Administrators —— 管理员本就能取得任何文件的所有权 / Administrators can already take ownership of any file.
	// BUILTIN\Administrators can already take ownership of any file.
}

func check(path string) Result {
	if _, err := os.Stat(path); err != nil {
		return Result{Err: fmt.Errorf("读取私钥文件: %w", err)}
	}

	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return Result{Err: fmt.Errorf("读取文件权限: %w", err)}
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return Result{Err: fmt.Errorf("读取访问控制列表: %w", err)}
	}
	if dacl == nil {
		// NULL DACL 表示人人可访问，是最坏的情况。
		// A NULL DACL grants access to everyone and is the worst case.
		return Result{OK: false, Readers: []string{"Everyone（文件无访问控制）"}}
	}

	self, err := currentUserSID()
	if err != nil {
		return Result{Err: err}
	}

	var readers []string
	seen := map[string]bool{}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			// 单条 ACE 读取失败不应中断整体检查。
			// Failure to read one ACE must not abort the entire check.
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			// 只关心"允许"型 ACE；拒绝型不构成泄露风险。
			// Only allow ACEs matter; deny ACEs do not create an exposure risk.
			continue
		}

		sid := (*windows.SID)(unsafePointerToSID(ace))
		sidStr := sid.String()

		if sidStr == self || benignSIDs[sidStr] || seen[sidStr] {
			continue
		}
		// 只在实际具备读权限时才计入。
		// Count the principal only when it actually has read access.
		if ace.Mask&windows.GENERIC_READ == 0 &&
			ace.Mask&windows.FILE_GENERIC_READ == 0 &&
			ace.Mask&windows.GENERIC_ALL == 0 {
			continue
		}

		seen[sidStr] = true
		readers = append(readers, describeSID(sid))
	}

	return Result{OK: len(readers) == 0, Readers: readers}
}

// fix 调用 icacls 重建权限。
// 用 icacls 而非直接操作 ACL API：这一串操作（重置、断继承、授权）用 API 实现
// 需要构造 ACL 结构、处理继承标志，代码量与出错面都远大于三条命令；且 icacls
// 是 Windows 内建组件，无外部依赖。
// fix rebuilds permissions with icacls. Using the built-in tool keeps the reset,
// inheritance, and grant sequence smaller and less error-prone than constructing ACLs directly.
func fix(path string) error {
	self, err := currentUserName()
	if err != nil {
		return err
	}

	steps := []struct {
		desc string
		args []string
	}{
		// /reset 清除现有显式 ACE，回到仅继承状态
		// /reset removes explicit ACEs and returns to inherited permissions only.
		{"重置权限", []string{path, "/reset"}},
		// /inheritance:r 断开继承并移除继承来的 ACE
		// /inheritance:r disables inheritance and removes inherited ACEs.
		{"断开权限继承", []string{path, "/inheritance:r"}},
		// 仅授予当前用户与 SYSTEM 完全控制
		// Grant full control only to the current user and SYSTEM.
		{"授权当前用户", []string{path, "/grant:r", self + ":F"}},
		{"授权 SYSTEM", []string{path, "/grant:r", "*S-1-5-18:F"}},
	}

	for _, s := range steps {
		cmd := exec.Command("icacls", s.args...)
		cmd.SysProcAttr = hiddenWindow()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s失败: %w (%s)", s.desc, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func currentUserSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("获取当前用户: %w", err)
	}
	return user.User.Sid.String(), nil
}

func currentUserName() (string, error) {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("获取当前用户: %w", err)
	}
	account, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		// 查不到账户名时退回 SID 字面量，icacls 同样接受 *S-1-... 形式
		// Fall back to the SID literal when lookup fails; icacls also accepts *S-1-... form.
		return "*" + user.User.Sid.String(), nil
	}
	if domain == "" {
		return account, nil
	}
	return domain + "\\" + account, nil
}

// describeSID 尽量返回 "DOMAIN\Account (S-1-...)" 形式。
// 报错必须精确到"是谁能读"，用户才知道该处理什么。
// describeSID returns "DOMAIN\Account (S-1-...)" when possible.
// Errors must identify exactly who can read the key so the user knows what to fix.
func describeSID(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	if domain == "" {
		return fmt.Sprintf("%s (%s)", account, sid.String())
	}
	return fmt.Sprintf("%s\\%s (%s)", domain, account, sid.String())
}
