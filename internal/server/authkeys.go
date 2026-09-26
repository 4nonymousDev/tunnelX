package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/keyperm"
)

// authKeys is an atomically replaceable authorized_keys snapshot. Failed
// reads or parses never damage the last valid snapshot.
type authKeys struct {
	path         string
	logf         func(string, ...any)
	mu           sync.RWMutex
	keys         map[string]bool
	healthy      bool
	fingerprints map[string]bool
	modTime      time.Time
	size         int64
	hash         [sha256.Size]byte
	checkedAt    time.Time
	importMu     sync.Mutex
}

var errAuthorizedKeyExists = errors.New("public key is already authorized")

const reloadInterval = 2 * time.Second

func newAuthKeys(path string, logf func(string, ...any)) (*authKeys, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &authKeys{path: path, logf: logf, keys: map[string]bool{}, fingerprints: map[string]bool{}}
	if err := a.load(); err != nil {
		return nil, err
	}
	if a.Count() == 0 {
		logf("%s 中尚无手工授权公钥；账号设备通过管理后台创建的账号接入", path)
	}
	return a, nil
}

func parseAuthorizedKeys(data []byte) (map[string]bool, map[string]bool, error) {
	keys := map[string]bool{}
	fps := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("authorized_keys 第 %d 行无效: %w", line, err)
		}
		if len(bytes.TrimSpace(rest)) != 0 {
			return nil, nil, fmt.Errorf("authorized_keys 第 %d 行包含多余内容", line)
		}
		if len(options) > 0 {
			return nil, nil, fmt.Errorf("authorized_keys 第 %d 行包含尚不支持的授权限制，拒绝忽略", line)
		}
		keys[string(key.Marshal())] = true
		fps[ssh.FingerprintSHA256(key)] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("扫描 authorized_keys: %w", err)
	}
	return keys, fps, nil
}

func (a *authKeys) load() error {
	data, err := readAuthorizedFile(a.path)
	if err != nil {
		return fmt.Errorf("读取 authorized_keys %s: %w", a.path, err)
	}
	keys, fps, err := parseAuthorizedKeys(data)
	if err != nil {
		return err
	}
	h := sha256.Sum256(data)
	a.mu.Lock()
	a.keys = keys
	a.fingerprints = fps
	a.healthy = true
	a.size = int64(len(data))
	a.hash = h
	a.checkedAt = time.Now()
	a.mu.Unlock()
	return nil
}

func (a *authKeys) Authorized(key ssh.PublicKey) bool {
	a.maybeReload()
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.healthy && a.keys[string(key.Marshal())]
}
func (a *authKeys) AuthorizedFingerprint(fp string) bool {
	allowed, _ := a.FingerprintStatus(fp)
	return allowed
}
func (a *authKeys) FingerprintStatus(fp string) (bool, bool) {
	a.maybeReload()
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.healthy && a.fingerprints[fp], a.healthy
}
func (a *authKeys) Count() int { a.mu.RLock(); defer a.mu.RUnlock(); return len(a.keys) }

func (a *authKeys) Import(publicKey, comment string) (string, error) {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	raw := []byte(strings.TrimSpace(publicKey))
	key, _, options, rest, err := ssh.ParseAuthorizedKey(raw)
	if err != nil || len(options) > 0 || len(bytes.TrimSpace(rest)) != 0 {
		if err == nil {
			err = errors.New("key options and multiple public keys are not supported")
		}
		return "", fmt.Errorf("invalid public key: %w", err)
	}
	fp := ssh.FingerprintSHA256(key)
	if strings.ContainsAny(comment, "\r\n") {
		return "", errors.New("key comment must be a single line")
	}
	data, err := readAuthorizedFile(a.path)
	if err != nil {
		return "", err
	}
	_, fps, err := parseAuthorizedKeys(data)
	if err != nil {
		return "", err
	}
	if fps[fp] {
		// Re-registering a known key must refresh a repaired authorization file
		// too, without appending or rewriting the existing key/comment.
		if err := a.load(); err != nil {
			a.markUnhealthy()
			return "", err
		}
		return "", errAuthorizedKeyExists
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if comment = strings.TrimSpace(comment); comment != "" {
		line += " " + comment
	}
	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	next := append(data, []byte(prefix+line+"\n")...)
	if len(next) > maxAuthorizedBytes {
		return "", errors.New("authorized_keys exceeds size limit")
	}
	tmp := filepath.Join(filepath.Dir(a.path), ".authorized_keys-"+rand.Text()+".tmp")
	f, err := keyperm.Create(tmp)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	_, writeErr := f.Write(next)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Rename(tmp, a.path); err != nil {
		return "", err
	}
	if err = syncAuthDirectory(filepath.Dir(a.path)); err != nil {
		a.markUnhealthy()
		return "", err
	}
	if err := a.load(); err != nil {
		return "", err
	}
	return fp, nil
}

func (a *authKeys) maybeReload() {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	a.mu.RLock()
	last := a.checkedAt
	a.mu.RUnlock()
	if time.Since(last) < reloadInterval {
		return
	}
	if err := a.load(); err != nil {
		a.markUnhealthy()
		a.logf("重载 authorized_keys 失败，已暂停新的授权准入: %v", err)
		return
	}
}
func (a *authKeys) markUnhealthy() {
	a.mu.Lock()
	a.healthy = false
	a.checkedAt = time.Now()
	a.mu.Unlock()
}

const maxAuthorizedBytes = 1 << 20

// CheckAuthorizedKeys validates an upgrade's input without opening/migrating a
// database, starting listeners, or modifying authorization data.
func CheckAuthorizedKeys(path string) error {
	data, err := readAuthorizedFile(path)
	if err != nil {
		return err
	}
	_, _, err = parseAuthorizedKeys(data)
	return err
}

func readAuthorizedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxAuthorizedBytes {
		return nil, errors.New("authorized_keys must be a regular file no larger than 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAuthorizedBytes+1))
	if len(data) > maxAuthorizedBytes {
		return nil, errors.New("authorized_keys exceeds size limit")
	}
	return data, err
}
