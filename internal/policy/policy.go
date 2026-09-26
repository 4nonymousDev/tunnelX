// Package policy provides the fast in-memory admission decision used by SSH
// authentication and a gate which serializes that decision with block changes.
package policy

import (
	"context"
	"errors"
	"sync"
	"time"

	"tunnelx/internal/store"
)

type Block struct {
	Fingerprint, Reason, Operator string
	CreatedAt                     time.Time
	ExpiresAt                     *time.Time
}
type Loader interface {
	ActiveBlacklist(context.Context, time.Time) ([]store.BlacklistEntry, error)
}

type Policy struct {
	gate      sync.RWMutex
	change    sync.Mutex
	pending   map[string]bool
	version   uint64
	uncertain bool
	blocks    map[string]Block
	now       func() time.Time
}

func New(blocks []Block) *Policy {
	p := &Policy{blocks: map[string]Block{}, pending: map[string]bool{}, now: time.Now}
	p.replace(blocks)
	return p
}

// Admit holds a shared gate for the complete final admission/register sequence.
// Block operations use Change, so no connection can slip between the check and
// a disconnect scan.
func (p *Policy) Admit(fingerprint string, register func() error) (bool, error) {
	p.gate.RLock()
	defer p.gate.RUnlock()
	if p.blockedLocked(fingerprint, p.now().UTC()) {
		return false, nil
	}
	if register != nil {
		return true, register()
	}
	return true, nil
}
func (p *Policy) Blocked(fingerprint string) bool {
	p.gate.RLock()
	defer p.gate.RUnlock()
	return p.blockedLocked(fingerprint, p.now().UTC())
}

// Change gates only short in-memory changes. Callbacks MUST NOT do database/network I/O.
func (p *Policy) Change(fn func(*Snapshot) error) error {
	p.gate.Lock()
	defer p.gate.Unlock()
	p.version++
	defer func() { p.version++ }()
	s := &Snapshot{p: p}
	return fn(s)
}

// PersistChange denies the affected fingerprint while persistence runs outside
// the admission gate. Unrelated identities remain available during slow I/O.
// Management changes serialize, so a failed write cannot overwrite a newer state.
func (p *Policy) PersistChange(fp string, persist func() error, commit func(*Snapshot)) error {
	return p.PersistChanges([]string{fp}, persist, commit)
}

// PersistChanges also covers key rotation: both the previous and replacement
// fingerprints are denied until the durable identity change has completed.
func (p *Policy) PersistChanges(fingerprints []string, persist func() error, commit func(*Snapshot)) error {
	p.change.Lock()
	defer p.change.Unlock()
	p.gate.Lock()
	p.version++
	for _, fp := range fingerprints {
		if fp != "" {
			p.pending[fp] = true
		}
	}
	p.gate.Unlock()
	err := persist()
	p.gate.Lock()
	defer p.gate.Unlock()
	p.version++
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		p.uncertain = true
	}
	if err == nil && commit != nil {
		commit(&Snapshot{p: p})
	}
	for _, fp := range fingerprints {
		delete(p.pending, fp)
	}
	return err
}

func (p *Policy) Version() uint64 { p.gate.RLock(); defer p.gate.RUnlock(); return p.version }

// AdmitVersion joins an earlier lock-free database validation with the final
// in-memory admission. The callback may manipulate memory only, never perform I/O.
func (p *Policy) AdmitVersion(fp string, expected uint64, register func() error) (bool, error) {
	p.gate.RLock()
	defer p.gate.RUnlock()
	if p.version != expected || p.blockedLocked(fp, p.now().UTC()) {
		return false, nil
	}
	if register != nil {
		return true, register()
	}
	return true, nil
}

type Snapshot struct{ p *Policy }

func (s *Snapshot) Set(b Block)            { s.p.blocks[b.Fingerprint] = normalize(b) }
func (s *Snapshot) Delete(fp string)       { delete(s.p.blocks, fp) }
func (s *Snapshot) Blocked(fp string) bool { return s.p.blockedLocked(fp, s.p.now().UTC()) }

func (p *Policy) Reload(ctx context.Context, l Loader) error {
	p.change.Lock()
	defer p.change.Unlock()
	now := p.now().UTC()
	entries, err := l.ActiveBlacklist(ctx, now)
	if err != nil {
		return err
	}
	bs := make([]Block, 0, len(entries))
	for _, b := range entries {
		bs = append(bs, Block{Fingerprint: b.Fingerprint, Reason: b.Reason, Operator: b.Operator, CreatedAt: b.CreatedAt, ExpiresAt: b.ExpiresAt})
	}
	p.gate.Lock()
	p.version++
	p.replace(bs)
	p.gate.Unlock()
	return nil
}
func (p *Policy) replace(bs []Block) {
	n := p.now().UTC()
	next := make(map[string]Block, len(bs))
	for _, b := range bs {
		b = normalize(b)
		if b.ExpiresAt == nil || b.ExpiresAt.After(n) {
			next[b.Fingerprint] = b
		}
	}
	p.blocks = next
}
func (p *Policy) blockedLocked(fp string, now time.Time) bool {
	if p.uncertain {
		return true
	}
	if p.pending[fp] {
		return true
	}
	b, ok := p.blocks[fp]
	if !ok {
		return false
	}
	return b.ExpiresAt == nil || b.ExpiresAt.After(now)
}
func normalize(b Block) Block {
	b.CreatedAt = b.CreatedAt.UTC()
	if b.ExpiresAt != nil {
		x := b.ExpiresAt.UTC()
		b.ExpiresAt = &x
	}
	return b
}
