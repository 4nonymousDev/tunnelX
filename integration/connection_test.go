//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/keygen"
	"tunnelx/internal/localapi"
	"tunnelx/internal/store"
	"tunnelx/internal/updatepolicy"
)

// This suite runs the real server and two CLI binaries as separate processes.
// All listeners and temporary identities belong to this test, never a live server.
func TestServerAndCLIConnection(t *testing.T) {
	binDir := os.Getenv("TUNNELX_TEST_BIN_DIR")
	if binDir == "" {
		t.Fatal("set TUNNELX_TEST_BIN_DIR to the directory containing tunnel-server and tunnelx-cli")
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	serverBin, cliBin := filepath.Join(binDir, "tunnel-server"+ext), filepath.Join(binDir, "tunnelx-cli"+ext)
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "server-data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	host, err := keygen.Generate(dir, "host_key", "")
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(dir, "authorized_keys")
	if err = os.WriteFile(authPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	password := randomText(t)
	db, err := store.Open(filepath.Join(dataDir, "tunnel-server.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ProvisionAdminAccount(context.Background(), "ci-admin", password, store.AdminAction{Reason: "isolated integration fixture"})
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	server := startProcess(t, filepath.Join(dir, "server.log"), serverBin, "-addr", "127.0.0.1:0", "-admin-addr", "127.0.0.1:0", "-hostkey", host.KeyPath, "-auth", authPath, "-data-dir", dataDir)
	var sshAddr, adminURL string
	waitFor(t, "server listeners", func() bool {
		server.assertRunning(t)
		data, _ := os.ReadFile(server.logPath)
		for _, line := range strings.Split(string(data), "\n") {
			if before, after, ok := strings.Cut(line, "监听 "); ok {
				fields := strings.Fields(after)
				if len(fields) == 0 {
					continue
				}
				if strings.Contains(before, "管理端已启动") {
					adminURL = "http://" + fields[0]
				}
				if strings.Contains(before, "tunnel-server") {
					sshAddr = strings.Split(fields[0], "（")[0]
				}
			}
		}
		return sshAddr != "" && adminURL != ""
	})
	t.Log("server is listening; authenticate administrator and register device public keys")
	jar, _ := cookiejar.New(nil)
	admin := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	adminJSON(t, admin, adminURL, "POST", "/auth/login", "", map[string]any{"username": "ci-admin", "password": password}, &session)
	if session.CSRF == "" {
		t.Fatal("admin login did not return CSRF token")
	}
	want := randomText(t)
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, want) }))
	defer echo.Close()
	echoPort := echo.Listener.Addr().(*net.TCPAddr).Port
	importListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	importPort := importListener.Addr().(*net.TCPAddr).Port
	_ = importListener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	newClient := func(id string, tunnel config.Tunnel) (*localapi.Client, string) {
		clientDir := filepath.Join(dir, id)
		if err := os.MkdirAll(clientDir, 0700); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(clientDir, "config.json")
		key, err := keygen.Generate(clientDir, "device_key", "")
		if err != nil {
			t.Fatal(err)
		}
		publicKey, err := os.ReadFile(key.PubPath)
		if err != nil {
			t.Fatal(err)
		}
		adminJSON(t, admin, adminURL, "POST", "/clients/import-key", session.CSRF, map[string]any{"client_id": id, "public_key": string(publicKey), "reason": "connection smoke test"}, nil)
		cfg := config.Config{KeyPath: key.KeyPath, SchemaVersion: 2, ID: id, Name: id, ServerAddr: sshAddr, Tunnels: []config.Tunnel{tunnel}}
		body, _ := json.Marshal(cfg)
		if err := os.WriteFile(cfgPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		endpoint := filepath.Join(clientDir, localapi.EndpointFileName)
		process := startProcess(t, filepath.Join(clientDir, "cli.log"), cliBin, "run", "--config", cfgPath, "--endpoint", endpoint, "--accept-host-key", host.Fingerprint)
		var client *localapi.Client
		waitFor(t, id+" local API", func() bool {
			process.assertRunning(t)
			var err error
			client, err = localapi.NewClient(endpoint)
			if err != nil {
				return false
			}
			_, err = client.Snapshot(ctx)
			return err == nil
		})
		waitFor(t, id+" authenticated connection", func() bool { s, err := client.Snapshot(ctx); return err == nil && s.Connection.State == "connected" })
		return client, cfgPath
	}
	exporter, exportConfig := newClient("ci-exporter", config.Tunnel{ID: "http", Kind: config.KindExport, Enabled: true, LocalHost: "127.0.0.1", LocalPort: echoPort})
	importer, importConfig := newClient("ci-importer", config.Tunnel{ID: "local-http", Kind: config.KindImport, Enabled: true, PeerID: "ci-exporter", PeerTunnelID: "http", ListenPort: importPort})
	client := &http.Client{Timeout: time.Second}
	checkTraffic := func() bool {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", importPort))
		if err != nil {
			return false
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
		return err == nil && string(body) == want
	}
	waitFor(t, "HTTP through both SSH clients", checkTraffic)
	t.Log("public-key registration and end-to-end HTTP forwarding passed")
	// Snapshot the already resolved identities before applying a version policy.
	before := map[string][]byte{}
	for _, path := range []string{exportConfig, importConfig} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = body
		var cfg config.Config
		if err = json.Unmarshal(body, &cfg); err != nil {
			t.Fatal(err)
		}
		key := cfg.KeyPath
		if !filepath.IsAbs(key) {
			key = filepath.Join(filepath.Dir(path), key)
		}
		before[key], err = os.ReadFile(key)
		if err != nil {
			t.Fatal(err)
		}
	}
	var policy updatepolicy.Policy
	adminJSON(t, admin, adminURL, "GET", "/client-update-policy", "", nil, &policy)
	deadline := time.Now().Add(time.Hour)
	setPolicy := func(minimum string, after *time.Time) {
		adminJSON(t, admin, adminURL, "PUT", "/client-update-policy", session.CSRF, map[string]any{"minimum_version": minimum, "enforce_after": after, "expected_generation": policy.Generation, "reason": "smoke test"}, &policy)
	}
	setPolicy("999.0.0", &deadline)
	waitFor(t, "grace notice", func() bool {
		s, err := importer.Snapshot(ctx)
		return err == nil && s.UpdatePolicy != nil && s.UpdatePolicy.Required && !s.UpdatePolicy.Blocked
	})
	if !checkTraffic() {
		t.Fatal("grace period interrupted traffic")
	}
	deadline = time.Now().Add(-time.Second)
	setPolicy("999.0.0", &deadline)
	for _, c := range []*localapi.Client{exporter, importer} {
		waitFor(t, "required-update disconnection", func() bool {
			s, err := c.Snapshot(ctx)
			return err == nil && s.Connection.State == "failed" && s.UpdatePolicy != nil && s.UpdatePolicy.Blocked
		})
	}
	if checkTraffic() {
		t.Fatal("expired clients still forwarded traffic")
	}
	setPolicy("", nil)
	if err = exporter.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err = importer.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "traffic after policy cancellation", checkTraffic)
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("key/config changed after policy enforcement: %s", filepath.Base(path))
		}
	}
	t.Log("grace, deadline enforcement, cancellation and preserved keys/config passed")
	for _, c := range []*localapi.Client{importer, exporter} {
		if err = c.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

type process struct {
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	logPath string
}

func startProcess(t *testing.T, logPath, executable string, args ...string) *process {
	t.Helper()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	p := &process{cmd: exec.Command(executable, args...), done: make(chan struct{}), logPath: logPath}
	p.cmd.Stdout, p.cmd.Stderr = log, log
	if err = p.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	go func() { p.err = p.cmd.Wait(); log.Close(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = p.cmd.Process.Signal(os.Interrupt)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}
func (p *process) assertRunning(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		t.Fatalf("%s exited early: %v", filepath.Base(p.cmd.Path), p.err)
	default:
	}
}
func waitFor(t *testing.T, name string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
}
func randomText(t *testing.T) string {
	t.Helper()
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw[:])
}
func adminJSON(t *testing.T, client *http.Client, origin, method, path, csrf string, body, out any) {
	t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, origin+"/api/v1"+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("admin %s %s: HTTP %d", method, path, response.StatusCode)
	}
	if out != nil {
		if err = json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}
