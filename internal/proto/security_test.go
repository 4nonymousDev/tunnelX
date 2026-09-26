package proto

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenRequiresCompleteV2Target(t *testing.T) {
	target := Target{ClientID: "client", Fingerprint: "SHA256:key", TunnelID: "web", SessionID: "session", Generation: 1}
	raw, err := EncodeOpen(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOpen(raw)
	if err != nil || got != target {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, bad := range [][]byte{
		[]byte(`{"v":1,"target":{"client_id":"client"}}`),
		[]byte(`{"v":2,"target":{}}`),
		append(append([]byte{}, raw...), raw...),
		[]byte(strings.Replace(string(raw), `"v":2`, `"v":2,"unexpected":true`, 1)),
		[]byte(strings.Repeat(" ", MaxOpenBytes+1)),
	} {
		if _, err := DecodeOpen(bad); err == nil {
			t.Fatalf("accepted invalid target: %.100s", bad)
		}
	}
}

func TestSendSizeBoundaryIsAtomic(t *testing.T) {
	var wire bytes.Buffer
	c := NewConn(&wire)
	if err := c.Send(strings.Repeat("x", MaxFrameBytes)); err == nil {
		t.Fatal("oversized send accepted")
	}
	if wire.Len() != 0 {
		t.Fatal("oversized message leaked a partial frame")
	}
	// Include the JSON envelope and line ending in the exact byte budget.
	base := map[string]string{"type": "test", "data": ""}
	raw, _ := json.Marshal(base)
	base["data"] = strings.Repeat("x", MaxFrameBytes-len(raw)-1)
	if err := c.Send(base); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != MaxFrameBytes {
		t.Fatalf("frame = %d", wire.Len())
	}
	if _, _, err := NewConn(&wire).Recv(); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	base["data"] += "x"
	if err := c.Send(base); err == nil || wire.Len() != 0 {
		t.Fatal("limit+1 was not atomically rejected")
	}
}
