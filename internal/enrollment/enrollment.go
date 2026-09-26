// Package enrollment defines the one-shot, password-authenticated device enrollment channel.
package enrollment

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

const ChannelType = "tunnelx-enroll-v1"
const MaxFrameBytes = 16 * 1024

type Request struct {
	ClientID  string `json:"client_id"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
	Signature []byte `json:"signature"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// SigningPayload binds proof of key possession to this SSH session, account and device.
func SigningPayload(sessionID []byte, username, clientID string, publicKey ssh.PublicKey) []byte {
	return ssh.Marshal(struct {
		Domain    string
		SessionID []byte
		Username  string
		ClientID  string
		PublicKey []byte
	}{"tunnelx/enroll/v1", sessionID, username, clientID, publicKey.Marshal()})
}

func NormalizeUsername(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

func ValidUsername(username string) bool {
	if len(username) < 3 || len(username) > 64 {
		return false
	}
	if c := username[0]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
		return false
	}
	for _, c := range username {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

func ValidLoginPassword(password string) bool {
	return password != "" && len(password) <= 512 && utf8.ValidString(password) && utf8.RuneCountInString(password) <= 128
}

// Read accepts one bounded JSON line, without echoing untrusted fields in errors.
func Read(r io.Reader, dst any) error {
	line, err := bufio.NewReaderSize(r, MaxFrameBytes).ReadSlice('\n')
	if err != nil || len(line) > MaxFrameBytes {
		return errors.New("invalid enrollment frame")
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid enrollment frame")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid enrollment frame")
	}
	return nil
}

func Write(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > MaxFrameBytes {
		return errors.New("invalid enrollment frame")
	}
	data = append(data, '\n')
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
