package server

import (
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/proto"
)

// The server must route between independently authenticated SSH connections,
// without opening a local TCP port or interpreting an address as authority.
func TestCrossConnectionForward(t *testing.T) {
	addr, keyPath := startServer(t)
	exporter := dialWith(t, addr, keyPath)
	exportPC, control := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer control.Close()
	forwarded := exporter.HandleChannelOpen(proto.ForwardChannelType)
	ack := publishManagementTunnels(t, exportPC, []proto.TunnelSpec{{TunnelID: "http", SrcHost: "127.0.0.1", SrcPort: 8080, Name: "HTTP"}})
	if len(ack.Targets) != 1 {
		t.Fatalf("targets: %+v", ack.Targets)
	}
	target := ack.Targets[0]
	observed := make(chan error, 1)
	go func() {
		select {
		case incoming := <-forwarded:
			got, err := proto.DecodeOpen(incoming.ExtraData())
			if err != nil || got != target {
				_ = incoming.Reject(ssh.Prohibited, "wrong identity")
				observed <- fmt.Errorf("forwarded target %+v: %v", got, err)
				return
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				observed <- err
				return
			}
			defer channel.Close()
			go ssh.DiscardRequests(requests)
			data, err := io.ReadAll(channel)
			if err == nil {
				_, err = channel.Write(append([]byte("OK:"), data...))
			}
			_ = channel.CloseWrite()
			observed <- err
		case <-time.After(3 * time.Second):
			observed <- fmt.Errorf("exporter did not receive forwarding channel")
		}
	}()
	importer := dialWith(t, addr, keyPath)
	_, importControl := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer importControl.Close()
	payload, _ := proto.EncodeOpen(target)
	channel, requests, err := importer.OpenChannel(proto.OpenChannelType, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	timer := time.AfterFunc(3*time.Second, func() { _ = importer.Close() })
	defer timer.Stop()
	if _, err = channel.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	_ = channel.CloseWrite()
	data, err := io.ReadAll(channel)
	if err != nil || string(data) != "OK:request" {
		t.Fatalf("cross connection response %q: %v", data, err)
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
}

func TestUnknownOrForgedTargetRejected(t *testing.T) {
	addr, keyPath := startServer(t)
	exporter := dialWith(t, addr, keyPath)
	pc, ctrl := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer ctrl.Close()
	incoming := exporter.HandleChannelOpen(proto.ForwardChannelType)
	ack := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 80}})
	target := ack.Targets[0]
	importer := dialWith(t, addr, keyPath)
	_, ctrl2 := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer ctrl2.Close()
	cases := map[string]func(*proto.Target){
		"unknown client":    func(x *proto.Target) { x.ClientID = "unknown" },
		"wrong fingerprint": func(x *proto.Target) { x.Fingerprint = "SHA256:other" },
		"wrong tunnel":      func(x *proto.Target) { x.TunnelID = "other" },
		"wrong session":     func(x *proto.Target) { x.SessionID = "expired" },
		"wrong generation":  func(x *proto.Target) { x.Generation++ },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			bad := target
			change(&bad)
			raw, _ := proto.EncodeOpen(bad)
			if ch, _, err := importer.OpenChannel(proto.OpenChannelType, raw); err == nil {
				ch.Close()
				t.Fatal("forged target accepted")
			}
		})
	}
	for _, raw := range [][]byte{[]byte(`{"v":1,"target":{}}`), []byte(`{"v":2,"target":{}}`), []byte("malformed")} {
		if ch, _, err := importer.OpenChannel(proto.OpenChannelType, raw); err == nil {
			ch.Close()
			t.Fatal("invalid payload accepted")
		}
	}
	select {
	case received := <-incoming:
		_ = received.Reject(ssh.Prohibited, "unexpected")
		t.Fatal("invalid request reached exporter")
	default:
	}
}

func TestOwnerRejectionReleasesOpenBudget(t *testing.T) {
	addr, keyPath := startServer(t, func(c *Config) { c.Limits.ChannelsPerSession = 2; c.Limits.ChannelsTotal = 2 })
	exporter := dialWith(t, addr, keyPath)
	pc, ctrl := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer ctrl.Close()
	incoming := exporter.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 80}}).Targets[0]
	var accepts atomic.Int32
	go func() {
		for n := range incoming {
			if accepts.Add(1) == 1 {
				_ = n.Reject(ssh.ConnectionFailed, "service offline")
				continue
			}
			ch, requests, err := n.Accept()
			if err != nil {
				continue
			}
			go ssh.DiscardRequests(requests)
			_, _ = ch.Write([]byte("ready"))
			_ = ch.CloseWrite()
			_ = ch.Close()
		}
	}()
	importer := dialWith(t, addr, keyPath)
	_, ctrl2 := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer ctrl2.Close()
	raw, _ := proto.EncodeOpen(target)
	if ch, _, err := importer.OpenChannel(proto.OpenChannelType, raw); err == nil {
		ch.Close()
		t.Fatal("owner rejection ignored")
	}
	var connected ssh.Channel
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ch, requests, err := importer.OpenChannel(proto.OpenChannelType, raw)
		if err == nil {
			go ssh.DiscardRequests(requests)
			connected = ch
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if connected == nil {
		t.Fatal("rejected open leaked the only channel budget")
	}
	defer connected.Close()
	_ = connected.CloseWrite()
	got, err := io.ReadAll(connected)
	if err != nil || string(got) != "ready" {
		t.Fatalf("recovered stream %q %v", got, err)
	}
}

func TestOwnerOpenTimeoutClosesUnresponsiveConnection(t *testing.T) {
	addr, keyPath := startServer(t, func(c *Config) { c.OpenTimeout = 100 * time.Millisecond; c.WriteTimeout = 500 * time.Millisecond })
	exporter := dialWith(t, addr, keyPath)
	pc, ctrl := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer ctrl.Close()
	incoming := exporter.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "slow", SrcPort: 80}}).Targets[0]
	seen := make(chan struct{})
	go func() {
		if _, ok := <-incoming; ok {
			close(seen)
		}
	}() // Deliberately never Accept/Reject.
	importer := dialWith(t, addr, keyPath)
	_, ctrl2 := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer ctrl2.Close()
	raw, _ := proto.EncodeOpen(target)
	result := make(chan error, 1)
	go func() {
		ch, _, err := importer.OpenChannel(proto.OpenChannelType, raw)
		if ch != nil {
			_ = ch.Close()
		}
		result <- err
	}()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach exporter")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("unanswered open accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open was not bounded")
	}
	ownerClosed := make(chan error, 1)
	go func() { ownerClosed <- exporter.Wait() }()
	select {
	case <-ownerClosed:
	case <-time.After(time.Second):
		t.Fatal("unresponsive owner transport remained open")
	}
	// Only the non-responsive owner is retired; the importer remains usable.
	if _, _, err := importer.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("importer was unnecessarily disconnected: %v", err)
	}
}

// An importer can disappear while a healthy exporter is still preparing its
// local connection. Cancellation must retire only this pending stream.
func TestVisitorCancellationPreservesResponsiveOwner(t *testing.T) {
	addr, keyPath := startServer(t, func(c *Config) { c.OpenTimeout = 2 * time.Second })
	exporter := dialWith(t, addr, keyPath)
	pc, ctrl := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer ctrl.Close()
	incoming := exporter.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 80}}).Targets[0]
	importer := dialWith(t, addr, keyPath)
	_, ctrl2 := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer ctrl2.Close()
	raw, _ := proto.EncodeOpen(target)
	openResult := make(chan error, 1)
	go func() {
		ch, _, err := importer.OpenChannel(proto.OpenChannelType, raw)
		if ch != nil {
			_ = ch.Close()
		}
		openResult <- err
	}()
	var pending ssh.NewChannel
	select {
	case pending = <-incoming:
	case <-time.After(time.Second):
		t.Fatal("request did not reach exporter")
	}
	ownerClosed := make(chan error, 1)
	go func() { ownerClosed <- exporter.Wait() }()
	_ = importer.Close()
	select {
	case <-openResult:
	case <-time.After(time.Second):
		t.Fatal("disconnected importer open did not finish")
	}
	select {
	case err := <-ownerClosed:
		t.Fatalf("importer cancellation retired a healthy exporter: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	ch, requests, err := pending.Accept()
	if err != nil {
		t.Fatalf("healthy exporter could not complete its pending reply: %v", err)
	}
	go ssh.DiscardRequests(requests)
	defer ch.Close()
	if _, _, err := exporter.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("exporter did not survive cancellation: %v", err)
	}
}

func TestCancelledPendingOpenRetainsItsBudgetUntilOwnerReplies(t *testing.T) {
	addr, keyPath := startServer(t, func(c *Config) {
		c.OpenTimeout = 2 * time.Second
		c.Limits.ChannelsPerSession = 2
		c.Limits.ChannelsTotal = 2
	})
	exporter := dialWith(t, addr, keyPath)
	pc, ctrl := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer ctrl.Close()
	incoming := exporter.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 80}}).Targets[0]
	importer := dialWith(t, addr, keyPath)
	_, ctrl2 := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer ctrl2.Close()
	raw, _ := proto.EncodeOpen(target)
	firstDone := make(chan struct{})
	go func() {
		ch, _, _ := importer.OpenChannel(proto.OpenChannelType, raw)
		if ch != nil {
			_ = ch.Close()
		}
		close(firstDone)
	}()
	var pending ssh.NewChannel
	select {
	case pending = <-incoming:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach exporter")
	}
	_ = importer.Close()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first importer open did not stop")
	}
	other := dialWith(t, addr, keyPath)
	_, ctrl3 := openTestControl(t, other, proto.RoleImporter, "same-conn-test")
	defer ctrl3.Close()
	second := make(chan error, 1)
	go func() {
		ch, _, err := other.OpenChannel(proto.OpenChannelType, raw)
		if ch != nil {
			_ = ch.Close()
		}
		second <- err
	}()
	select {
	case unexpected := <-incoming:
		if unexpected != nil {
			_ = unexpected.Reject(ssh.Prohibited, "budget must be retained")
		}
		t.Fatal("cancelled pending open prematurely released its budget or owner connection")
	case err := <-second:
		if err == nil {
			t.Fatal("second open exceeded the sole active flow budget")
		}
	case <-time.After(time.Second):
		t.Fatal("excess open was not rejected promptly")
	}
	if err := pending.Reject(ssh.ConnectionFailed, "local service unavailable"); err != nil {
		t.Fatalf("owner connection did not survive visitor cancellation: %v", err)
	}
	go func() {
		for next := range incoming {
			ch, requests, err := next.Accept()
			if err != nil {
				continue
			}
			go ssh.DiscardRequests(requests)
			_, _ = ch.Write([]byte("ready"))
			_ = ch.CloseWrite()
			_ = ch.Close()
		}
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ch, requests, err := other.OpenChannel(proto.OpenChannelType, raw)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		go ssh.DiscardRequests(requests)
		_ = ch.CloseWrite()
		data, err := io.ReadAll(ch)
		_ = ch.Close()
		if err != nil || string(data) != "ready" {
			t.Fatalf("recovered flow %q: %v", data, err)
		}
		return
	}
	t.Fatal("completed cancellation did not release the flow budget")
}

func TestOwnerRequestsCannotExhaustVisitorsRateBudget(t *testing.T) {
	addr, keyPath := startServer(t)
	exporter := dialWith(t, addr, keyPath)
	pc, ctrl := openTestControl(t, exporter, proto.RoleExporter, "cross-exporter")
	defer ctrl.Close()
	incoming := exporter.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 80}}).Targets[0]
	ownerChannel := make(chan ssh.Channel, 1)
	go func() {
		n := <-incoming
		if n == nil {
			ownerChannel <- nil
			return
		}
		ch, requests, err := n.Accept()
		if err != nil {
			ownerChannel <- nil
			return
		}
		go ssh.DiscardRequests(requests)
		ownerChannel <- ch
	}()
	importer := dialWith(t, addr, keyPath)
	_, ctrl2 := openTestControl(t, importer, proto.RoleImporter, "cross-importer")
	defer ctrl2.Close()
	raw, _ := proto.EncodeOpen(target)
	ch, requests, err := importer.OpenChannel(proto.OpenChannelType, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go ssh.DiscardRequests(requests)
	out := <-ownerChannel
	if out == nil {
		t.Fatal("owner channel failed")
	}
	defer out.Close()
	timer := time.AfterFunc(2*time.Second, func() { _ = exporter.Close() })
	defer timer.Stop()
	// The target is malicious. Its request flood must be charged only to its
	// own connection, leaving the visitor's other tunnels and control usable.
	for i := 0; i < 100; i++ {
		if _, err = out.SendRequest("unsupported-owner-request", true, nil); err != nil {
			break
		}
	}
	if _, _, err := importer.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("owner exhausted another client's request budget: %v", err)
	}
}

func openTestControl(t *testing.T, client *ssh.Client, role, id string) (*proto.Conn, ssh.Channel) {
	t.Helper()
	ch, requests, err := client.OpenChannel(proto.ChannelType, nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(requests)
	pc := proto.NewConn(ch)
	if err = pc.Send(proto.Hello{V: proto.Version, Type: proto.TypeHello, Role: role, ID: id, Name: id, ClientVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(3*time.Second, func() { _ = client.Close() })
	defer timer.Stop()
	for {
		env, raw, err := pc.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if env.Type == proto.TypeHelloOK {
			return pc, ch
		}
		if env.Type == proto.TypeError {
			t.Fatalf("hello rejected: %s", raw)
		}
	}
}

func dialWith(t *testing.T, addr, keyPath string) *ssh.Client {
	t.Helper()
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
