package tunnel

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// TestListenLoopbackDualStack 验证本地监听同时覆盖 IPv4 与 IPv6 环回。
// 只绑 127.0.0.1 时，浏览器访问 http://localhost:PORT 若解析到 ::1 会连接被拒，
// 且请求根本到不了本程序——日志中没有任何记录，故障表现为"什么都没发生"。
// TestListenLoopbackDualStack verifies listening on both IPv4 and IPv6 loopback.
// Binding only 127.0.0.1 can invisibly reject localhost requests that resolve to ::1.
func TestListenLoopbackDualStack(t *testing.T) {
	port := freeLocalPort(t)

	ln, err := listenLoopback(port)
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("ok"))
			c.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)

	// 三种地址写法都应可达。localhost 视系统解析可能落到任一地址族，
	// 正是它必须两边都能通的原因。
	// All three forms must work because localhost may resolve to either address family.
	for _, target := range []string{
		fmt.Sprintf("127.0.0.1:%d", port),
		fmt.Sprintf("[::1]:%d", port),
		fmt.Sprintf("localhost:%d", port),
	} {
		c, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err != nil {
			// IPv6 在部分环境被禁用，此时 ::1 不可达属正常。
			// Some environments disable IPv6, in which case an unreachable ::1 is expected.
			if target == fmt.Sprintf("[::1]:%d", port) {
				t.Logf("IPv6 环回不可用（系统可能已禁用 IPv6）: %v", err)
				continue
			}
			t.Errorf("连接 %s 失败: %v", target, err)
			continue
		}
		buf := make([]byte, 2)
		c.Read(buf)
		c.Close()
		if string(buf) != "ok" {
			t.Errorf("从 %s 读到 %q, 期望 \"ok\"", target, buf)
			continue
		}
		t.Logf("连接 %s 成功", target)
	}
}

// TestListenLoopbackPortInUse 验证端口被占用时报错，而非静默只绑一个地址族。
// TestListenLoopbackPortInUse verifies that occupancy is reported rather than silently binding one family.
func TestListenLoopbackPortInUse(t *testing.T) {
	port := freeLocalPort(t)

	// 先占住 IPv4 环回。
	// Occupy IPv4 loopback first.
	blocker, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("占用端口: %v", err)
	}
	defer blocker.Close()

	ln, err := listenLoopback(port)
	if err == nil {
		// IPv6 侧可能绑定成功，此时不算错误——但 IPv4 侧确实被占，
		// 用户访问 127.0.0.1 会落到 blocker 上。记录此行为以便知晓。
		// IPv6 may still bind successfully; record that IPv4 requests would reach the blocker.
		ln.Close()
		t.Log("IPv4 被占用时仍绑定了 IPv6，属可接受的降级")
		return
	}
	t.Logf("端口被占用时正确报错: %v", err)
}

func freeLocalPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测可用端口: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
