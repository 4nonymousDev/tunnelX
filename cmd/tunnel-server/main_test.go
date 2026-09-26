package main

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerPreflightDoesNotProvisionOrMigrate(t *testing.T) {
	if os.Getenv("TUNNELX_TEST_SERVER_MAIN") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"tunnel-server"}, os.Args[i+1:]...)
				break
			}
		}
		flag.CommandLine = flag.NewFlagSet("tunnel-server", flag.ExitOnError)
		main()
		return
	}
	auth := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(auth, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{{"version", []string{"-version"}, "tunnel-server"}, {"authorized-keys", []string{"-check-auth", "-auth", auth}, "authorized_keys: valid"}} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "must-not-exist")
			args := append([]string{"-test.run=^TestServerPreflightDoesNotProvisionOrMigrate$", "--"}, test.args...)
			args = append(args, "-data-dir", dir, "-admin-account", "admin", "-admin-token-file", "deprecated-missing-file")
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "TUNNELX_TEST_SERVER_MAIN=1")
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("preflight failed: %v %s", err, output)
			}
			if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preflight modified server data: %v", err)
			}
		})
	}
}
