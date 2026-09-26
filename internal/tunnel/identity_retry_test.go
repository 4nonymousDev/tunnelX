package tunnel

import (
	"testing"
	"time"
	"tunnelx/internal/proto"
)

func TestPendingIdentityRetriesSlowlyWithoutChangingTrust(t *testing.T) {
	pending := &proto.Error{Code: proto.CodeIdentityUnverified}
	if !Classify(pending).Retryable {
		t.Fatal("pending administrator approval cannot recover")
	}
	b := NewBackoff()
	for range 20 {
		wait := b.NextFor(pending)
		if wait < 16*time.Second || wait > 24*time.Second {
			t.Fatalf("pending retry interval: %s", wait)
		}
	}
	if wait := b.Next(); wait > 6*time.Second {
		t.Fatalf("approval delay poisoned network backoff: %s", wait)
	}
	if Classify(&proto.Error{Code: proto.CodeIdentityMismatch}).Retryable {
		t.Fatal("fingerprint mismatch must require intervention")
	}
}
