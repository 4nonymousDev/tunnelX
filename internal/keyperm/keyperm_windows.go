//go:build windows

package keyperm

import (
	"fmt"
	"os"

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

// Replace the DACL in one operation; never reset it to inherited broad access.
func fix(path string) error {
	sd, err := privateDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
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
