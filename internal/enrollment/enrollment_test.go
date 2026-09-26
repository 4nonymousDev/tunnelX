package enrollment

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestProofBindsSessionAccountDeviceAndKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	payload := SigningPayload([]byte("session-one"), "alice", "device-one", signer.PublicKey())
	sig, _ := signer.Sign(rand.Reader, payload)
	if err := signer.PublicKey().Verify(payload, sig); err != nil {
		t.Fatal(err)
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewSignerFromKey(otherPriv)
	for _, altered := range [][]byte{
		SigningPayload([]byte("session-two"), "alice", "device-one", signer.PublicKey()),
		SigningPayload([]byte("session-one"), "other", "device-one", signer.PublicKey()),
		SigningPayload([]byte("session-one"), "alice", "device-two", signer.PublicKey()),
		SigningPayload([]byte("session-one"), "alice", "device-one", other.PublicKey()),
	} {
		if err := signer.PublicKey().Verify(altered, sig); err == nil {
			t.Fatal("altered enrollment proof accepted")
		}
	}
}

func TestFramesRejectOversizeUnknownAndTrailingData(t *testing.T) {
	for _, frame := range []string{
		`{"ok":true,"password":"must-not-be-echoed"}` + "\n",
		`{"ok":true} {"ok":false}` + "\n",
		`{"ok":true}`,
		strings.Repeat(" ", MaxFrameBytes) + "\n",
	} {
		var response Response
		if err := Read(strings.NewReader(frame), &response); err == nil || strings.Contains(err.Error(), "must-not-be-echoed") {
			t.Fatalf("unsafe frame result: %v", err)
		}
	}
	var encoded bytes.Buffer
	if err := Write(&encoded, Response{OK: true}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := Read(&encoded, &response); err != nil || !response.OK {
		t.Fatalf("round trip: %+v %v", response, err)
	}
	if err := Write(&encoded, Response{Error: strings.Repeat("x", MaxFrameBytes)}); err == nil {
		t.Fatal("oversize write accepted")
	}
}

func TestCredentialBounds(t *testing.T) {
	for _, username := range []string{"_bad", ".bad", "-bad", "ab", "a b", "中文账号", strings.Repeat("a", 65)} {
		if ValidUsername(username) {
			t.Fatalf("accepted username %q", username)
		}
	}
	if !ValidUsername(NormalizeUsername(" Alice-1 ")) || !ValidLoginPassword(strings.Repeat("密", 128)) {
		t.Fatal("valid credentials rejected")
	}
	for _, password := range []string{"", strings.Repeat("a", 129), string([]byte{0xff})} {
		if ValidLoginPassword(password) {
			t.Fatal("invalid password accepted")
		}
	}
}
