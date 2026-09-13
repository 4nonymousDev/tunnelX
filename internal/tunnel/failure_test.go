package tunnel

import (
	"errors"
	"strings"
	"testing"
)

// TestNotConfiguredIsFatal 验证配置缺项被判为不可重试。
// 这类失败靠等待永远不会好——用户不填，重试一万次也是同样的结果。
// 曾经它们走 Classify 的兜底分支被判为可重试，表现为日志里无限刷
// "尚未配置私钥路径；N 秒后重试"，退避时间一路涨到几分钟。
// TestNotConfiguredIsFatal verifies that missing configuration is not retryable.
// Waiting can never fill a missing value; this previously produced endless backoff logs.
func TestNotConfiguredIsFatal(t *testing.T) {
	f := NotConfigured("尚未配置服务器地址")
	if f.Retryable {
		t.Error("配置缺项应判为不可重试")
	}
	if f.Reason != "尚未配置服务器地址" {
		t.Errorf("Reason = %q", f.Reason)
	}
	if f.Err == nil {
		t.Error("Err 不应为 nil——上层可能需要 errors.Is/As")
	}
}

// TestClassifyPlainErrorStillRetryable 验证兜底分支未被改动：
// 未知错误仍默认可重试（误判为致命会让本可自愈的隧道永久停摆）。
// TestClassifyPlainErrorStillRetryable verifies the fallback: unknown errors remain
// retryable because treating them as fatal could permanently stop a recoverable tunnel.
func TestClassifyPlainErrorStillRetryable(t *testing.T) {
	f := Classify(errors.New("某种未知故障"))
	if !f.Retryable {
		t.Error("未知错误应默认可重试")
	}
}

func TestClassifyHostKeyMismatch(t *testing.T) {
	f := Classify(WrapHostKeyMismatch(errors.New("host key changed")))
	if f.Retryable {
		t.Fatal("主机密钥不匹配不应自动重试")
	}
	if !strings.Contains(f.Reason, "主机密钥与记录不符") || !strings.Contains(f.Reason, "known_hosts") {
		t.Fatalf("Reason = %q，应说明真实的 known_hosts 密钥冲突", f.Reason)
	}
}

func TestClassifyHostKeyRejected(t *testing.T) {
	f := Classify(WrapHostKeyRejected(errors.New("user rejected unknown host")))
	if f.Retryable {
		t.Fatal("用户拒绝首次主机密钥后不应自动重试")
	}
	if !strings.Contains(f.Reason, "未信任服务器主机密钥") {
		t.Fatalf("Reason = %q，应准确说明首次主机密钥未获信任", f.Reason)
	}
	if strings.Contains(f.Reason, "known_hosts") || strings.Contains(f.Reason, "中间人攻击") {
		t.Fatalf("Reason = %q，不应把用户拒绝误报为 known_hosts 冲突", f.Reason)
	}
}
