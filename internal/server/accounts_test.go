package server

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/enrollment"
	"tunnelx/internal/proto"
	"tunnelx/internal/sshconn"
	"tunnelx/internal/store"
)

const accountTestPassword = "correct horse battery staple"

func accountServer(t *testing.T) (*Server, string, string, store.Account) {
	t.Helper()
	s, addr, path := startServerInstance(t, func(c *Config) { c.DataDir = t.TempDir(); c.IdentityBindings = nil })
	a, err := (*adminBackend)(s).CreateAccount(context.Background(), adminapi.CreateAccountRequest{Username: "alice", Password: accountTestPassword, MaxDevices: 2, Reason: "test administrator provisioning"})
	if err != nil {
		t.Fatal(err)
	}
	return s, addr, path, a
}
func enrollAccount(t *testing.T, addr, keyPath, id string) error {
	t.Helper()
	d := sshconn.Dialer{Addr: addr, KeyPath: keyPath, KnownHosts: filepath.Join(filepath.Dir(keyPath), "known_hosts"), Prompt: func(string, string) bool { return true }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.Enroll(ctx, "alice", accountTestPassword, id, "test device")
}
func accountPasswordConn(t *testing.T, addr, password string) *ssh.Client {
	t.Helper()
	c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "alice", Auth: []ssh.AuthMethod{ssh.Password(password)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAccountLoginAutomaticallyEnrollsNewKeyAndTunnels(t *testing.T) {
	s, addr, legacy, _ := accountServer(t)
	keyPath := filepath.Join(filepath.Dir(legacy), "automatic_key")
	genKey(t, keyPath)
	key := loadSigner(t, keyPath)
	fp := ssh.FingerprintSHA256(key.PublicKey())
	if s.auth.AuthorizedFingerprint(fp) {
		t.Fatal("fixture key already manually authorized")
	}
	if err := enrollAccount(t, addr, keyPath, "new-device"); err != nil {
		t.Fatal(err)
	}
	if s.auth.AuthorizedFingerprint(fp) {
		t.Fatal("account login wrote a second authorization source")
	}
	if !s.fingerprintAuthorized(fp) {
		t.Fatal("account key not authorized")
	}
	client := dialWith(t, addr, keyPath)
	defer client.Close()
	pc, ch := openTestControl(t, client, proto.RoleExporter, "new-device")
	defer ch.Close()
	forwarded := client.HandleChannelOpen(proto.ForwardChannelType)
	ack := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "kept-tunnel", SrcHost: "127.0.0.1", SrcPort: 8080}})
	if len(ack.Targets) != 1 || ack.Targets[0].Fingerprint != fp {
		t.Fatalf("account exporter not published: %+v", ack)
	}
	// A second device uses the same account while retaining a distinct key/ID.
	if err := enrollAccount(t, addr, legacy, "second-device"); err != nil {
		t.Fatal(err)
	}
	visitor := dialWith(t, addr, legacy)
	defer visitor.Close()
	_, visitorCh := openTestControl(t, visitor, proto.RoleImporter, "second-device")
	defer visitorCh.Close()
	var visitorID string
	for _, sess := range s.sessions.Snapshot() {
		if sess.ClientID == "second-device" {
			visitorID = sess.ID
		}
	}
	lease, err := s.acquire(visitorID, ack.Targets[0])
	if err != nil {
		t.Fatalf("account keys cannot acquire existing forwarding path: %v", err)
	}
	lease.Close()
	completed := make(chan error, 1)
	go func() {
		select {
		case incoming := <-forwarded:
			out, requests, err := incoming.Accept()
			if err != nil {
				completed <- err
				return
			}
			defer out.Close()
			go ssh.DiscardRequests(requests)
			data, err := io.ReadAll(out)
			if err == nil {
				_, err = out.Write(append([]byte("OK:"), data...))
			}
			_ = out.CloseWrite()
			completed <- err
		case <-time.After(3 * time.Second):
			completed <- errors.New("account exporter received no forwarded channel")
		}
	}()
	payload, err := proto.EncodeOpen(ack.Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	business, requests, err := visitor.OpenChannel(proto.OpenChannelType, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer business.Close()
	go ssh.DiscardRequests(requests)
	timer := time.AfterFunc(3*time.Second, func() { _ = visitor.Close(); _ = client.Close() })
	defer timer.Stop()
	if _, err = business.Write([]byte("account request")); err != nil {
		t.Fatal(err)
	}
	_ = business.CloseWrite()
	response, err := io.ReadAll(business)
	if err != nil || string(response) != "OK:account request" {
		t.Fatalf("account forwarding response %q: %v", response, err)
	}
	if err = <-completed; err != nil {
		t.Fatal(err)
	}
}

func TestAccountDisableAndResetCannotFallBackToAuthorizedFile(t *testing.T) {
	for _, change := range []string{"disable", "reset"} {
		t.Run(change, func(t *testing.T) {
			s, addr, keyPath, account := accountServer(t)
			key := loadSigner(t, keyPath)
			fp := ssh.FingerprintSHA256(key.PublicKey())
			if err := enrollAccount(t, addr, keyPath, "original-device"); err != nil {
				t.Fatal(err)
			}
			client := dialWith(t, addr, keyPath)
			defer client.Close()
			_, channel := openTestControl(t, client, proto.RoleExporter, "original-device")
			defer channel.Close()
			request := adminapi.UpdateAccountRequest{ExpectedGeneration: account.Generation, Reason: "security event"}
			if change == "disable" {
				disabled := false
				request.Enabled = &disabled
			} else {
				request.Password = "a different secure password"
			}
			if _, err := (*adminBackend)(s).UpdateAccount(context.Background(), account.Username, request); err != nil {
				t.Fatal(err)
			}
			if !s.auth.AuthorizedFingerprint(fp) {
				t.Fatal("fixture no longer demonstrates file fallback")
			}
			if s.fingerprintAuthorized(fp) || s.validateIdentity(fp, "original-device") == nil {
				t.Fatal("account revocation bypassed through original key file")
			}
			if len(s.sessions.Snapshot()) != 0 {
				t.Fatal("account update retained business session")
			}
			if err := enrollAccount(t, addr, keyPath, "original-device"); err == nil {
				t.Fatal("old password restored revoked account credentials")
			}
			if c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "tunnelx", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second}); err == nil {
				c.Close()
				t.Fatal("revoked key authenticated")
			}
		})
	}
}

func TestPasswordTransportCannotOpenBusinessChannels(t *testing.T) {
	s, addr, _, _ := accountServer(t)
	for _, kind := range []string{proto.ChannelType, proto.OpenChannelType, "session", "direct-tcpip"} {
		client := accountPasswordConn(t, addr, accountTestPassword)
		if ch, _, err := client.OpenChannel(kind, nil); err == nil {
			ch.Close()
			t.Fatalf("password session opened %s", kind)
		}
		client.Close()
	}
	if len(s.sessions.Snapshot()) != 0 {
		t.Fatal("password login entered business session registry")
	}
}

func TestAccountEnrollmentRejectsReplayedProofAndOldEpoch(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "replayed_proof", true: "account_changed_after_password"}[stale], func(t *testing.T) {
			s, addr, keyPath, a := accountServer(t)
			key := loadSigner(t, keyPath)
			client := accountPasswordConn(t, addr, accountTestPassword)
			sessionID := client.SessionID()
			if !stale {
				sessionID = []byte("another SSH session")
			} else {
				if _, err := (*adminBackend)(s).UpdateAccount(context.Background(), a.Username, adminapi.UpdateAccountRequest{Password: "different secure password", ExpectedGeneration: a.Generation, Reason: "reset during login"}); err != nil {
					t.Fatal(err)
				}
			}
			sig, err := key.Sign(rand.Reader, enrollment.SigningPayload(sessionID, "alice", "untrusted-id", key.PublicKey()))
			if err != nil {
				t.Fatal(err)
			}
			ch, requests, err := client.OpenChannel(enrollment.ChannelType, nil)
			if err != nil {
				t.Fatal(err)
			}
			go ssh.DiscardRequests(requests)
			defer ch.Close()
			if err = enrollment.Write(ch, enrollment.Request{ClientID: "untrusted-id", PublicKey: string(ssh.MarshalAuthorizedKey(key.PublicKey())), Signature: ssh.Marshal(sig)}); err != nil {
				t.Fatal(err)
			}
			var response enrollment.Response
			if err = enrollment.Read(ch, &response); err != nil {
				t.Fatal(err)
			}
			if response.OK {
				t.Fatal("replayed or stale account proof accepted")
			}
			if _, err = s.store.GetIdentity(context.Background(), "untrusted-id"); !errors.Is(err, store.ErrIdentityUnbound) {
				t.Fatalf("failed proof created identity: %v", err)
			}
		})
	}
}

func TestAccountClaimsExistingIdentityWithoutChangingKeyOrGeneration(t *testing.T) {
	s, addr, keyPath, _ := accountServer(t)
	ctx := context.Background()
	key := loadSigner(t, keyPath)
	pub := string(ssh.MarshalAuthorizedKey(key.PublicKey()))
	fp := ssh.FingerprintSHA256(key.PublicKey())
	if _, err := (*adminBackend)(s).ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "kept-id", PublicKey: pub, Username: "kept metadata", Reason: "legacy registration"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.store.GetIdentity(ctx, "kept-id")
	if err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = enrollAccount(t, addr, keyPath, "kept-id"); err != nil {
		t.Fatal(err)
	}
	after, err := s.store.GetIdentity(ctx, "kept-id")
	if err != nil || before != after {
		t.Fatalf("claim changed device identity: %+v %+v %v", before, after, err)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil || string(keyAfter) != string(keyBefore) {
		t.Fatal("claim replaced original private key")
	}
	client, err := s.store.GetClient(ctx, fp)
	if err != nil || client.Username != "kept metadata" {
		t.Fatalf("claim lost original metadata: %+v %v", client, err)
	}
}

func TestLoginRateLimitsAreBoundedAndExpire(t *testing.T) {
	var limiter loginLimiter
	now := time.Now()
	for i := 0; i < 10; i++ {
		if !limiter.allow("192.0.2.1", "alice", now) {
			t.Fatal("unexpected early throttling")
		}
	}
	if limiter.allow("192.0.2.2", "alice", now) {
		t.Fatal("distributed account guessing not throttled")
	}
	if !limiter.allow("192.0.2.2", "alice", now.Add(5*time.Minute)) {
		t.Fatal("account permanently locked")
	}
	if len(limiter.windows) > 2048 {
		t.Fatal("unbounded login limiter")
	}
}

func TestAccountUncertainUpdateCommitClosesOwnedSessions(t *testing.T) {
	s, addr, keyPath, account := accountServer(t)
	if err := enrollAccount(t, addr, keyPath, "original-id"); err != nil {
		t.Fatal(err)
	}
	client := dialWith(t, addr, keyPath)
	defer client.Close()
	_, ch := openTestControl(t, client, proto.RoleExporter, "original-id")
	defer ch.Close()
	for _, query := range []string{
		`CREATE TABLE account_commit_fault(fingerprint TEXT REFERENCES clients(fingerprint) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER fail_account_commit BEFORE UPDATE ON accounts BEGIN INSERT INTO account_commit_fault VALUES('missing-client'); END`,
	} {
		if _, err := s.store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	disabled := false
	_, err := (*adminBackend)(s).UpdateAccount(context.Background(), "alice", adminapi.UpdateAccountRequest{Enabled: &disabled, ExpectedGeneration: account.Generation, Reason: "disable"})
	if !errors.Is(err, store.ErrCommitOutcomeUnknown) {
		t.Fatalf("uncertain commit hidden: %v", err)
	}
	if len(s.sessions.Snapshot()) != 0 {
		t.Fatal("owned sessions survived uncertain commit")
	}
	if ok, _ := s.policy.Admit("unrelated", nil); ok {
		t.Fatal("uncertain account commit did not pause admission")
	}
}

func TestAccountRevokedDeviceCanBeExplicitlyRestored(t *testing.T) {
	s, addr, keyPath, account := accountServer(t)
	ctx := context.Background()
	if err := enrollAccount(t, addr, keyPath, "kept-id"); err != nil {
		t.Fatal(err)
	}
	b := (*adminBackend)(s)
	binding, err := s.store.GetIdentity(ctx, "kept-id")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.RevokeIdentity(ctx, binding.ClientID, binding.Generation, "retired"); err != nil {
		t.Fatal(err)
	}
	if err = enrollAccount(t, addr, keyPath, "kept-id"); err == nil {
		t.Fatal("password login implicitly restored revoked device")
	}
	binding, err = s.store.GetIdentity(ctx, "kept-id")
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err = b.UpdateAccount(ctx, account.Username, adminapi.UpdateAccountRequest{Enabled: &disabled, ExpectedGeneration: account.Generation, Reason: "disabled"}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: "different-id", Fingerprint: binding.Fingerprint, Reason: "must not move managed key"}); !errors.Is(err, adminapi.ErrConflict) {
		t.Fatalf("managed key moved to another identity: %v", err)
	}
	if _, err = b.BindIdentity(ctx, adminapi.BindIdentityRequest{ClientID: binding.ClientID, Fingerprint: binding.Fingerprint, ExpectedGeneration: binding.Generation, Reason: "explicit administrator restore"}); err != nil {
		t.Fatalf("administrator cannot restore owned identity: %v", err)
	}
	if s.fingerprintAuthorized(binding.Fingerprint) {
		t.Fatal("identity restoration bypassed disabled account")
	}
	enabled := true
	if _, err = b.UpdateAccount(ctx, account.Username, adminapi.UpdateAccountRequest{Enabled: &enabled, ExpectedGeneration: account.Generation + 1, Reason: "explicit administrator reenable"}); err != nil {
		t.Fatal(err)
	}
	if s.fingerprintAuthorized(binding.Fingerprint) {
		t.Fatal("reenable reused stale remembered credential")
	}
	if err = enrollAccount(t, addr, keyPath, "kept-id"); err != nil {
		t.Fatalf("cannot log in after explicit restoration: %v", err)
	}
	if !s.fingerprintAuthorized(binding.Fingerprint) {
		t.Fatal("restored device remains unusable")
	}
}
