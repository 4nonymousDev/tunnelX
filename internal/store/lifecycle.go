package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrStoragePressure = errors.New("audit storage budget reached; new work paused")
var ErrCommitOutcomeUnknown = errors.New("transaction outcome is uncertain; admissions paused until database inspection and restart")

type Store struct {
	db                   *sql.DB
	path                 string
	backupPath           string
	opts                 Options
	rejected             [3]atomic.Int64
	stop                 chan struct{}
	done                 chan struct{}
	closeOnce            sync.Once
	closeErr             error
	maintenance          sync.Mutex
	lastMaintenanceError atomic.Value
	uncertainMu          sync.Mutex
	uncertainOps         map[string]struct{}
	commitUncertain      atomic.Bool
}

type Options struct {
	MaxDatabaseBytes    int64
	MaxWALBytes         int64
	AuditRetention      time.Duration
	AdminRetention      time.Duration
	MaxAuditRows        int
	MaintenanceInterval time.Duration
}

func DefaultOptions() Options {
	return Options{MaxDatabaseBytes: 512 << 20, MaxWALBytes: 64 << 20, AuditRetention: 30 * 24 * time.Hour, AdminRetention: 180 * 24 * time.Hour, MaxAuditRows: 200000, MaintenanceInterval: time.Minute}
}
func Open(path string) (*Store, error) { return OpenWithOptions(path, DefaultOptions()) }
func OpenWithOptions(path string, o Options) (*Store, error) {
	if o.MaxDatabaseBytes < 1<<20 || o.MaxWALBytes < 4096 || o.AuditRetention <= 0 || o.AdminRetention <= 0 || o.MaxAuditRows < 1 || o.MaintenanceInterval <= 0 {
		return nil, errors.New("invalid storage limits")
	}
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, opts: o, stop: make(chan struct{}), done: make(chan struct{}), uncertainOps: make(map[string]struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > schemaVersion {
		db.Close()
		return nil, fmt.Errorf("database schema version %d is newer than supported %d", version, schemaVersion)
	}
	if version > 0 && version < schemaVersion && s.fileBacked() {
		if err = s.backupBeforeMigration(ctx, version); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	var pageSize int64
	if err = db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", o.MaxDatabaseBytes/pageSize)); err != nil {
		db.Close()
		return nil, err
	}
	// Persisted pending intents survive crashes and require explicit reconciliation.
	if err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE admin_actions SET result='needs_reconcile',error='process stopped before operation completion was confirmed' WHERE id IN (SELECT audit_id FROM admin_operations WHERE state='pending')`); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE admin_operations SET state='needs_reconcile',updated_at=? WHERE state='pending'`, dbtime(time.Now()))
		return e
	}); err != nil {
		db.Close()
		return nil, err
	}
	s.lastMaintenanceError.Store("")
	go s.runMaintenance()
	return s, nil
}

func (s *Store) fileBacked() bool {
	return s.path != "" && s.path != ":memory:" && !strings.HasPrefix(s.path, "file:")
}
func (s *Store) backupBeforeMigration(ctx context.Context, version int) error {
	name := fmt.Sprintf("%s.backup-v%d-to-v%d-%s", s.path, version, schemaVersion, time.Now().UTC().Format("20060102T150405.000000000Z"))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create migration backup: %w", err)
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO ?", name); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("migration backup failed; original schema retained: %w", err)
	}
	// VACUUM INTO produces a consistent standalone snapshot, including WAL data.
	f, err = os.OpenFile(name, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Windows directory handles do not support File.Sync; on POSIX persist the
	// directory entry as well, so migration cannot outrun its recovery snapshot.
	if runtime.GOOS != "windows" {
		dir, e := os.Open(filepath.Dir(name))
		if e != nil {
			return e
		}
		e = dir.Sync()
		ce := dir.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	s.backupPath = name
	return nil
}
func (s *Store) BackupPath() string { return s.backupPath }

func (s *Store) CheckAdmission(ctx context.Context) error {
	if s.commitUncertain.Load() {
		return ErrCommitOutcomeUnknown
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.fileBacked() {
		return nil
	}
	// Page count includes pages currently only in WAL. Leave space for closing
	// records and administrative revocation before SQLite's hard page limit.
	var pageSize, pages, free int64
	for _, p := range []struct {
		name string
		dest *int64
	}{{"page_size", &pageSize}, {"page_count", &pages}, {"freelist_count", &free}} {
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(p.dest); err != nil {
			return err
		}
	}
	reserve := min(int64(4<<20), s.opts.MaxDatabaseBytes/16)
	if (pages-free)*pageSize >= s.opts.MaxDatabaseBytes-reserve {
		return ErrStoragePressure
	}
	for _, f := range []struct {
		path  string
		limit int64
	}{{s.path, s.opts.MaxDatabaseBytes}, {s.path + "-wal", s.opts.MaxWALBytes}} {
		info, err := os.Stat(f.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		limit := f.limit
		if f.path != s.path {
			limit -= min(int64(4<<20), limit/8)
		}
		if info.Size() >= limit {
			if f.path == s.path {
				var free int64
				if err = s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
					return err
				}
				if free >= 32 {
					continue
				}
			}
			return ErrStoragePressure
		}
	}
	return nil
}
func (s *Store) AdmissionError(ctx context.Context) error { return s.CheckAdmission(ctx) }
func (s *Store) CommitUncertain() bool                    { return s.commitUncertain.Load() }

// RecordRejection never touches disk, allocates no source/cardinality map, and
// retains no unverified fingerprint/IP. Counts are approximate across hard crashes.
func (s *Store) RecordRejection(reason string) {
	i := 2
	if reason == "rejected" {
		i = 0
	} else if reason == "blocked" {
		i = 1
	}
	saturatingAdd(&s.rejected[i], 1)
}
func saturatingAdd(v *atomic.Int64, n int64) {
	const max = int64(1 << 62)
	for {
		old := v.Load()
		next := old + n
		if next > max || next < old {
			next = max
		}
		if v.CompareAndSwap(old, next) {
			return
		}
	}
}
func (s *Store) PendingRejections() int64 {
	var n int64
	for i := range s.rejected {
		v := s.rejected[i].Load()
		if v > int64(1<<62)-n {
			return 1 << 62
		}
		n += v
	}
	return n
}
func (s *Store) flushRejections(ctx context.Context) error {
	if err := s.CheckAdmission(ctx); err != nil {
		return err
	}
	for i, reason := range []string{"rejected", "blocked", "other"} {
		n := s.rejected[i].Swap(0)
		if n == 0 {
			continue
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO rejection_counts(bucket_at,reason,count) VALUES(?,?,?) ON CONFLICT(bucket_at,reason) DO UPDATE SET count=CASE WHEN rejection_counts.count>=4611686018427387904-excluded.count THEN 4611686018427387904 ELSE rejection_counts.count+excluded.count END`, dbtime(time.Now().UTC().Truncate(5*time.Minute)), reason, n)
		if err != nil {
			saturatingAdd(&s.rejected[i], n)
			return err
		}
	}
	return nil
}

func (s *Store) runMaintenance() {
	defer close(s.done)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			s.maintenance.Lock()
			err := s.flushRejections(ctx)
			if time.Since(last) >= s.opts.MaintenanceInterval {
				if e := s.prune(ctx, time.Now()); e != nil {
					err = e
				}
				last = time.Now()
			}
			if e := s.checkpoint(ctx); e != nil {
				err = e
			}
			s.maintenance.Unlock()
			cancel()
			if err != nil {
				s.lastMaintenanceError.Store(err.Error())
			} else {
				s.lastMaintenanceError.Store("")
			}
		}
	}
}

func (s *Store) MaintenanceError() string {
	v := s.lastMaintenanceError.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}
func (s *Store) Maintain(ctx context.Context, now time.Time) error {
	s.maintenance.Lock()
	defer s.maintenance.Unlock()
	if err := s.prune(ctx, now); err != nil {
		return err
	}
	return s.checkpoint(ctx)
}
func (s *Store) checkpoint(ctx context.Context) error {
	mode := "PASSIVE"
	if s.fileBacked() {
		if info, err := os.Stat(s.path + "-wal"); err == nil && info.Size() >= s.opts.MaxWALBytes/2 {
			mode = "TRUNCATE"
		}
	}
	var busy, total, done int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &total, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("WAL checkpoint blocked by an active transaction")
	}
	return nil
}
func (s *Store) prune(ctx context.Context, now time.Time) error {
	// All table/column identifiers are constants. Each pass deletes at most 500 rows/table.
	for _, t := range []struct {
		table, col, complete string
		age                  time.Duration
	}{{"connection_audit", "authenticated_at", "disconnected_at IS NOT NULL OR result IN ('rejected','blocked')", s.opts.AuditRetention}, {"access_audit", "started_at", "ended_at IS NOT NULL", s.opts.AuditRetention}, {"admin_actions", "created_at", "id NOT IN (SELECT audit_id FROM admin_operations)", s.opts.AdminRetention}} {
		q := fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE (%s) AND (%s<? OR id IN (SELECT id FROM %s ORDER BY %s DESC,id DESC LIMIT -1 OFFSET ?)) ORDER BY %s,id LIMIT 500)", t.table, t.table, t.complete, t.col, t.table, t.col, t.col)
		if _, err := s.db.ExecContext(ctx, q, dbtime(now.Add(-t.age)), s.opts.MaxAuditRows); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rejection_counts WHERE bucket_at<?`, dbtime(now.Add(-7*24*time.Hour))); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM identity_claims WHERE last_seen_at<?`, dbtime(now.Add(-30*24*time.Hour))); err != nil {
		return err
	}
	// Resolved operations and their audit may expire; unresolved intentions never do.
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_operations WHERE id IN (SELECT id FROM admin_operations WHERE state IN ('applied','failed') AND updated_at<? ORDER BY updated_at LIMIT 500)`, dbtime(now.Add(-s.opts.AdminRetention)))
	return err
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.done
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		s.maintenance.Lock()
		_ = s.flushRejections(ctx)
		s.maintenance.Unlock()
		cancel()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// DataPath returns only a configured path; caller must not expose it to anonymous users.
func (s *Store) DataPath() string { return filepath.Clean(s.path) }
