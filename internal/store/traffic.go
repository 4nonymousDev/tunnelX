package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type TrafficTotal struct {
	Role, ClientID, Name string
	Bytes                int64
}

// Bootstrap once from retained, completed audits. The marker and both roles
// commit together; later starts never re-import audits or double-count bytes.
func (s *Store) LoadTraffic(ctx context.Context) (time.Time, time.Time, []TrafficTotal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	defer tx.Rollback()
	var since, saved int64
	err = tx.QueryRowContext(ctx, `SELECT since_at,persisted_at FROM traffic_state WHERE id=1`).Scan(&since, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		saved = dbtime(time.Now())
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(started_at),?) FROM access_audit WHERE ended_at IS NOT NULL AND bytes_sent+bytes_received>0`, saved).Scan(&since); err != nil {
			return time.Time{}, time.Time{}, nil, err
		}
		for _, query := range []string{
			`INSERT INTO traffic_totals(role,client_id,name,bytes)
			 SELECT 'exporter',COALESCE(NULLIF(a.target_client_id,''),a.target_fingerprint),COALESCE(NULLIF(MAX(c.last_reported_name),''),COALESCE(NULLIF(a.target_client_id,''),a.target_fingerprint)),SUM(a.bytes_sent+a.bytes_received)
			 FROM access_audit a LEFT JOIN clients c ON c.fingerprint=a.target_fingerprint
			 WHERE a.ended_at IS NOT NULL AND a.bytes_sent+a.bytes_received>0 AND COALESCE(NULLIF(a.target_client_id,''),a.target_fingerprint)<>''
			 GROUP BY COALESCE(NULLIF(a.target_client_id,''),a.target_fingerprint)`,
			`INSERT INTO traffic_totals(role,client_id,name,bytes)
			 SELECT 'importer',COALESCE(NULLIF(a.visitor_client_id,''),a.visitor_fingerprint),COALESCE(NULLIF(MAX(c.last_reported_name),''),COALESCE(NULLIF(a.visitor_client_id,''),a.visitor_fingerprint)),SUM(a.bytes_sent+a.bytes_received)
			 FROM access_audit a LEFT JOIN clients c ON c.fingerprint=a.visitor_fingerprint
			 WHERE a.ended_at IS NOT NULL AND a.bytes_sent+a.bytes_received>0 AND COALESCE(NULLIF(a.visitor_client_id,''),a.visitor_fingerprint)<>''
			 GROUP BY COALESCE(NULLIF(a.visitor_client_id,''),a.visitor_fingerprint)`,
		} {
			if _, err = tx.ExecContext(ctx, query); err != nil {
				return time.Time{}, time.Time{}, nil, err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO traffic_state(id,since_at,persisted_at) VALUES(1,?,?)`, since, saved); err != nil {
			return time.Time{}, time.Time{}, nil, err
		}
	} else if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	if err = bootstrapTrafficHistory(ctx, tx); err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT role,client_id,name,bytes FROM traffic_totals ORDER BY role,client_id`)
	if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	var totals []TrafficTotal
	for rows.Next() {
		var v TrafficTotal
		if err = rows.Scan(&v.Role, &v.ClientID, &v.Name, &v.Bytes); err != nil {
			rows.Close()
			return time.Time{}, time.Time{}, nil, err
		}
		totals = append(totals, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	return scantime(since), scantime(saved), totals, nil
}

// Absolute totals make retries safe even when the previous commit outcome was
// uncertain. No database I/O occurs on a forwarding goroutine.
func (s *Store) SaveTraffic(ctx context.Context, totals []TrafficTotal, buckets []TrafficBucket, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO traffic_totals(role,client_id,name,bytes) VALUES(?,?,?,?) ON CONFLICT(role,client_id) DO UPDATE SET name=excluded.name,bytes=MAX(traffic_totals.bytes,excluded.bytes)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range totals {
		if _, err = stmt.ExecContext(ctx, v.Role, v.ClientID, v.Name, v.Bytes); err != nil {
			return err
		}
	}
	if err = saveTrafficBuckets(ctx, tx, buckets); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE traffic_state SET persisted_at=MAX(persisted_at,?) WHERE id=1`, dbtime(at)); err != nil {
		return err
	}
	return tx.Commit()
}
