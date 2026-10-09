package store

import (
	"context"
	"database/sql"
	"time"
)

// Quarter-hour UTC buckets support local-day and local-hour boundaries in
// modern time zones, including half-hour and quarter-hour offsets.
const TrafficBucketDuration = 15 * time.Minute

type TrafficBucket struct {
	Role, ClientID, RunID string
	At                    time.Time
	Bytes                 int64
}

func bootstrapTrafficHistory(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic_history_state`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	// Old audits contain only final byte counts, so attribute them to the
	// completion bucket. New traffic is attributed as it is sampled.
	for _, query := range []string{
		`INSERT INTO traffic_buckets(bucket_at,role,client_id,run_id,bytes) SELECT (ended_at/900000000)*900000000,'exporter',COALESCE(NULLIF(target_client_id,''),target_fingerprint),'audit-bootstrap',SUM(bytes_sent+bytes_received) FROM access_audit WHERE ended_at IS NOT NULL AND bytes_sent+bytes_received>0 AND COALESCE(NULLIF(target_client_id,''),target_fingerprint)<>'' GROUP BY (ended_at/900000000),COALESCE(NULLIF(target_client_id,''),target_fingerprint)`,
		`INSERT INTO traffic_buckets(bucket_at,role,client_id,run_id,bytes) SELECT (ended_at/900000000)*900000000,'importer',COALESCE(NULLIF(visitor_client_id,''),visitor_fingerprint),'audit-bootstrap',SUM(bytes_sent+bytes_received) FROM access_audit WHERE ended_at IS NOT NULL AND bytes_sent+bytes_received>0 AND COALESCE(NULLIF(visitor_client_id,''),visitor_fingerprint)<>'' GROUP BY (ended_at/900000000),COALESCE(NULLIF(visitor_client_id,''),visitor_fingerprint)`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO traffic_history_state(id,initialized_at) VALUES(1,?)`, dbtime(time.Now()))
	return err
}

func saveTrafficBuckets(ctx context.Context, tx *sql.Tx, buckets []TrafficBucket) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO traffic_buckets(bucket_at,role,client_id,run_id,bytes) VALUES(?,?,?,?,?) ON CONFLICT(bucket_at,role,client_id,run_id) DO UPDATE SET bytes=MAX(traffic_buckets.bytes,excluded.bytes)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range buckets {
		if _, err = stmt.ExecContext(ctx, dbtime(v.At), v.Role, v.ClientID, v.RunID, v.Bytes); err != nil {
			return err
		}
	}
	return nil
}

// Query saved totals and their watermark from one consistent database snapshot.
func (s *Store) TrafficHistory(ctx context.Context, from, to time.Time) ([]TrafficTotal, time.Time, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, time.Time{}, err
	}
	defer tx.Rollback()
	var saved int64
	if err = tx.QueryRowContext(ctx, `SELECT persisted_at FROM traffic_state WHERE id=1`).Scan(&saved); err != nil {
		return nil, time.Time{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.role,b.client_id,COALESCE(t.name,b.client_id),SUM(b.bytes) FROM traffic_buckets b LEFT JOIN traffic_totals t ON t.role=b.role AND t.client_id=b.client_id WHERE b.bucket_at>=? AND b.bucket_at<? GROUP BY b.role,b.client_id ORDER BY SUM(b.bytes) DESC,b.client_id`, dbtime(from), dbtime(to))
	if err != nil {
		return nil, time.Time{}, err
	}
	var result []TrafficTotal
	for rows.Next() {
		var v TrafficTotal
		if err = rows.Scan(&v.Role, &v.ClientID, &v.Name, &v.Bytes); err != nil {
			rows.Close()
			return nil, time.Time{}, err
		}
		result = append(result, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return nil, time.Time{}, err
	}
	return result, scantime(saved), nil
}
