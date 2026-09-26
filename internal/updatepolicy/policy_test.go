package updatepolicy

import (
	"strings"
	"testing"
	"time"
)

func TestVersionOrderingAndValidation(t *testing.T) {
	for _, tc := range []struct {
		client, minimum string
		want            bool
	}{
		{"0.2.0", "0.2.1", false}, {"0.2.1", "0.2.1", true}, {"0.10.0", "0.2.1", true},
		{"1.0.0", "0.99.99", true}, {"v0.2.1+build.5", "0.2.1", true},
		{"0.2.1-rc.1", "0.2.1", false}, {"0.3.0-rc.1", "0.2.1", true},
		{"dev", "0.2.1", false}, {"", "0.2.1", false}, {"0.02.1", "0.2.1", false},
		{"0.2.1-01", "0.2.1", false}, {"18446744073709551616.0.0", "0.2.1", false},
	} {
		if got := AtLeast(tc.client, tc.minimum); got != tc.want {
			t.Errorf("AtLeast(%q,%q)=%v", tc.client, tc.minimum, got)
		}
	}
	for _, value := range []string{"dev", "0.2", "v0.2.1", "0.2.1-rc.1", "0.2.1+build", "0.02.1", "1.0.0\n"} {
		if ValidMinimum(value) {
			t.Errorf("invalid minimum accepted: %q", value)
		}
	}
}

func TestFixedGraceDeadline(t *testing.T) {
	deadline := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	p := Policy{MinimumVersion: "0.2.1", EnforceAfter: &deadline, Generation: 2}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.2.0", "dev", ""} {
		before := p.Status(version, deadline.Add(-time.Nanosecond))
		at := p.Status(version, deadline)
		after := p.Status(version, deadline.Add(DefaultGracePeriod))
		if !before.Required || before.Blocked || !at.Blocked || !after.Blocked {
			t.Fatalf("deadline boundary for %q: %+v %+v %+v", version, before, at, after)
		}
	}
	if s := p.Status("0.2.1", deadline); s.Required || s.Blocked {
		t.Fatal("supported client blocked")
	}
	if (Policy{}).Status("dev", deadline) != nil {
		t.Fatal("disabled policy affected clients")
	}
	p.Message = strings.Repeat("字", 501)
	if p.Validate() == nil {
		t.Fatal("oversized message accepted")
	}
	p.Message = ""
	p.EnforceAfter = nil
	if p.Validate() == nil {
		t.Fatal("deadline missing")
	}
	if (Policy{Message: "not cleared"}).Validate() == nil {
		t.Fatal("ambiguous disabled policy accepted")
	}
}
