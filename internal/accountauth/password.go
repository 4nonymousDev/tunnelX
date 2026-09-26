// Package accountauth bounds the CPU and memory used for account passwords.
package accountauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var ErrBusy = errors.New("password verification capacity reached")
var ErrInvalidPassword = errors.New("password must contain 15 to 128 Unicode characters and at most 512 bytes")

const prefix = "$argon2id$v=19$m=19456,t=2,p=1$"

// A global, nonblocking limit also covers administrator password changes.
// Each worker uses 19 MiB; callers must apply connection/rate limits separately.
var workers = make(chan struct{}, 2)

func ValidPassword(password string) bool {
	return len(password) <= 512 && utf8.ValidString(password) && utf8.RuneCountInString(password) >= 15 && utf8.RuneCountInString(password) <= 128
}

func Hash(password string) (string, error) {
	if !ValidPassword(password) {
		return "", ErrInvalidPassword
	}
	select {
	case workers <- struct{}{}:
		defer func() { <-workers }()
	default:
		return "", ErrBusy
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt[:], 2, 19*1024, 1, 32)
	defer clear(key)
	return prefix + base64.RawStdEncoding.EncodeToString(salt[:]) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

// Verify does the same fixed-cost derivation for missing/corrupt records and
// malformed passwords. A missing record can never authenticate, even if its
// dummy value happens to match. Stored parameters cannot inflate resource use.
func Verify(encoded, password string) (bool, error) {
	select {
	case workers <- struct{}{}:
		defer func() { <-workers }()
	default:
		return false, ErrBusy
	}
	var salt [16]byte
	var expected [32]byte
	valid := false
	if strings.HasPrefix(encoded, prefix) && len(encoded) <= 128 {
		parts := strings.Split(encoded[len(prefix):], "$")
		if len(parts) == 2 {
			s, es := base64.RawStdEncoding.Strict().DecodeString(parts[0])
			k, ek := base64.RawStdEncoding.Strict().DecodeString(parts[1])
			if es == nil && ek == nil && len(s) == len(salt) && len(k) == len(expected) {
				copy(salt[:], s)
				copy(expected[:], k)
				valid = true
			}
			clear(k)
		}
	}
	if !ValidPassword(password) {
		valid = false
		password = "invalid-password-input"
	}
	key := argon2.IDKey([]byte(password), salt[:], 2, 19*1024, 1, 32)
	defer clear(key)
	match := subtle.ConstantTimeCompare(key, expected[:]) == 1
	return valid && match, nil
}
