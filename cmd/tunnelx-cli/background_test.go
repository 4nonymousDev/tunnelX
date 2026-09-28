package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tunnelx/internal/localapi"
)

func TestBackgroundRejectsInvalidFlags(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--bg"},
		{"run", "--bg", "--confirm-via-api", "--accept-host-key", "SHA256:test"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("run(%q) unexpectedly succeeded", args)
		}
	}
}

func TestBackgroundLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("background lifecycle integration runs on Windows and Linux")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "tunnelx test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	configDir := filepath.Join(root, "config with spaces")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "client.json")
	// No server is required: readiness means the local core is running, not
	// that a remote SSH connection has succeeded.
	if err := os.WriteFile(configPath, []byte(`{"schema_version":2,"id":"background-test","name":"background test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state with spaces")
	endpoint := filepath.Join(stateDir, "custom endpoint.json")
	paths := []string{"--config", filepath.Join("config with spaces", "client.json"), "--state-dir", "state with spaces", "--endpoint", filepath.Join("state with spaces", "custom endpoint.json")}
	invoke := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = root
		cmd.Env = withoutBackgroundStartup(os.Environ())
		// CombinedOutput also detects accidentally inherited parent pipes: it
		// must return while the detached child is still alive.
		cmd.WaitDelay = time.Second
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(endpoint)
		if err != nil {
			return
		}
		var ep localapi.Endpoint
		if json.Unmarshal(data, &ep) == nil && ep.PID > 0 {
			if process, err := os.FindProcess(ep.PID); err == nil {
				_ = process.Kill()
				_ = process.Release()
			}
		}
	})
	args := append([]string{"run", "--bg"}, paths...)
	output, err := invoke(args...)
	if err != nil || !strings.Contains(output, "PID") {
		t.Fatalf("start background: %v\n%s", err, output)
	}
	data, err := os.ReadFile(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var ep localapi.Endpoint
	if err := json.Unmarshal(data, &ep); err != nil || ep.PID <= 0 {
		t.Fatalf("endpoint: %s (%v)", data, err)
	}
	// The launcher has exited; authenticated control must remain available.
	if output, err := invoke(append([]string{"status"}, paths...)...); err != nil {
		t.Fatalf("status after launcher exit: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "tunnelx.log")); err != nil {
		t.Fatalf("background log missing: %v", err)
	}
	if output, err := invoke(args...); err == nil || !strings.Contains(output, "已在运行") {
		t.Fatalf("duplicate launch must report child startup failure: %v\n%s", err, output)
	}
	if output, err := invoke(append([]string{"stop"}, paths...)...); err != nil {
		t.Fatalf("stop: %v\n%s", err, output)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(endpoint); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not remove the control endpoint")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A bad endpoint reaches the child but must never be reported as success.
	badEndpoint := filepath.Join(root, "endpoint is a directory")
	if err := os.Mkdir(badEndpoint, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badEndpoint, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = invoke("run", "--bg", "--config", configPath, "--state-dir", stateDir, "--endpoint", badEndpoint)
	if err == nil || !strings.Contains(output, "后台启动失败") {
		t.Fatalf("invalid endpoint: %v\n%s", err, output)
	}
}
