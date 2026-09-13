package server

import (
	"crypto/rand"
	"crypto/rsa"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testAuthorizedLine(t *testing.T) (ssh.PublicKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pub, ssh.MarshalAuthorizedKey(pub)
}
func TestAuthKeysStrictReloadAndEmpty(t *testing.T) {
	pub, line := testAuthorizedLine(t)
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, line, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newAuthKeys(path, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if !a.AuthorizedFingerprint(ssh.FingerprintSHA256(pub)) {
		t.Fatal("fingerprint not authorized")
	}
	if err = os.WriteFile(path, append(line, []byte("not a key\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.checkedAt = time.Now().Add(-reloadInterval)
	a.mu.Unlock()
	a.maybeReload()
	if !a.Authorized(pub) || a.Count() != 1 {
		t.Fatal("failed reload replaced snapshot")
	}
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.checkedAt = time.Now().Add(-reloadInterval)
	a.mu.Unlock()
	a.maybeReload()
	if a.Count() != 0 {
		t.Fatal("valid empty file did not revoke last key")
	}
}
