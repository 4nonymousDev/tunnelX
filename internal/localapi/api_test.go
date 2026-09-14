package localapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/core"
	"tunnelx/internal/tunnel"
)

func TestServerAuthenticatesAndPreventsDuplicateOwner(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{ID: "api-test", Name: "api-test", Tunnels: []config.Tunnel{}}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	service, err := core.New(cfg, core.Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	endpointPath := filepath.Join(dir, EndpointFileName)
	server, err := Start(service, endpointPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	client, err := NewClient(endpointPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != APIVersion || snapshot.ClientVersion != "test" || snapshot.ID != "api-test" || snapshot.Connection.State != "idle" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	respCreate, err := client.request(context.Background(), http.MethodPost, "/v1/tunnels", TunnelConfigDTO{
		Kind: "export", Name: "web", LocalHost: "127.0.0.1", LocalPort: 8080,
	})
	if err != nil {
		t.Fatal(err)
	}
	respCreate.Body.Close()
	snapshot, err = client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tunnels) != 1 || snapshot.Tunnels[0].Config.ID == "" {
		t.Fatalf("created tunnel has no stable id: %#v", snapshot.Tunnels)
	}
	tunnelID := snapshot.Tunnels[0].Config.ID
	updated := snapshot.Tunnels[0].Config
	updated.Name = "renamed"
	respUpdate, err := client.request(context.Background(), http.MethodPut, "/v1/tunnels/"+tunnelID, updated)
	if err != nil {
		t.Fatal(err)
	}
	respUpdate.Body.Close()
	respBatch, err := client.request(context.Background(), http.MethodPost, "/v1/tunnels/batch", []TunnelConfigDTO{
		{Kind: "export", Name: "batch-one", LocalHost: "127.0.0.1", LocalPort: 8081},
		{Kind: "export", Name: "batch-two", LocalHost: "127.0.0.1", LocalPort: 8082},
	})
	if err != nil {
		t.Fatal(err)
	}
	respBatch.Body.Close()
	snapshot, err = client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tunnels) != 3 {
		t.Fatalf("batch create produced %d tunnels, want 3 total", len(snapshot.Tunnels))
	}

	resp, err := http.Get(server.Endpoint().Address + "/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", resp.StatusCode)
	}
	if _, err := Start(service, endpointPath); err == nil {
		t.Fatal("second server unexpectedly acquired the same endpoint")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(endpointPath)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("endpoint permissions = %o, want 600", perm)
		}
	}
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	default:
		t.Fatal("shutdown request did not notify the executable host")
	}
}

func TestTunnelConfigDTOPreservesPeerTunnelID(t *testing.T) {
	original := config.Tunnel{
		ID: "import-rule", Kind: config.KindImport, PeerID: "peer",
		PeerTunnelID: "remote-rule", PeerSrcPort: 80, ListenPort: 8080,
	}
	dto := tunnelConfigDTO(original)
	if dto.PeerTunnelID != "remote-rule" {
		t.Fatalf("DTO 丢失 peer_tunnel_id: %+v", dto)
	}
	if roundTrip := dto.config(); roundTrip.PeerTunnelID != original.PeerTunnelID {
		t.Fatalf("DTO 往返丢失 peer_tunnel_id: %+v", roundTrip)
	}
}

func TestSnapshotDTOUsesEmptyCollections(t *testing.T) {
	dto := makeSnapshot(core.Snapshot{})
	if dto.Tunnels == nil {
		t.Fatal("empty tunnel collection must encode as [] instead of null")
	}
	if dto.Registry == nil {
		t.Fatal("empty registry collection must encode as [] instead of null")
	}
}

func TestTunnelStateNamesAreStableProtocolValues(t *testing.T) {
	cases := map[tunnel.State]string{
		tunnel.StateStopped:      "stopped",
		tunnel.StateRunning:      "running",
		tunnel.StateReconnecting: "reconnecting",
		tunnel.StateError:        "error",
		tunnel.StatePeerOffline:  "peer_offline",
	}
	for state, want := range cases {
		if got := tunnelStateName(state); got != want {
			t.Fatalf("tunnelStateName(%v) = %q, want %q", state, got, want)
		}
	}
}

func TestEventStreamDeliversLogs(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{ID: "events-test", Name: "events-test", Tunnels: []config.Tunnel{}}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	service, _ := core.New(cfg, core.Options{Version: "test"})
	defer service.Close()
	server, err := Start(service, filepath.Join(dir, EndpointFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	}()
	client, err := NewClient(filepath.Join(dir, EndpointFileName))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := client.Events(ctx)
	time.Sleep(20 * time.Millisecond)
	service.Log().Infof("test", "streamed")
	select {
	case event := <-events:
		if event.Log == nil || event.Log.Message != "streamed" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case err := <-errs:
		t.Fatalf("event stream failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("event stream timed out")
	}
}
