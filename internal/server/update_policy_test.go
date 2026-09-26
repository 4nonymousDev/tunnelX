package server

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/config"
	"tunnelx/internal/control"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/proto"
	"tunnelx/internal/store"
	"tunnelx/internal/updatepolicy"
)

func updatePolicyServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	s, addr, key := startServerInstance(t, func(c *Config) { c.DataDir = t.TempDir(); c.IdentityBindings = nil })
	fp := ssh.FingerprintSHA256(loadSigner(t, key).PublicKey())
	for _, id := range []string{"cross-exporter", "cross-importer", "same-conn-test"} {
		if _, err := s.store.BindIdentity(context.Background(), id, fp, 0, store.AdminAction{}); err != nil {
			t.Fatal(err)
		}
	}
	return s, addr, key
}

func setUpdatePolicy(t *testing.T, s *Server, minimum string, deadline *time.Time) updatepolicy.Policy {
	t.Helper()
	b := (*adminBackend)(s)
	old, err := b.GetClientUpdatePolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.SetClientUpdatePolicy(context.Background(), adminapi.UpdatePolicyRequest{MinimumVersion: minimum, EnforceAfter: deadline, ExpectedGeneration: old.Generation, Reason: "test policy"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func versionControl(t *testing.T, client *ssh.Client, id, version string) *control.Client {
	t.Helper()
	c, err := control.Dial(client, &config.Config{ID: id, Name: id}, proto.RoleImporter, version, logbuf.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestUpdatePolicyGraceKeepsLegacyV2TrafficThenClosesIt(t *testing.T) {
	s, addr, key := updatePolicyServer(t)
	deadline := time.Now().Add(time.Hour)
	setUpdatePolicy(t, s, "0.2.1", &deadline)
	owner := dialWith(t, addr, key)
	// The old v2 reader only understands HelloOK/Registry/PublishOK and ignores
	// extra JSON fields. An unknown version is allowed throughout the grace period.
	pc, ownerControl := openTestControl(t, owner, proto.RoleExporter, "cross-exporter")
	defer ownerControl.Close()
	incoming := owner.HandleChannelOpen(proto.ForwardChannelType)
	target := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "http", SrcHost: "127.0.0.1", SrcPort: 8080}}).Targets[0]
	forwardedDone := make(chan struct{})
	go func() {
		defer close(forwardedDone)
		next, ok := <-incoming
		if !ok {
			return
		}
		ch, reqs, err := next.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		go ssh.DiscardRequests(reqs)
		_, _ = io.Copy(ch, ch)
	}()
	visitor := dialWith(t, addr, key)
	visitorControl := versionControl(t, visitor, "cross-importer", "0.2.1")
	payload, _ := proto.EncodeOpen(target)
	ch, reqs, err := visitor.OpenChannel(proto.OpenChannelType, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	timer := time.AfterFunc(10*time.Second, func() { owner.Close(); visitor.Close() })
	defer timer.Stop()
	if _, err = ch.Write([]byte("works")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err = io.ReadFull(ch, buf); err != nil || string(buf) != "works" {
		t.Fatalf("grace traffic=%q %v", buf, err)
	}
	// No policy save or restart is needed when this fixed deadline elapses.
	deadline = time.Now().Add(500 * time.Millisecond)
	setUpdatePolicy(t, s, "0.2.1", &deadline)
	go func() {
		for {
			if _, _, err := pc.Recv(); err != nil {
				return
			}
		}
	}()
	readDone := make(chan error, 1)
	go func() { _, err := ch.Read(make([]byte, 1)); readDone <- err }()
	select {
	case err = <-readDone:
		if err == nil {
			t.Fatal("old-version channel stayed open")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadline did not close active forwarding")
	}
	select {
	case <-forwardedDone:
	case <-time.After(3 * time.Second):
		t.Fatal("owner channel leaked")
	}
	select {
	case <-visitorControl.Done():
		t.Fatalf("supported client disconnected: %v", visitorControl.Cause())
	default:
	}
	if _, err = s.acquire(visitorControlSession(s, "cross-importer"), target); err == nil {
		t.Fatal("expired target admitted")
	}
}

func visitorControlSession(s *Server, id string) string {
	for _, session := range s.sessions.Snapshot() {
		if session.ClientID == id {
			return session.ID
		}
	}
	return ""
}

func TestUpdatePolicyNotificationRejectionCancellationAndSupportedVersions(t *testing.T) {
	s, addr, key := updatePolicyServer(t)
	client := dialWith(t, addr, key)
	c := versionControl(t, client, "same-conn-test", "0.2.0")
	if c.UpdatePolicy() != nil {
		t.Fatal("new server unexpectedly required an update")
	}
	deadline := time.Now().Add(time.Hour)
	p := setUpdatePolicy(t, s, "0.2.1", &deadline)
	waitPolicy := func(generation int64, disabled bool) {
		t.Helper()
		end := time.Now().Add(4 * time.Second)
		for time.Now().Before(end) {
			got := c.UpdatePolicy()
			if disabled && got == nil {
				return
			}
			if !disabled && got != nil && got.Generation == generation {
				if !got.Required || got.Blocked {
					t.Fatalf("grace state=%+v", got)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("live policy notification missing")
	}
	waitPolicy(p.Generation, false)
	setUpdatePolicy(t, s, "", nil)
	waitPolicy(0, true)
	deadline = time.Now().Add(-time.Second)
	setUpdatePolicy(t, s, "0.2.1", &deadline)
	select {
	case <-c.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("old control not stopped")
	}
	var required *proto.Error
	if !errors.As(c.Cause(), &required) || required.Code != proto.CodeUpdateRequired || required.UpdatePolicy == nil || !required.UpdatePolicy.Blocked {
		t.Fatalf("lost typed update error: %v", c.Cause())
	}
	for _, version := range []string{"0.2.0", "dev", "0.2.1-rc.1"} {
		conn := dialWith(t, addr, key)
		_, err := control.Dial(conn, &config.Config{ID: "same-conn-test"}, proto.RoleImporter, version, logbuf.New(nil))
		if !errors.As(err, &required) || required.Code != proto.CodeUpdateRequired {
			t.Fatalf("%s rejection=%v", version, err)
		}
		conn.Close()
	}
	current := dialWith(t, addr, key)
	versionControl(t, current, "cross-importer", "0.2.1+build.5")
	setUpdatePolicy(t, s, "", nil)
	restored := dialWith(t, addr, key)
	versionControl(t, restored, "cross-exporter", "0.2.0")
}

func TestUpdatePolicyReloadAndUncertainCommit(t *testing.T) {
	s, addr, key := updatePolicyServer(t)
	deadline := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	setUpdatePolicy(t, s, "0.2.1", &deadline)
	cfg := s.cfg
	s.Close()
	reopened, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := reopened.clientUpdateStatus("0.2.0")
	if status == nil || !status.Blocked || !status.EnforceAfter.Equal(deadline) {
		t.Fatalf("restart reset policy: %+v", status)
	}
	reopened.Close()

	s, addr, key = updatePolicyServer(t)
	versionControl(t, dialWith(t, addr, key), "same-conn-test", "0.2.1")
	for _, query := range []string{
		`CREATE TABLE policy_commit_fault(fingerprint TEXT REFERENCES clients(fingerprint) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER fail_policy_commit BEFORE UPDATE ON client_update_policy BEGIN INSERT INTO policy_commit_fault VALUES('missing-client'); END`,
	} {
		if _, err = s.store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	_, err = (*adminBackend)(s).SetClientUpdatePolicy(context.Background(), adminapi.UpdatePolicyRequest{MinimumVersion: "0.2.1", EnforceAfter: &deadline, ExpectedGeneration: 1, Reason: "test failure"})
	if !errors.Is(err, store.ErrCommitOutcomeUnknown) {
		t.Fatalf("uncertain commit=%v", err)
	}
	if len(s.sessions.Snapshot()) != 0 {
		t.Fatal("sessions survived uncertain policy commit")
	}
	if ok, _ := s.policy.Admit("unrelated", nil); ok {
		t.Fatal("uncertain update did not pause admission")
	}
}
