package control

import (
	"errors"
	"testing"
	"tunnelx/internal/proto"
)

func TestResolveNeverPinsUnverifiedOrAmbiguousIdentity(t *testing.T) {
	entry := proto.RegistryEntry{ID: "peer", Fingerprint: "SHA256:approved", TunnelID: "web", SessionID: "session", Generation: 1, SrcPort: 80, IdentityVerified: true}
	c := &Client{registry: []proto.RegistryEntry{entry}}
	target, err := c.ResolveTarget("peer", "", "", 80)
	if err != nil || target != entry.Target() {
		t.Fatalf("verified unique legacy resolve: %+v %v", target, err)
	}
	assertCode := func(want string) {
		t.Helper()
		_, err := c.ResolveTarget("peer", "", "", 80)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != want {
			t.Fatalf("want %s, got %v", want, err)
		}
	}
	c.registry[0].IdentityVerified = false
	assertCode(proto.CodeIdentityUnverified)
	c.registry[0] = entry
	if _, err := c.ResolveTarget("peer", "SHA256:other", "web", 80); err == nil {
		t.Fatal("wrong pin accepted")
	}
	second := entry
	second.TunnelID = "second"
	c.registry = append(c.registry, second)
	assertCode(proto.CodeAmbiguousTarget)
	if _, err := c.ResolveTarget("peer", "SHA256:approved", "web", 80); err != nil {
		t.Fatal(err)
	}
	c.registry = nil
	assertCode(proto.CodePeerOffline)
}
