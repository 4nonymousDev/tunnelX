package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"tunnelx/internal/proto"
)

func TestSlowRegistryReaderIsDisconnectedWithoutBlockingOtherClients(t *testing.T) {
	addr, keyPath := startServer(t, func(c *Config) {
		c.WriteTimeout = 500 * time.Millisecond
		c.Limits.ExportsPerSession = 1024
		c.Limits.ExportsTotal = 1024
		c.Limits.PublishBytes = 3 << 20
		c.Limits.RegistryBytes = 3 << 20
	})
	owner := dialWith(t, addr, keyPath)
	pc, ownerControl := openTestControl(t, owner, proto.RoleExporter, "cross-exporter")
	defer ownerControl.Close()
	slow := dialWith(t, addr, keyPath)
	_, slowControl := openTestControl(t, slow, proto.RoleImporter, "cross-importer")
	defer slowControl.Close()
	// Stop consuming the control stream. A full registry exceeds SSH's initial
	// channel window, forcing a real blocked Send rather than only socket buffering.
	tunnels := make([]proto.TunnelSpec, 1024)
	for i := range tunnels {
		tunnels[i] = proto.TunnelSpec{
			TunnelID: strings.Repeat("界", 124) + fmt.Sprintf("%04d", i),
			Name:     strings.Repeat("界", 200), SrcHost: strings.Repeat("界", 255), SrcPort: 80,
		}
	}
	ack := publishManagementTunnels(t, pc, tunnels)
	if len(ack.Targets) != len(tunnels) {
		t.Fatalf("published %d targets", len(ack.Targets))
	}
	// Keep the healthy reader consuming registry updates after its publication.
	go func() {
		for {
			if _, _, err := pc.Recv(); err != nil {
				return
			}
		}
	}()
	closed := make(chan error, 1)
	go func() { closed <- slow.Wait() }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("slow registry consumer outlived the write deadline")
	}
	if _, _, err := owner.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("slow consumer blocked or disconnected the healthy publisher: %v", err)
	}
}
