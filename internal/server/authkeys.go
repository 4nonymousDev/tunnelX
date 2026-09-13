package server

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// authKeys is an atomically replaceable authorized_keys snapshot. Failed
// reads or parses never damage the last valid snapshot.
type authKeys struct {
	path         string
	logf         func(string, ...any)
	mu           sync.RWMutex
	keys         map[string]bool
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
		logf("警告：%s 中尚无授权公钥，任何客户端都无法连接", path)
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
		key, _, _, rest, err := ssh.ParseAuthorizedKey(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("authorized_keys 第 %d 行无效: %w", line, err)
		}
		if len(bytes.TrimSpace(rest)) != 0 {
			return nil, nil, fmt.Errorf("authorized_keys 第 %d 行包含多余内容", line)
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
	info, err := os.Stat(a.path)
	if err != nil {
		return fmt.Errorf("读取 authorized_keys %s: %w", a.path, err)
	}
	data, err := os.ReadFile(a.path)
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
	a.modTime = info.ModTime()
	a.size = info.Size()
	a.hash = h
	a.checkedAt = time.Now()
	a.mu.Unlock()
	return nil
}

func (a *authKeys) Authorized(key ssh.PublicKey) bool {
	a.maybeReload()
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.keys[string(key.Marshal())]
}
func (a *authKeys) AuthorizedFingerprint(fp string) bool {
	a.maybeReload()
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fingerprints[fp]
}
func (a *authKeys) Count() int { a.mu.RLock(); defer a.mu.RUnlock(); return len(a.keys) }

func (a *authKeys) Import(publicKey, comment string) (string, error) {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	raw := []byte(strings.TrimSpace(publicKey))
	key, _, _, rest, err := ssh.ParseAuthorizedKey(raw)
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		if err == nil {
			err = errors.New("multiple public keys are not allowed")
		}
		return "", fmt.Errorf("invalid public key: %w", err)
	}
	fp := ssh.FingerprintSHA256(key)
	data, err := os.ReadFile(a.path)
	if err != nil {
		return "", err
	}
	_, fps, err := parseAuthorizedKeys(data)
	if err != nil {
		return "", err
	}
	if fps[fp] {
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
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(prefix + line + "\n")
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
	if err := a.load(); err != nil {
		return "", err
	}
	return fp, nil
}

func (a *authKeys) maybeReload() {
	a.mu.RLock()
	last := a.checkedAt
	oldHash := a.hash
	a.mu.RUnlock()
	if time.Since(last) < reloadInterval {
		return
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		a.markChecked()
		a.logf("检查 authorized_keys 失败，沿用已加载的公钥: %v", err)
		return
	}
	h := sha256.Sum256(data)
	if h == oldHash {
		a.markChecked()
		return
	}
	if err = a.load(); err != nil {
		a.markChecked()
		a.logf("重载 authorized_keys 失败，沿用已加载的公钥: %v", err)
		return
	}
	a.logf("authorized_keys 已重载，当前 %d 个授权公钥", a.Count())
}
func (a *authKeys) markChecked() { a.mu.Lock(); a.checkedAt = time.Now(); a.mu.Unlock() }
