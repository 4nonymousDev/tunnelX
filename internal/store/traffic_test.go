package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficHistoryBootstrapRetryAndTimeRange(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().UTC().Truncate(TrafficBucketDuration).Add(-time.Hour)
	id, err := s.StartAccess(ctx, AccessAudit{VisitorClientID: "i", TargetClientID: "e", StartedAt: at.Add(-time.Hour), Result: "started"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAccess(ctx, id, "completed", "", 70, 30, at); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.LoadTraffic(ctx); err != nil {
		t.Fatal(err)
	}
	assertBytes := func(from, to time.Time, want int64) {
		t.Helper()
		values, _, err := s.TrafficHistory(ctx, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if want == 0 {
			if len(values) != 0 {
				t.Fatalf("unexpected range totals: %+v", values)
			}
			return
		}
		if len(values) != 2 {
			t.Fatalf("missing role totals: %+v", values)
		}
		for _, v := range values {
			if v.Bytes != want {
				t.Fatalf("%s bytes=%d want %d", v.Role, v.Bytes, want)
			}
		}
	}
	assertBytes(at, at.Add(TrafficBucketDuration), 100)
	assertBytes(at.Add(-TrafficBucketDuration), at, 0)
	// Loading twice must not replay the audit bootstrap.
	if _, _, _, err = s.LoadTraffic(ctx); err != nil {
		t.Fatal(err)
	}
	assertBytes(at, at.Add(TrafficBucketDuration), 100)
	next := at.Add(TrafficBucketDuration)
	totals := []TrafficTotal{{Role: "exporter", ClientID: "e", Name: "E", Bytes: 150}, {Role: "importer", ClientID: "i", Name: "I", Bytes: 150}}
	buckets := []TrafficBucket{{Role: "exporter", ClientID: "e", RunID: "run-1", At: next, Bytes: 50}, {Role: "importer", ClientID: "i", RunID: "run-1", At: next, Bytes: 50}}
	for n := 0; n < 2; n++ {
		if err = s.SaveTraffic(ctx, totals, buckets, next); err != nil {
			t.Fatal(err)
		}
	}
	assertBytes(next, next.Add(TrafficBucketDuration), 50)
	assertBytes(at, next.Add(TrafficBucketDuration), 150)
	// A second server run in the same bucket contributes once, independently.
	for n := range buckets {
		buckets[n].RunID = "run-2"
		buckets[n].Bytes = 25
		totals[n].Bytes = 175
	}
	if err = s.SaveTraffic(ctx, totals, buckets, next); err != nil {
		t.Fatal(err)
	}
	assertBytes(next, next.Add(TrafficBucketDuration), 75)
	// Audit retention cannot erase cumulative or time-filtered traffic.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM access_audit`); err != nil {
		t.Fatal(err)
	}
	_, _, saved, err := s.LoadTraffic(ctx)
	if err != nil || len(saved) != 2 || saved[0].Bytes != 175 || saved[1].Bytes != 175 {
		t.Fatalf("retained totals: %+v %v", saved, err)
	}
	assertBytes(at, next.Add(TrafficBucketDuration), 175)
	// An invalid bucket rolls back the cumulative update as well.
	totals[0].Bytes = 200
	buckets[0].Role = "invalid"
	if err = s.SaveTraffic(ctx, totals, buckets, next); err == nil {
		t.Fatal("invalid role accepted")
	}
	_, _, saved, err = s.LoadTraffic(ctx)
	if err != nil || saved[0].Bytes != 175 {
		t.Fatalf("partial save: %+v %v", saved, err)
	}
}
