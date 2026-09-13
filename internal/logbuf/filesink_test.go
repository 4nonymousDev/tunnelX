package logbuf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileSinkWrites 验证日志确实落盘，且格式包含排查所需的全部字段。
// TestFileSinkWrites verifies persistence and all troubleshooting fields.
func TestFileSinkWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnelx.log")

	sink, err := NewFileSink(path)
	if err != nil {
		t.Fatalf("创建 sink: %v", err)
	}

	buf := New(nil)
	buf.SetSink(sink)

	buf.Infof("conn", "正在连接 %s", "1.2.3.4:2222")
	buf.Warnf("nginx", "转发失败: %v", "连接被拒")
	buf.Errorf("conn", "认证被拒绝")

	if err := buf.CloseSink(); err != nil {
		t.Fatalf("关闭 sink: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志: %v", err)
	}
	text := string(data)
	t.Logf("日志内容:\n%s", text)

	for _, want := range []string{
		"INFO", "WARN", "ERROR", // 等级 / Levels.
		// Severity levels.
		"[conn]", "[nginx]", // 来源 / Sources.
		// Sources.
		"正在连接 1.2.3.4:2222", // 消息与格式化参数 / Message and formatting arguments.
		// Message and formatting arguments.
		"转发失败: 连接被拒",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("日志中缺少 %q", want)
		}
	}

	// 时间戳须带日期与毫秒：跨天排查、与服务端日志比对时都需要。
	// Timestamps need dates and milliseconds for cross-day and server-log correlation.
	if !strings.Contains(text, "-") || !strings.Contains(text, ".") {
		t.Error("时间戳格式不含日期或毫秒")
	}
}

// TestFileSinkAppends 验证重启后追加而非覆盖——上次运行的记录不应丢失。
// TestFileSinkAppends verifies that restart appends instead of overwriting prior records.
func TestFileSinkAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnelx.log")

	s1, err := NewFileSink(path)
	if err != nil {
		t.Fatalf("首次创建: %v", err)
	}
	s1.Write(Entry{Level: Info, Source: "app", Msg: "第一次运行"})
	s1.Close()

	s2, err := NewFileSink(path)
	if err != nil {
		t.Fatalf("二次创建: %v", err)
	}
	s2.Write(Entry{Level: Info, Source: "app", Msg: "第二次运行"})
	s2.Close()

	data, _ := os.ReadFile(path)
	text := string(data)

	if !strings.Contains(text, "第一次运行") {
		t.Error("重启后丢失了上次运行的日志")
	}
	if !strings.Contains(text, "第二次运行") {
		t.Error("缺少本次运行的日志")
	}
}

// TestFileSinkRotates 验证超过阈值时轮转，且历史被保留而非清空。
// 现有 PowerShell 脚本用 Clear-Content 直接丢弃历史，
// 而排查时往往正需要出事前的那段记录。
// TestFileSinkRotates verifies retention after the size threshold instead of destructive clearing.
func TestFileSinkRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnelx.log")

	sink, err := NewFileSink(path)
	if err != nil {
		t.Fatalf("创建 sink: %v", err)
	}

	// 写满超过 MaxFileSize，触发至少一次轮转。
	// Exceed MaxFileSize to trigger at least one rotation.
	long := strings.Repeat("x", 1024)
	for i := 0; i < MaxFileSize/1024+10; i++ {
		sink.Write(Entry{Level: Info, Source: "test", Msg: long})
	}
	sink.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("轮转后主日志文件不存在: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("轮转后未生成 .1 历史文件: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Logf("轮转后的文件: %v", names)

	// 保留数不应无限增长。
	// Retention must remain bounded.
	if len(names) > KeepFiles+1 {
		t.Errorf("文件数 %d 超过上限 %d", len(names), KeepFiles+1)
	}
}

// TestBufferWithoutSink 验证未设置 sink 时写入不会 panic。
// TestBufferWithoutSink verifies that writing without a sink does not panic.
func TestBufferWithoutSink(t *testing.T) {
	buf := New(nil)
	buf.Infof("app", "无 sink 时应正常工作")

	if got := len(buf.Snapshot(Debug)); got != 1 {
		t.Errorf("缓冲条目数 = %d, 期望 1", got)
	}
	if err := buf.CloseSink(); err != nil {
		t.Errorf("无 sink 时 CloseSink 应返回 nil, 得到 %v", err)
	}
}
