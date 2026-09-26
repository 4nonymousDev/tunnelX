package accountauth

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordDerivationSaltBoundsAndExactInput(t *testing.T) {
	password := "  correct horse battery  "
	one, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if one == two || !strings.HasPrefix(one, prefix) || strings.Contains(one, password) {
		t.Fatal("password hash lacks salt or leaks input")
	}
	for _, test := range []struct {
		hash, password string
		want           bool
	}{
		{one, password, true}, {two, password, true}, {one, strings.TrimSpace(password), false},
		{one, "different password", false}, {"", password, false}, {"$argon2id$v=19$m=999999999,t=99,p=99$invalid$invalid", password, false},
		{one, strings.Repeat("a", 513), false}, {one, "short", false},
	} {
		got, err := Verify(test.hash, test.password)
		if err != nil || got != test.want {
			t.Fatalf("verify want %v got %v err %v", test.want, got, err)
		}
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 129), strings.Repeat("界", 129), strings.Repeat("a", 14) + "\xff"} {
		if _, err := Hash(bad); !errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("invalid password accepted: %v", err)
		}
	}
	if !ValidPassword(strings.Repeat("界", 128)) || !ValidPassword(strings.Repeat("a", 15)) {
		t.Fatal("valid boundary password rejected")
	}
}

func TestPasswordWorkLimitRejectsWithoutQueue(t *testing.T) {
	workers <- struct{}{}
	workers <- struct{}{}
	defer func() { <-workers; <-workers }()
	if _, err := Hash("long enough password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("hash: %v", err)
	}
	if _, err := Verify("", "long enough password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("verify: %v", err)
	}
}
