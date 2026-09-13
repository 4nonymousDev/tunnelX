package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tunnelx/internal/config"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/tunnel"
)

// TestConnectRejectsMissingSettings 验证三处配置缺项都在连接前被拦下，
// 且判为不可重试。
//
// 两条都要紧：
//   - 拦下：空用户名此前无人校验，会完成整个 SSH 握手直到服务器拒绝，
//     再被报成"认证被拒绝：私钥未被授权"，与真正的原因南辕北辙。
//   - 不可重试：配置缺项靠等待永远不会好，此前走 Classify 兜底被判为
//     可重试，日志里会无限刷"N 秒后重试"，退避一路涨到几分钟。
//
// TestConnectRejectsMissingSettings verifies that three missing settings are rejected
// before connecting and classified as non-retryable.
//
// Both properties matter:
//   - Early rejection: an empty username otherwise completes the SSH handshake before
//     producing a misleading "private key unauthorized" error.
//   - Non-retryable: waiting cannot repair missing configuration. Treating it as retryable
//     would endlessly log retries with backoff growing to minutes.
func TestConnectRejectsMissingSettings(t *testing.T) {
	cases := []struct {
		name       string
		addr, user string
		keyPath    string
		wantSubstr string
	}{
		{"缺服务器地址", "", "m1", "tunnel_key", "尚未配置服务器地址"},
		{"缺用户名", "1.2.3.4:2222", "", "tunnel_key", "尚未配置用户名"},
		{"缺私钥路径", "1.2.3.4:2222", "m1", "", "尚未配置私钥路径"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()

			// 私钥必须真实存在：checkKeyPerm 排在地址/用户名检查之前，
			// 私钥不存在会先一步报错，掩盖掉本用例要验证的那个 guard。
			// The private key must exist because checkKeyPerm runs before address/username
			// validation; otherwise its error would mask the guard under test.
			keyPath := c.keyPath
			if keyPath != "" {
				keyPath = filepath.Join(dir, keyPath)
				if err := os.WriteFile(keyPath, []byte("dummy"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			cfg := &config.Config{
				ID:         "test-id",
				Name:       "测试机",
				ServerAddr: c.addr,
				ServerUser: c.user,
				KeyPath:    keyPath,
			}
			cfg.SetPathForTest(filepath.Join(dir, config.FileName))

			m := New(cfg, logbuf.New(nil), "test", func() {})

			err := m.connectAndServe(context.Background())
			if err == nil {
				t.Fatal("配置缺项时应返回错误")
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Errorf("错误 = %q, 应包含 %q", err, c.wantSubstr)
			}

			// 关键：必须判为不可重试，否则 loop 会无限退避重试。
			// It must be non-retryable or loop will retry forever with backoff.
			fault := tunnel.Classify(err)
			if fault.Retryable {
				t.Errorf("配置缺项被判为可重试——会导致无限重试一个永远不会好的问题")
			}
		})
	}
}
