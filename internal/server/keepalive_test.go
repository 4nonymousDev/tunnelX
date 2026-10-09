package server

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

func TestServerHeartbeatReleasesBlackholedIdentity(t *testing.T) {
	s, addr, keyPath := startServerInstance(t, func(c *Config) {
		c.HeartbeatInterval = 20 * time.Millisecond
		c.HeartbeatTimeout = 200 * time.Millisecond
	})
	proxyAddr, blackhole := startHeartbeatBlackhole(t, addr)
	oldClient := dialWith(t, proxyAddr, keyPath)
	_, oldControl := openTestControl(t, oldClient, proto.RoleExporter, "cross-exporter")
	defer oldControl.Close()
	oldSessions := s.sessions.Snapshot()
	if len(oldSessions) != 1 {
		t.Fatalf("initial sessions: %+v", oldSessions)
	}
	oldID := oldSessions[0].ID
	blackhole()
	// The proxy leaves both sockets open, so a new transport initially sees the
	// stale identity; only the server's independent watchdog can release it.
	duplicate := dialWith(t, addr, keyPath)
	expectHeartbeatDuplicate(t, duplicate, "cross-exporter", "duplicate")
	if _, ok := s.sessions.Get(oldID); !ok {
		t.Fatal("duplicate rejection removed the original holder")
	}
	waitHeartbeatCondition(t, func() bool {
		_, live := s.sessions.Get(oldID)
		return !live
	}, "blackholed identity was not released")

	reconnected := dialWith(t, addr, keyPath)
	_, control := openTestControl(t, reconnected, proto.RoleExporter, "cross-exporter")
	defer control.Close()
	xs := s.sessions.Snapshot()
	if len(xs) != 1 || xs[0].ID == oldID || xs[0].State != session.Online {
		t.Fatalf("reconnection did not acquire a new session: %+v", xs)
	}
}

func TestServerHeartbeatKeepsHealthyIdleHolder(t *testing.T) {
	s, addr, keyPath := startServerInstance(t, func(c *Config) {
		c.HeartbeatInterval = 20 * time.Millisecond
		c.HeartbeatTimeout = 200 * time.Millisecond
	})
	client := dialWith(t, addr, keyPath)
	pc, control := openTestControl(t, client, proto.RoleExporter, "cross-exporter")
	defer control.Close()
	initial := s.sessions.Snapshot()[0].ID
	// ssh.Client replies false to unknown global requests. A negative reply is
	// still evidence of a healthy transport, including while control is idle.
	time.Sleep(3 * s.cfg.HeartbeatTimeout)
	if current, ok := s.sessions.Get(initial); !ok || current.State != session.Online {
		t.Fatal("healthy idle session expired")
	}
	for i := 0; i < 3; i++ {
		duplicate := dialWith(t, addr, keyPath)
		expectHeartbeatDuplicate(t, duplicate, "cross-exporter", "duplicate")
		_ = duplicate.Close()
	}
	if _, ok := s.sessions.Get(initial); !ok {
		t.Fatal("duplicate rejection removed healthy holder")
	}
	ack := publishManagementTunnels(t, pc, []proto.TunnelSpec{{TunnelID: "web", SrcPort: 8080}})
	if len(ack.Targets) != 1 || ack.Targets[0].SessionID != initial {
		t.Fatalf("holder lost publication authority: %+v", ack)
	}
}

func TestDuplicateHelloPreservesHolderMetadataAndAudit(t *testing.T) {
	cfg := managementConfig(t)
	cfg.AdminAddr = ""
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keyPath := filepath.Join(filepath.Dir(cfg.HostKeyPath), "client_key")
	key := loadSigner(t, keyPath)
	fp := ssh.FingerprintSHA256(key.PublicKey())
	if _, err = (*adminBackend)(s).ImportPublicKey(ctx, adminapi.ImportPublicKeyRequest{ClientID: "original-id", PublicKey: string(ssh.MarshalAuthorizedKey(key.PublicKey())), Reason: "test registration"}); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	addr, _ := startTestListener(t, s)
	client := dialWith(t, addr, keyPath)
	_, control := openTestControl(t, client, proto.RoleExporter, "original-id")
	defer control.Close()
	duplicate := dialWith(t, addr, keyPath)
	expectHeartbeatDuplicate(t, duplicate, "original-id", "must not overwrite")
	current, err := s.store.GetClient(ctx, fp)
	if err != nil || current.LastReportedName != "original-id" || current.LastRole != proto.RoleExporter || current.LastVersion != "test" {
		t.Fatalf("duplicate changed active client metadata: %+v, %v", current, err)
	}
	waitHeartbeatCondition(t, func() bool {
		rows, e := s.store.ListConnections(ctx, store.AuditFilter{Fingerprint: fp, Limit: 10})
		if e != nil {
			return false
		}
		for _, row := range rows {
			if row.DisconnectReason == "duplicate_id" {
				if row.HelloAt != nil || row.Result != "disconnected" || row.ReportedName != "" {
					t.Errorf("rejected duplicate was recorded as online: %+v", row)
				}
				return true
			}
		}
		return false
	}, "duplicate rejection missing from connection audit")
}

func expectHeartbeatDuplicate(t *testing.T, client *ssh.Client, id, name string) {
	t.Helper()
	ch, reqs, err := client.OpenChannel(proto.ChannelType, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	pc := proto.NewConn(ch)
	if err = pc.Send(proto.Hello{V: proto.Version, Type: proto.TypeHello, ID: id, Name: name, Role: proto.RoleImporter, ClientVersion: "duplicate-version"}); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(3*time.Second, func() { _ = client.Close() })
	defer timer.Stop()
	env, raw, err := pc.Recv()
	var rejection proto.Error
	if err != nil || env.Type != proto.TypeError || proto.Decode(raw, &rejection) != nil || rejection.Code != proto.CodeDuplicateID {
		t.Fatalf("expected duplicate ID rejection, got %s: %s, %v", env.Type, raw, err)
	}
}

func waitHeartbeatCondition(t *testing.T, ready func() bool, message string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatal(message)
		case <-tick.C:
		}
	}
}

type heartbeatDropWriter struct {
	conn net.Conn
	drop *atomic.Bool
}

func (w heartbeatDropWriter) Write(p []byte) (int, error) {
	if w.drop.Load() {
		return len(p), nil
	}
	return w.conn.Write(p)
}

// After activation this real TCP proxy drains and discards bytes in both
// directions without closing either socket or telling SSH the transport failed.
func startHeartbeatBlackhole(t *testing.T, target string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var drop atomic.Bool
	var mu sync.Mutex
	var conns []net.Conn
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		front, err := ln.Accept()
		if err != nil {
			return
		}
		back, err := net.DialTimeout("tcp", target, 3*time.Second)
		if err != nil {
			_ = front.Close()
			return
		}
		mu.Lock()
		conns = append(conns, front, back)
		mu.Unlock()
		workers.Add(2)
		copyDirection := func(dst, src net.Conn) {
			defer workers.Done()
			defer front.Close()
			defer back.Close()
			_, _ = io.Copy(heartbeatDropWriter{dst, &drop}, src)
		}
		go copyDirection(back, front)
		go copyDirection(front, back)
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return ln.Addr().String(), func() { drop.Store(true) }
}
