package proto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// maxLineBytes 限制单条消息的大小，防止畸形输入耗尽内存。
// 注册表即使数百条也远小于此。
// maxLineBytes limits individual messages so malformed input cannot exhaust memory.
// Even a registry containing hundreds of entries is far smaller than this.
const maxLineBytes = 4 << 20 // 4MB

// Conn 在一条 SSH channel 上收发 JSON Lines 消息。
// 写入加锁：多个 goroutine 可能同时上报（如隧道状态变化与心跳），而 json.Encoder
// 的写入不是原子的，并发写会导致两条消息的字节交错，产生无法解析的行。
// Conn exchanges JSON Lines messages over a single SSH channel.
// Writes are locked because multiple goroutines may report concurrently (for example,
// tunnel state changes and heartbeats), while json.Encoder writes are not atomic;
// concurrent writes could interleave two messages and produce an invalid line.
type Conn struct {
	rw  io.ReadWriter
	enc *json.Encoder
	sc  *bufio.Scanner
	mu  sync.Mutex
}

// NewConn 包装一条已建立的 SSH channel。
// NewConn wraps an established SSH channel.
func NewConn(rw io.ReadWriter) *Conn {
	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	return &Conn{
		rw:  rw,
		enc: json.NewEncoder(rw),
		sc:  sc,
	}
}

// Send 写出一条消息。encoding/json 会自动追加 \n 并转义消息体内的换行，
// 因此不会破坏 JSON Lines 的分帧。
// Send writes one message. encoding/json appends \n and escapes newlines within the
// message body, so JSON Lines framing remains intact.
func (c *Conn) Send(msg any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enc.Encode(msg); err != nil {
		return fmt.Errorf("发送控制消息: %w", err)
	}
	return nil
}

// Recv 读取下一条消息的原始字节与其类型信封。
// 返回的 []byte 仅在下次调用 Recv 前有效——bufio.Scanner 会复用底层缓冲区。
// 调用方若需保留，应在解码后立即使用，或自行复制。
// Recv reads the raw bytes of the next message and its type envelope.
// The returned []byte remains valid only until the next Recv call because
// bufio.Scanner reuses its backing buffer. Callers that need to retain it must use it
// immediately after decoding or make their own copy.
func (c *Conn) Recv() (Envelope, []byte, error) {
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return Envelope{}, nil, fmt.Errorf("读取控制消息: %w", err)
		}
		return Envelope{}, nil, io.EOF
	}
	line := c.sc.Bytes()
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Envelope{}, nil, fmt.Errorf("解析控制消息: %w", err)
	}
	return env, line, nil
}

// Decode 将 Recv 返回的原始字节解码为具体消息结构。
// Decode unmarshals the raw bytes returned by Recv into a concrete message type.
func Decode(raw []byte, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("解码控制消息: %w", err)
	}
	return nil
}
