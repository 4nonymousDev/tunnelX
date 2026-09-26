// Package updatepolicy defines the server's optional minimum client version.
// Version reports are compatibility information, not proof of binary integrity.
package updatepolicy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const DefaultGracePeriod = 7 * 24 * time.Hour

type Policy struct {
	MinimumVersion string     `json:"minimum_version"`
	EnforceAfter   *time.Time `json:"enforce_after"`
	Message        string     `json:"message"`
	Generation     int64      `json:"generation"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type Status struct {
	Policy
	Required   bool      `json:"required"`
	Blocked    bool      `json:"blocked"`
	ServerTime time.Time `json:"server_time"`
}

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func parseVersion(value string) ([3]uint64, bool, bool) {
	var out [3]uint64
	if len(value) > 100 {
		return out, false, false
	}
	m := versionPattern.FindStringSubmatch(value)
	if m == nil {
		return out, false, false
	}
	for i := range out {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return out, false, false
		}
		out[i] = n
	}
	for _, part := range strings.Split(m[4], ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return out, false, false
		}
	}
	return out, m[4] != "", true
}

// The minimum must be a stable, three-component release. Client prereleases
// compare below the corresponding release; build metadata has no precedence.
func ValidMinimum(value string) bool {
	_, pre, ok := parseVersion(value)
	return ok && !pre && !strings.HasPrefix(value, "v") && !strings.Contains(value, "+")
}

func AtLeast(client, minimum string) bool {
	v, pre, ok := parseVersion(client)
	m, _, valid := parseVersion(minimum)
	if !ok || !valid {
		return false
	}
	for i := range v {
		if v[i] != m[i] {
			return v[i] > m[i]
		}
	}
	return !pre
}

func (p Policy) Validate() error {
	if !utf8.ValidString(p.Message) || utf8.RuneCountInString(p.Message) > 500 {
		return fmt.Errorf("更新说明最多500个字符")
	}
	if p.MinimumVersion == "" {
		if p.EnforceAfter != nil || p.Message != "" {
			return fmt.Errorf("关闭策略时须清空版本、期限和说明")
		}
		return nil
	}
	if !ValidMinimum(p.MinimumVersion) {
		return fmt.Errorf("最低版本须为正式版本号，例如0.2.1")
	}
	if p.EnforceAfter == nil || p.EnforceAfter.Year() < 2020 || p.EnforceAfter.Year() > 2100 {
		return fmt.Errorf("请设置有效的强制更新期限")
	}
	return nil
}

func (p Policy) Status(client string, now time.Time) *Status {
	if p.MinimumVersion == "" {
		return nil
	}
	required := !AtLeast(client, p.MinimumVersion)
	return &Status{Policy: p, Required: required, Blocked: required && p.EnforceAfter != nil && !now.Before(*p.EnforceAfter), ServerTime: now.UTC()}
}

func (s Status) Reason() string {
	if s.Blocked {
		return fmt.Sprintf("当前客户端版本已停止接入，请升级到 %s 或更高版本；原密钥和配置继续使用。%s", s.MinimumVersion, s.Message)
	}
	if s.EnforceAfter == nil {
		return ""
	}
	return fmt.Sprintf("请在 %s 前升级到 %s 或更高版本，期限内可以继续使用。%s", s.EnforceAfter.Local().Format("2006-01-02 15:04 MST"), s.MinimumVersion, s.Message)
}
