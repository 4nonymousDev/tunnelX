package logbuf

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// 日志文件参数。
// Log-file parameters.
const (
	// MaxFileSize 触发轮转的阈值。
	// MaxFileSize is the rotation threshold.
	MaxFileSize = 5 << 20 // 5MB
	// KeepFiles 保留的历史文件数（tunnelx.log.1 … .N）。
	// KeepFiles is the number of historical files to retain (tunnelx.log.1 … .N).
	KeepFiles = 3
)

// FileSink 把日志写入文件，并按大小轮转。
// 轮转而非清空：现有 PowerShell 脚本用 Clear-Content 直接丢弃历史
// ，排查时往往正需要出事前的那段记录。
// FileSink writes logs to a size-rotated file. Rotation preserves the records
// preceding a failure instead of clearing them like the legacy script.
type FileSink struct {
	mu   sync.Mutex
	path string
	f    *os.File
	w    *bufio.Writer
	size int64
}

// NewFileSink 在指定路径创建日志文件（追加模式）。
// NewFileSink opens or creates the specified log file in append mode.
func NewFileSink(path string) (*FileSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开日志文件 %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("读取日志文件信息: %w", err)
	}

	return &FileSink{
		path: path,
		f:    f,
		w:    bufio.NewWriterSize(f, 16*1024),
		size: info.Size(),
	}, nil
}

// Write 写入一条日志。
// 时间戳精确到毫秒并带日期：排查跨天问题、或比对客户端与服务端日志时都需要。
// Write records one entry with a dated millisecond timestamp for cross-day and client/server correlation.
func (s *FileSink) Write(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.w == nil {
		return
	}

	n, err := fmt.Fprintf(s.w, "%s %-5s [%s] %s\n",
		e.Time.Format("2006-01-02 15:04:05.000"), e.Level, e.Source, e.Msg)
	if err != nil {
		return
	}
	s.size += int64(n)

	// 立即落盘：崩溃或断电时缓冲区内容会丢失，而那恰恰是最需要的部分。
	// 日志量很小（每秒至多数条），刷盘开销可忽略。
	// Flush immediately so a crash or power loss does not discard the most useful records.
	// Log volume is low enough that the cost is negligible.
	s.w.Flush()

	if s.size >= MaxFileSize {
		s.rotate()
	}
}

// rotate 轮转日志文件。调用方须持锁。
// rotate rotates log files; the caller must hold the lock.
func (s *FileSink) rotate() {
	s.w.Flush()
	s.f.Close()

	// tunnelx.log.2 → .3，.1 → .2，依次后移；最旧的被丢弃。
	// Shift .2 to .3 and .1 to .2, discarding the oldest file.
	for i := KeepFiles - 1; i >= 1; i-- {
		older := fmt.Sprintf("%s.%d", s.path, i+1)
		newer := fmt.Sprintf("%s.%d", s.path, i)
		os.Remove(older)
		os.Rename(newer, older)
	}
	os.Rename(s.path, s.path+".1")

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// 轮转失败则停止写入，但不影响程序运行——日志是辅助功能。
		// Stop logging after a rotation failure without stopping the application.
		s.f, s.w = nil, nil
		return
	}
	s.f = f
	s.w = bufio.NewWriterSize(f, 16*1024)
	s.size = 0
}

// Close 刷新缓冲并关闭文件。
// Close flushes the buffer and closes the file.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.w == nil {
		return nil
	}
	s.w.Flush()
	err := s.f.Close()
	s.w, s.f = nil, nil
	return err
}
