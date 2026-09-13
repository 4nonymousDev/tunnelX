package server

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/proto"
)

// TestControlChannelDoesNotBlockForwarding 验证控制通道不会阻塞同一连接上的
// 转发通道。
// 这是单连接多路复用的核心要求：Importer 在**同一条**
// SSH 连接上既开控制通道、又开 direct-tcpip 转发通道，两者必须并行。
// 曾出现的缺陷：服务端在 `for newCh := range chans` 循环里同步调用 serveControl，
// 而后者会阻塞至控制通道关闭。于是控制通道一旦建立，该循环就再也读不到后续的
// channel 请求——Importer 的 direct-tcpip 永远排队，症状是"连接建立后挂起、
// 服务端毫无日志"，极难定位。
// 既有测试未能发现此问题，因为它们把控制通道与转发通道放在了不同的 SSH 连接上。
// TestControlChannelDoesNotBlockForwarding verifies concurrent control and
// direct-tcpip channels on one multiplexed SSH connection and guards against a
// synchronous control handler starving later channel requests.
func TestControlChannelDoesNotBlockForwarding(t *testing.T) {
	addr, keyPath := startServer(t)

	client := dialWith(t, addr, keyPath)

	// ① 先在这条连接上开控制通道并完成握手，使服务端进入 serveControl。
	// 1. Open and handshake the control channel on this connection first.
	ctrlCh, ctrlReqs, err := client.OpenChannel(proto.ChannelType, nil)
	if err != nil {
		t.Fatalf("打开控制通道: %v", err)
	}
	defer ctrlCh.Close()
	go ssh.DiscardRequests(ctrlReqs)

	pc := proto.NewConn(ctrlCh)
	if err := pc.Send(proto.Hello{
		V: proto.Version, Type: proto.TypeHello,
		Role: proto.RoleExporter, ID: "same-conn-test",
		Name: "笔记本", ClientVersion: "test",
	}); err != nil {
		t.Fatalf("发送 hello: %v", err)
	}
	env, _, err := pc.Recv()
	if err != nil {
		t.Fatalf("接收 hello_ok: %v", err)
	}
	if env.Type != proto.TypeHelloOK {
		t.Fatalf("握手响应类型 = %s, 期望 %s", env.Type, proto.TypeHelloOK)
	}
	t.Log("控制通道已建立，服务端此刻正阻塞于 serveControl")
	remoteLn, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配远程端口: %v", err)
	}
	defer remoteLn.Close()
	targetPort := remoteLn.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, e := remoteLn.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = c.Write([]byte("PONG")) }()
		}
	}()
	publishTestPort(t, pc, targetPort)

	// ② 在**同一条连接**上开转发通道。若服务端的 channel 循环被控制通道占住，
	//    此调用将永远挂起。
	// 2. Open forwarding on the same connection; a blocked channel loop would hang here.
	done := make(chan error, 1)
	go func() {
		conn, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort))
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		conn.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			done <- fmt.Errorf("读取: %w", err)
			return
		}
		if string(buf) != "PONG" {
			done <- fmt.Errorf("收到 %q, 期望 PONG", buf)
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("同连接转发失败: %v", err)
		}
		t.Log("控制通道与转发通道在同一连接上并行工作正常")
	case <-time.After(10 * time.Second):
		t.Fatal("同连接上的转发通道超时挂起——" +
			"服务端的 channel 循环很可能被控制通道阻塞了")
	}

	// ③ 控制通道仍应可用：并行不是以牺牲控制通道为代价。
	// 3. The control channel must remain usable after concurrent forwarding.
	if err := pc.Send(proto.Publish{
		V: proto.Version, Type: proto.TypePublish,
		Tunnels: []proto.TunnelSpec{{SrcPort: 80, RemotePort: targetPort, Name: "web"}},
	}); err != nil {
		t.Fatalf("转发后发送 publish: %v", err)
	}
	// 服务端会在注册表变化时主动推送 registry，它可能先于 publish_ok 抵达
	// （二者分属推送与应答两条逻辑）。故需跳过推送消息再取应答。
	// A registry push may precede publish_ok, so skip pushes while waiting for the reply.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		env, _, err = pc.Recv()
		if err != nil {
			t.Fatalf("转发后接收应答: %v", err)
		}
		if env.Type == proto.TypeRegistry {
			// 主动推送，跳过。
			// Skip a server push.
			continue
		}
		break
	}
	if env.Type != proto.TypePublishOK {
		t.Errorf("publish 响应类型 = %s, 期望 %s", env.Type, proto.TypePublishOK)
	}
	t.Log("转发之后控制通道依然可用")
}
