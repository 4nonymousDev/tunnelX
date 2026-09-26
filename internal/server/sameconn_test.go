package server

import (
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"testing"
	"time"
	"tunnelx/internal/proto"
)

// A mixed Import/Export client shares one SSH transport. Reading the control
// channel synchronously in the transport dispatcher would deadlock this test.
func TestControlChannelDoesNotBlockForwarding(t *testing.T) {
	addr, keyPath := startServer(t)
	client := dialWith(t, addr, keyPath)
	pc, control := openTestControl(t, client, proto.RoleImporter, "same-conn-test")
	defer control.Close()
	forwarded := client.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 80}}).Targets[0]
	observed := make(chan error, 1)
	go func() {
		select {
		case n := <-forwarded:
			got, err := proto.DecodeOpen(n.ExtraData())
			if err != nil || got != target {
				_ = n.Reject(ssh.Prohibited, "wrong target")
				observed <- fmt.Errorf("wrong target %+v %v", got, err)
				return
			}
			ch, requests, err := n.Accept()
			if err != nil {
				observed <- err
				return
			}
			defer ch.Close()
			go ssh.DiscardRequests(requests)
			_, err = ch.Write([]byte("PONG"))
			_ = ch.CloseWrite()
			observed <- err
		case <-time.After(3 * time.Second):
			observed <- fmt.Errorf("same-connection forwarding blocked")
		}
	}()
	payload, _ := proto.EncodeOpen(target)
	done := make(chan error, 1)
	go func() {
		ch, requests, err := client.OpenChannel(proto.OpenChannelType, payload)
		if err != nil {
			done <- err
			return
		}
		defer ch.Close()
		go ssh.DiscardRequests(requests)
		_ = ch.CloseWrite()
		buf, err := io.ReadAll(ch)
		if err == nil && string(buf) != "PONG" {
			err = fmt.Errorf("wrong reply %q", buf)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("same transport deadlocked")
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	// The same control stream must still commit a publication after data transfer.
	updated := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 80, Name: "updated"}})
	if len(updated.Targets) != 1 || updated.Targets[0].Generation <= target.Generation {
		t.Fatalf("control did not update generation: %+v", updated)
	}
}
