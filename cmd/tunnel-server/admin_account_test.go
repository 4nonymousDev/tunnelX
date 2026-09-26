package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tunnelx/internal/store"
)

func TestReadAdminPasswordHiddenAndConfirmed(t *testing.T) {
	const password = "  correct horse battery staple  "
	var out bytes.Buffer
	confirmation := []byte(password)
	inputs := [][]byte{[]byte(password), confirmation}
	index := 0
	result, err := readAdminPassword(&out, func() ([]byte, error) {
		value := inputs[index]
		index++
		return value, nil
	})
	defer clear(result)
	if err != nil || string(result) != password || index != 2 {
		t.Fatalf("password changed: reads=%d err=%v", index, err)
	}
	if strings.Contains(out.String(), password) || !bytes.Equal(confirmation, make([]byte, len(confirmation))) {
		t.Fatal("password echoed or confirmation not cleared")
	}
}

func TestReadAdminPasswordFailureNeverReturnsOrEchoesSecret(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		failAt int
	}{{"short", []string{"short"}, -1}, {"mismatch", []string{"correct horse battery staple", "different secret password"}, -1}, {"read-error", []string{"correct horse battery staple"}, 0}, {"confirmation-error", []string{"correct horse battery staple", "secret confirmation value"}, 1}} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			var buffers [][]byte
			index := 0
			result, err := readAdminPassword(&out, func() ([]byte, error) {
				value := []byte(test.values[index])
				buffers = append(buffers, value)
				fail := index == test.failAt
				index++
				if fail {
					return value, errors.New("sensitive source read error")
				}
				return value, nil
			})
			if err == nil || len(result) != 0 {
				t.Fatal("invalid input returned a password")
			}
			for i, buffer := range buffers {
				if !bytes.Equal(buffer, make([]byte, len(buffer))) || strings.Contains(out.String()+err.Error(), test.values[i]) {
					t.Fatal("failed input retained or echoed password")
				}
			}
		})
	}
}

func TestAdminAccountRejectsRedirectedInputBeforeDatabaseMutation(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	dir := filepath.Join(t.TempDir(), "not-created")
	var out bytes.Buffer
	if err = runAdminAccount(dir, "admin", input, &out); err == nil {
		t.Fatal("redirected password accepted")
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid invocation changed data directory: %v", err)
	}
}

func TestProvisionAdminCommandUsesExistingDataAndLeavesKeysUntouched(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"host_key", "authorized_keys"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("unchanged fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	const password = "administrator secret password"
	if err := provisionAdminAccount(dir, "admin", password, &out); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "tunnel-server.db"))
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.AuthenticateAccount(context.Background(), "admin", password)
	if err != nil || !account.IsAdmin || !account.Enabled || account.Generation != 1 {
		t.Fatalf("administrator not persisted: %+v %v", account, err)
	}
	s.Close()
	if err = provisionAdminAccount(dir, "admin", "replacement secret password", &out); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(filepath.Join(dir, "tunnel-server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AuthenticateAccount(context.Background(), "admin", password); !errors.Is(err, store.ErrAccountAuth) {
		t.Fatal("original password still works")
	}
	account, err = s.AuthenticateAccount(context.Background(), "admin", "replacement secret password")
	if err != nil || account.Generation != 2 || !account.IsAdmin {
		t.Fatalf("administrator reset not persisted: %+v %v", account, err)
	}
	for _, name := range []string{"host_key", "authorized_keys"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(content) != "unchanged fixture" {
			t.Fatal("command changed host key or authorized keys")
		}
	}
	if strings.Contains(out.String(), password) || strings.Contains(out.String(), "replacement secret password") {
		t.Fatal("command output exposes password")
	}
}
