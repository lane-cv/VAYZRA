package operations

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresSampleStoreRejectsBatchBoundsAndInvalidRowsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()

	if err := store.InsertSamples(ctx, now, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty InsertSamples() error=%v want ErrInvalid", err)
	}
	assertOperationalSampleCount(t, pool, 0)

	tooMany := make([]Sample, MaxSampleInsertBatch+1)
	for index := range tooMany {
		tooMany[index] = sampleQueueDepth(now, float64(index))
	}
	if err := store.InsertSamples(ctx, now, tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized InsertSamples() error=%v want ErrInvalid", err)
	}
	assertOperationalSampleCount(t, pool, 0)

	invalid := []Sample{
		sampleQueueDepth(now, 1),
		sampleQueueDepth(now, 2),
	}
	invalid[1].Scope = SampleScope("student-42")
	if err := store.InsertSamples(ctx, now, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid InsertSamples() error=%v want ErrInvalid", err)
	}
	assertOperationalSampleCount(t, pool, 0)

	maximum := make([]Sample, MaxSampleInsertBatch)
	for index := range maximum {
		maximum[index] = sampleQueueDepth(
			now.Add(time.Duration(index)*time.Nanosecond),
			float64(index),
		)
	}
	if err := store.InsertSamples(ctx, now, maximum); err != nil {
		t.Fatalf("maximum InsertSamples() error=%v", err)
	}
	assertOperationalSampleCount(t, pool, MaxSampleInsertBatch)
}

func TestPostgresSampleStoreRollsBackTheWholeBatchOnDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION operations_test_reject_sample() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.value=2 THEN
    RAISE EXCEPTION 'sample rejected by test fixture';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER operations_test_reject_sample
BEFORE INSERT ON operational_samples
FOR EACH ROW EXECUTE FUNCTION operations_test_reject_sample()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `
DROP TRIGGER IF EXISTS operations_test_reject_sample ON operational_samples;
DROP FUNCTION IF EXISTS operations_test_reject_sample()`)
	})

	err := store.InsertSamples(ctx, now, []Sample{
		sampleQueueDepth(now, 1),
		sampleQueueDepth(now, 2),
		sampleQueueDepth(now, 3),
	})
	if err == nil {
		t.Fatal("InsertSamples() error=nil want database rejection")
	}
	assertOperationalSampleCount(t, pool, 0)
}

func TestPostgresSampleStoreReadsOnlyFixedSeriesWithExplicitFreshness(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	for _, sample := range []Sample{
		sampleQueueDepth(now.Add(-30*time.Second), 3),
		sampleQueueDepth(now.Add(-10*time.Second), 7),
		{
			Source:     SampleSourceWorker,
			Metric:     SampleMetricQueueItems,
			Scope:      SampleScopeAI,
			Value:      11,
			Unit:       SampleUnitCount,
			ObservedAt: now.Add(-2 * time.Hour),
		},
	} {
		if err := store.InsertSamples(ctx, now, []Sample{sample}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES
  ('worker','private_metric','student_private',1,'count',$1,NULL),
  ('worker','queue_items','processing',99,'private_unit',$1,NULL)`,
		now.Add(-time.Second),
	); err != nil {
		t.Fatal(err)
	}

	fresh, err := store.ReadLatestSamples(ctx, SampleReadRequest{
		Source:   SampleSourceWorker,
		Metric:   SampleMetricQueueItems,
		Scope:    SampleScopeProcessing,
		Limit:    2,
		Now:      now,
		FreshFor: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Freshness != SampleFreshnessFresh || fresh.Unit != SampleUnitCount {
		t.Fatalf("fresh result=%+v", fresh)
	}
	if len(fresh.Samples) != 2 ||
		fresh.Samples[0].Value != 7 ||
		fresh.Samples[1].Value != 3 {
		t.Fatalf("fresh samples=%+v", fresh.Samples)
	}
	for _, sample := range fresh.Samples {
		if sample.Source != SampleSourceWorker ||
			sample.Metric != SampleMetricQueueItems ||
			sample.Scope != SampleScopeProcessing ||
			sample.Unit != SampleUnitCount {
			t.Fatalf("unbounded sample returned: %+v", sample)
		}
		if sample.ObservedAt.Location() != time.UTC {
			t.Fatalf("observed location=%v want UTC", sample.ObservedAt.Location())
		}
	}

	stale, err := store.ReadLatestSamples(ctx, SampleReadRequest{
		Source:   SampleSourceWorker,
		Metric:   SampleMetricQueueItems,
		Scope:    SampleScopeAI,
		Limit:    1,
		Now:      now,
		FreshFor: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Freshness != SampleFreshnessStale || len(stale.Samples) != 1 {
		t.Fatalf("stale result=%+v", stale)
	}

	empty, err := store.ReadLatestSamples(ctx, SampleReadRequest{
		Source:   SampleSourceWorker,
		Metric:   SampleMetricQueueItems,
		Scope:    SampleScopeOutbox,
		Limit:    1,
		Now:      now,
		FreshFor: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Freshness != SampleFreshnessEmpty || len(empty.Samples) != 0 {
		t.Fatalf("empty result=%+v", empty)
	}
}

func TestPostgresSampleStoreReadBoundariesFailClosed(t *testing.T) {
	ctx := context.Background()
	_, store := migratedSampleStore(t)
	now := sampleTestClock()
	base := SampleReadRequest{
		Source:   SampleSourceWorker,
		Metric:   SampleMetricQueueItems,
		Scope:    SampleScopeProcessing,
		Limit:    1,
		Now:      now,
		FreshFor: time.Minute,
	}
	tests := map[string]func(*SampleReadRequest){
		"zero limit": func(request *SampleReadRequest) {
			request.Limit = 0
		},
		"limit above maximum": func(request *SampleReadRequest) {
			request.Limit = MaxSampleReadLimit + 1
		},
		"zero clock": func(request *SampleReadRequest) {
			request.Now = time.Time{}
		},
		"zero freshness": func(request *SampleReadRequest) {
			request.FreshFor = 0
		},
		"freshness above maximum": func(request *SampleReadRequest) {
			request.FreshFor = MaxSampleFreshFor + time.Nanosecond
		},
		"unknown metric": func(request *SampleReadRequest) {
			request.Metric = SampleMetric("private")
		},
		"illegal combination": func(request *SampleReadRequest) {
			request.Source = SampleSourceHost
		},
		"UUID-like scope": func(request *SampleReadRequest) {
			request.Scope = SampleScope("11111111-1111-4111-8111-111111111111")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			_, err := store.ReadLatestSamples(ctx, request)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("ReadLatestSamples() error=%v want ErrInvalid", err)
			}
		})
	}
}

func TestPostgresSampleStoreFreshnessIncludesExactBoundary(t *testing.T) {
	ctx := context.Background()
	_, store := migratedSampleStore(t)
	now := sampleTestClock()
	if err := store.InsertSamples(
		ctx,
		now,
		[]Sample{sampleQueueDepth(now.Add(-time.Minute), 1)},
	); err != nil {
		t.Fatal(err)
	}
	result, err := store.ReadLatestSamples(ctx, SampleReadRequest{
		Source:   SampleSourceWorker,
		Metric:   SampleMetricQueueItems,
		Scope:    SampleScopeProcessing,
		Limit:    1,
		Now:      now,
		FreshFor: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Freshness != SampleFreshnessFresh {
		t.Fatalf("freshness=%q want %q", result.Freshness, SampleFreshnessFresh)
	}
}

func TestPostgresSampleRetentionUsesConfiguredDayBoundaries(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	for _, days := range []int{1, 30} {
		t.Run(fmt.Sprintf("%d days", days), func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE operational_samples`); err != nil {
				t.Fatal(err)
			}
			cutoff := now.AddDate(0, 0, -days)
			for _, observedAt := range []time.Time{
				cutoff.Add(-time.Second),
				cutoff,
				cutoff.Add(time.Second),
			} {
				if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES ('app','backup_age_seconds','local',1,'seconds',$1,NULL)`,
					observedAt,
				); err != nil {
					t.Fatal(err)
				}
			}
			deleted, err := store.DeleteExpiredSamples(ctx, now, days)
			if err != nil {
				t.Fatal(err)
			}
			if deleted != 1 {
				t.Fatalf("deleted=%d want=1", deleted)
			}
			var count int
			if err := pool.QueryRow(ctx, `
SELECT count(*) FROM operational_samples WHERE observed_at >= $1`,
				cutoff,
			).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Fatalf("retained boundary rows=%d want=2", count)
			}
		})
	}
}

func TestPostgresSampleRetentionDeletesAtMostOneOrderedThousandRowBatch(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
)
SELECT 'app','backup_age_seconds','local',value,'seconds',
       $1::timestamptz + value * interval '1 second',NULL
FROM generate_series(1,1005) AS value`,
		now.AddDate(0, 0, -8),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES
  ('app','backup_age_seconds','local',2001,'seconds',$1,NULL),
  ('app','backup_age_seconds','local',2002,'seconds',$2,NULL)`,
		now.AddDate(0, 0, -7),
		now.AddDate(0, 0, -6),
	); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteExpiredSamples(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != MaxSampleRetentionBatch {
		t.Fatalf("first deleted=%d want=%d", deleted, MaxSampleRetentionBatch)
	}
	var expiredCount int
	var oldestExpired time.Time
	if err := pool.QueryRow(ctx, `
SELECT count(*),min(observed_at)
FROM operational_samples
WHERE observed_at < $1`,
		now.AddDate(0, 0, -7),
	).Scan(&expiredCount, &oldestExpired); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 5 {
		t.Fatalf("expired remaining=%d want=5", expiredCount)
	}
	wantOldest := now.AddDate(0, 0, -8).Add(1001 * time.Second)
	if !oldestExpired.Equal(wantOldest) {
		t.Fatalf("oldest remaining=%s want=%s", oldestExpired, wantOldest)
	}

	deleted, err = store.DeleteExpiredSamples(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 5 {
		t.Fatalf("second deleted=%d want=5", deleted)
	}
	deleted, err = store.DeleteExpiredSamples(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("third deleted=%d want=0", deleted)
	}
	assertOperationalSampleCount(t, pool, 2)
}

func TestPostgresSampleRetentionRejectsInvalidSettingsWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES ('app','backup_age_seconds','local',1,'seconds',$1,NULL)`,
		now.AddDate(0, 0, -31),
	); err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{0, 31} {
		deleted, err := store.DeleteExpiredSamples(ctx, now, days)
		if !errors.Is(err, ErrInvalid) || deleted != 0 {
			t.Fatalf("days=%d deleted=%d error=%v", days, deleted, err)
		}
	}
	assertOperationalSampleCount(t, pool, 1)
}

func migratedSampleStore(t *testing.T) (*pgxpool.Pool, *PostgresSampleStore) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE operational_samples`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `TRUNCATE operational_samples`); err != nil {
			t.Errorf("truncate operational samples: %v", err)
		}
	})
	return pool, NewPostgresSampleStore(pool)
}

func sampleTestClock() time.Time {
	return time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
}

func sampleQueueDepth(observedAt time.Time, value float64) Sample {
	return Sample{
		Source:     SampleSourceWorker,
		Metric:     SampleMetricQueueItems,
		Scope:      SampleScopeProcessing,
		Value:      value,
		Unit:       SampleUnitCount,
		ObservedAt: observedAt,
	}
}

func assertOperationalSampleCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM operational_samples`,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("operational sample count=%d want=%d", got, want)
	}
}
