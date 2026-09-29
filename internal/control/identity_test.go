package control

import (
	"errors"
	"testing"
	"tunnelx/internal/proto"
)

func TestResolveNeverPinsUnverifiedOrAmbiguousIdentity(t *testing.T) {
	entry := proto.RegistryEntry{ID: "peer", Fingerprint: "SHA256:approved", TunnelID: "web", SessionID: "session", Generation: 1, SrcPort: 80, IdentityVerified: true}
	c := &Client{registry: []proto.RegistryEntry{entry}}
	target, err := c.ResolveTarget("peer", "", "web")
	if err != nil || target != entry.Target() {
		t.Fatalf("verified tunnel resolve: %+v %v", target, err)
	}
	assertCode := func(want string) {
		t.Helper()
		_, err := c.ResolveTarget("peer", "", "web")
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != want {
			t.Fatalf("want %s, got %v", want, err)
		}
	}
	c.registry[0].IdentityVerified = false
	assertCode(proto.CodeIdentityUnverified)
	c.registry[0] = entry
	if _, err := c.ResolveTarget("peer", "SHA256:other", "web"); err == nil {
		t.Fatal("wrong pin accepted")
	}
	second := entry
	second.SessionID = "duplicate-session"
	c.registry = append(c.registry, second)
	assertCode(proto.CodeAmbiguousTarget)
	c.registry = []proto.RegistryEntry{entry}
	if _, err := c.ResolveTarget("peer", "SHA256:approved", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveTarget("peer", "", ""); err == nil {
		t.Fatal("missing tunnel ID accepted")
	}
	c.registry = nil
	assertCode(proto.CodePeerOffline)
}
