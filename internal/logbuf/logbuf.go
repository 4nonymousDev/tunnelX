// Package logbuf 提供带等级的环形日志缓冲。
// 用环形缓冲而非无限追加，顺带解决了内存无限增长的问题——这正是现有 PowerShell
// 脚本用 Clear-Content 粗暴清空所要应付的问题。
// Package logbuf provides a leveled ring-buffer logger.
// A ring buffer avoids unbounded memory growth without the legacy PowerShell script's destructive clearing.
package logbuf

import (
	"sync"
	"time"
)

// Capacity 是保留的最大行数。
// Capacity is the maximum number of retained lines.
const Capacity = 2000

// Level 是日志等级，决定 UI 中的着色。
// Level is the log severity and controls UI coloring.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	default:
		return "?"
	}
}

// Entry 是一条日志。
// Entry is one log record.
type Entry struct {
	Time   time.Time
	Level  Level
	Source string // 隧道名或子系统名，如 "NAS"、"conn"；UI 显示为 [来源] / Tunnel or subsystem name, displayed as [source].
	// Source is a tunnel or subsystem name such as "NAS" or "conn"; the UI displays it as [source].
	Msg string
}

// Buffer 是并发安全的环形日志缓冲。
// 隧道 goroutine 从任意线程写入，界面线程读取——后台 goroutine 不能直接改
// 控件，须经此缓冲 + 通知回调回到界面线程。
// Buffer is a concurrency-safe ring buffer. Tunnel goroutines write from arbitrary
// threads, while the UI reads via this buffer and marshals notifications to its thread.
type Buffer struct {
	mu      sync.RWMutex
	entries []Entry
	start   int // 环形起点 / Ring-buffer start.
	// start is the beginning of the ring.
	size int
	seq  uint64 // 单调递增，UI 据此判断是否有新内容 / Monotonic sequence used by the UI to detect new content.
	// seq increases monotonically so the UI can detect new content.

	onAppend    func()
	subscribers map[uint64]func(Entry)
	nextSubID   uint64
	sink        Sink
}

// Sink 接收每一条日志，用于落盘等旁路输出。
// 环形缓冲只保留最近 Capacity 行且随进程退出而消失，排查线上问题时不够用——
// 需要完整历史、精确时间戳，且能直接把文件发给他人分析。
// Sink receives every entry for side-channel output such as a file. Unlike the
// in-memory ring, it preserves complete timestamped history for offline analysis.
type Sink interface {
	Write(Entry)
	Close() error
}

// New 创建缓冲。onAppend 在每次写入后调用（持锁外），供 UI 触发刷新；可为 nil。
// 注意：onAppend 会在写入方的 goroutine 上被调用，订阅方须自行转交到界面线程。
// New creates a buffer. onAppend runs outside the lock on the writer's goroutine;
// UI subscribers must marshal it to the UI thread. It may be nil.
func New(onAppend func()) *Buffer {
	return &Buffer{
		entries:     make([]Entry, Capacity),
		onAppend:    onAppend,
		subscribers: make(map[uint64]func(Entry)),
	}
}

// Subscribe 订阅新增日志。多个 CLI/UI 可以同时订阅；返回函数用于解除订阅。
// 回调在写日志的 goroutine 上执行，订阅者不得阻塞。
// Subscribe registers a nonblocking callback for new entries and returns an unsubscribe function.
func (b *Buffer) Subscribe(fn func(Entry)) func() {
	if fn == nil {
		return func() {}
	}
	b.mu.Lock()
	b.nextSubID++
	id := b.nextSubID
	b.subscribers[id] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}
}

// SetOnAppend 替换写入回调。
// 缓冲区先于 UI 创建（main 需在 UI 构建前就能记录启动日志），故回调只能事后接管。
// SetOnAppend replaces the append callback after the UI takes over the earlier-created buffer.
func (b *Buffer) SetOnAppend(fn func()) {
	b.mu.Lock()
	b.onAppend = fn
	b.mu.Unlock()
}

// SetSink 设置旁路输出。传 nil 可停用。
// SetSink sets optional side-channel output; nil disables it.
func (b *Buffer) SetSink(s Sink) {
	b.mu.Lock()
	b.sink = s
	b.mu.Unlock()
}

// CloseSink 关闭旁路输出，确保缓冲内容落盘。退出前应调用。
// CloseSink closes side-channel output and flushes it before exit.
func (b *Buffer) CloseSink() error {
	b.mu.Lock()
	s := b.sink
	b.sink = nil
	b.mu.Unlock()

	if s != nil {
		return s.Close()
	}
	return nil
}

// notify 在锁外读取回调并调用，避免回调中再次取锁导致死锁。
// notify snapshots callbacks and invokes them outside the lock to avoid reentrant deadlocks.
func (b *Buffer) notify(e Entry) {
	b.mu.RLock()
	fn := b.onAppend
	subs := make([]func(Entry), 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()
	if fn != nil {
		fn()
	}
	for _, sub := range subs {
		sub(e)
	}
}

// writeSink 在锁外写出，避免磁盘 IO 阻塞其他写入者。
// writeSink performs disk I/O outside the lock so it does not block other writers.
func (b *Buffer) writeSink(e Entry) {
	b.mu.RLock()
	s := b.sink
	b.mu.RUnlock()
	if s != nil {
		s.Write(e)
	}
}

// Append 追加一条日志，超出容量时覆盖最旧的一条。
// Append adds an entry and overwrites the oldest one at capacity.
func (b *Buffer) Append(level Level, source, msg string) {
	b.mu.Lock()
	idx := (b.start + b.size) % Capacity
	if b.size == Capacity {
		b.start = (b.start + 1) % Capacity // 覆盖最旧 / Overwrite the oldest entry.
		// Overwrite the oldest entry.
	} else {
		b.size++
	}
	e := Entry{
		Time:   time.Now(),
		Level:  level,
		Source: source,
		Msg:    msg,
	}
	b.entries[idx] = e
	b.seq++
	b.mu.Unlock()

	b.writeSink(e)
	b.notify(e)
}

func (b *Buffer) Debugf(source, format string, args ...any) {
	b.Append(Debug, source, sprintf(format, args...))
}

func (b *Buffer) Infof(source, format string, args ...any) {
	b.Append(Info, source, sprintf(format, args...))
}

func (b *Buffer) Warnf(source, format string, args ...any) {
	b.Append(Warn, source, sprintf(format, args...))
}

func (b *Buffer) Errorf(source, format string, args ...any) {
	b.Append(Error, source, sprintf(format, args...))
}

// Snapshot 返回当前全部日志的副本，按时间正序。
// 可选按最低等级过滤，供 UI 的等级下拉框使用。
// Snapshot returns entries in chronological order, optionally filtered by minimum severity.
func (b *Buffer) Snapshot(min Level) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]Entry, 0, b.size)
	for i := 0; i < b.size; i++ {
		e := b.entries[(b.start+i)%Capacity]
		if e.Level >= min {
			out = append(out, e)
		}
	}
	return out
}

// Seq 返回写入序号，UI 可据此跳过无变化的重绘。
// Seq returns the write sequence so the UI can skip unchanged redraws.
func (b *Buffer) Seq() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.seq
}

// Clear 清空缓冲，对应 UI 的"清空"按钮。
// Clear empties the buffer for the UI's Clear action.
func (b *Buffer) Clear() {
	b.mu.Lock()
	b.start = 0
	b.size = 0
	b.seq++
	b.mu.Unlock()

	b.mu.RLock()
	fn := b.onAppend
	b.mu.RUnlock()
	if fn != nil {
		fn()
	}
}
