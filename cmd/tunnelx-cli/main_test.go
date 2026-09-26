package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/keygen"
)

func TestConfirmationForRunUsesControlAPI(t *testing.T) {
	handler, err := confirmationForRun("", true, os.Stdin)
	if err != nil {
		t.Fatalf("confirmationForRun() error = %v", err)
	}
	if handler != nil {
		t.Fatalf("confirmationForRun() = %#v，API 确认模式必须让 core.Service 管理待确认请求", handler)
	}
}

func TestLoginPasswordRejectsRedirectedInputWithoutReadingOrPrintingIt(t *testing.T) {
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := out.WriteString("must-not-be-echoed\n"); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	var output bytes.Buffer
	password, err := readLoginPassword(in, &output)
	if err == nil || len(password) != 0 || strings.Contains(output.String(), "must-not-be-echoed") {
		t.Fatal("unsafe redirected password handling")
	}
}

func TestRunKeygenCreatesMetadataKeyPair(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "client_key")
	var out bytes.Buffer
	if err := runKeygen([]string{"--username", "alice", "--email", "alice@example.com", "--output", keyPath}, &out); err != nil {
		t.Fatal(err)
	}
	publicData, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, comment, _, rest, err := ssh.ParseAuthorizedKey(publicData)
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("ParseAuthorizedKey: rest=%q err=%v", rest, err)
	}
	metadata, err := keygen.ParseMetadataComment(comment)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Username != "alice" || metadata.Email != "alice@example.com" || metadata.ComputerName != "" {
		t.Fatalf("metadata=%#v", metadata)
	}
	privateData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(privateData)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicKey.Marshal(), signer.PublicKey().Marshal()) {
		t.Fatal("public and private keys do not match")
	}
	if strings.Contains(out.String(), "alice@example.com") {
		t.Fatal("personal key comment printed to terminal")
	}
	for _, want := range []string{"Private key: " + keyPath, "Public key: " + keyPath + ".pub", "Fingerprint: " + ssh.FingerprintSHA256(publicKey), "Authorized key line:", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunKeygenWithoutPersonalMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client_key")
	if err := runKeygen([]string{"--output", path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	_, comment, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil || comment != "" {
		t.Fatalf("unexpected personal metadata %q: %v", comment, err)
	}
}

func TestRunKeygenRefusesOverwrite(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "client_key")
	if err := os.WriteFile(keyPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runKeygen([]string{"--username", "alice", "--email", "alice@example.com", "--output", keyPath}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("error=%v", err)
	}
	data, readErr := os.ReadFile(keyPath)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("existing key changed: data=%q err=%v", data, readErr)
	}
}

func TestRunKeygenRequiresExistingOutputDirectory(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "missing", "client_key")
	err := runKeygen([]string{"--username", "alice", "--email", "alice@example.com", "--output", keyPath}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "输出目录不可用") {
		t.Fatalf("error=%v", err)
	}
}

func TestConfirmationForRunRejectsConflictingModes(t *testing.T) {
	_, err := confirmationForRun("SHA256:test", true, os.Stdin)
	if err == nil {
		t.Fatal("同时启用 API 确认和固定指纹时应返回错误")
	}
	if !strings.Contains(err.Error(), "不能与") {
		t.Fatalf("error = %q，应解释两个确认选项冲突", err)
	}
}
