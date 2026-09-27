package server

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/proto"
	"tunnelx/internal/store"
)

func TestMigratedKeyConnectsWithoutPublicKeyFileEntry(t *testing.T) {
	s, addr, path := startServerInstance(t, func(c *Config) {
		c.DataDir = t.TempDir()
		if err := os.WriteFile(c.AuthorizedKeys, nil, 0600); err != nil {
			t.Fatal(err)
		}
	})
	fp := ssh.FingerprintSHA256(loadSigner(t, path).PublicKey())
	if s.auth.AuthorizedFingerprint(fp) {
		t.Fatal("fixture already authorized by file")
	}
	if _, err := s.store.DB().Exec(`INSERT INTO device_keys VALUES(?,'migrated-device',0)`, fp); err != nil {
		t.Fatal(err)
	}
	if _, err := (*adminBackend)(s).BindIdentity(context.Background(), adminapi.BindIdentityRequest{ClientID: "migrated-device", Fingerprint: fp, Reason: "migration fixture"}); err != nil {
		t.Fatal(err)
	}
	client := dialWith(t, addr, path)
	defer client.Close()
	_, control := openTestControl(t, client, proto.RoleImporter, "migrated-device")
	defer control.Close()
}

func TestAdministratorPasswordCannotAuthenticateSSH(t *testing.T) {
	s, addr, _ := startServerInstance(t, func(c *Config) { c.DataDir = t.TempDir() })
	_, err := s.store.ProvisionAdminAccount(context.Background(), "admin", "test administrator password", store.AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "admin", Auth: []ssh.AuthMethod{ssh.Password("test administrator password")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second})
	if err == nil {
		c.Close()
		t.Fatal("server accepted SSH password authentication")
	}
	if s.sshCfg.PasswordCallback != nil {
		t.Fatal("password authentication still configured")
	}
}

func TestMigratedKeyOverridesFileAndIsIndependentOfAdminAccount(t *testing.T) {
	s, _, path := startServerInstance(t, func(c *Config) { c.DataDir = t.TempDir() })
	fp := ssh.FingerprintSHA256(loadSigner(t, path).PublicKey())
	ctx := context.Background()
	if _, err := s.store.DB().Exec(`INSERT INTO device_keys VALUES(?,'migrated-device',0)`, fp); err != nil {
		t.Fatal(err)
	}
	if s.fingerprintAuthorized(fp) {
		t.Fatal("disabled device fell back to authorized_keys")
	}
	if _, err := (*adminBackend)(s).BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: "migrated-device", Fingerprint: fp, Reason: "explicit recovery"}); err != nil {
		t.Fatal(err)
	}
	if !s.fingerprintAuthorized(fp) {
		t.Fatal("explicit recovery failed")
	}
	a, err := (*adminBackend)(s).CreateAccount(ctx, adminapi.CreateAccountRequest{Username: "admin", Password: "administrator password", Reason: "test"})
	if err != nil || !a.IsAdmin {
		t.Fatal("account API must create an administrator", err)
	}
	if _, err = (*adminBackend)(s).UpdateAccount(ctx, a.Username, adminapi.UpdateAccountRequest{Password: "replacement administrator password", ExpectedGeneration: a.Generation, Reason: "reset"}); err != nil {
		t.Fatal(err)
	}
	if !s.fingerprintAuthorized(fp) {
		t.Fatal("administrator reset changed device authorization")
	}
	if err = (*adminBackend)(s).RevokeIdentity(ctx, "migrated-device", 1, "revoke"); err != nil {
		t.Fatal(err)
	}
	if s.fingerprintAuthorized(fp) {
		t.Fatal("revoked device fell back to authorized_keys")
	}
}
