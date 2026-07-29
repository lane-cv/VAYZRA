package operations

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestPostgresSampleStoreRollsBackWhenCopyFromSkipsARow(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION operations_test_skip_sample() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.value=2 THEN
    RETURN NULL;
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER operations_test_skip_sample
BEFORE INSERT ON operational_samples
FOR EACH ROW EXECUTE FUNCTION operations_test_skip_sample()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `
DROP TRIGGER IF EXISTS operations_test_skip_sample ON operational_samples;
DROP FUNCTION IF EXISTS operations_test_skip_sample()`)
	})

	err := store.InsertSamples(ctx, now, []Sample{
		sampleQueueDepth(now, 1),
		sampleQueueDepth(now, 2),
		sampleQueueDepth(now, 3),
	})
	if err == nil {
		t.Fatal("InsertSamples() error=nil want short CopyFrom rejection")
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
			Scope:      SampleScopeOutbox,
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
  ('worker','private_metric','student_private',1,'count',$1,NULL)`,
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
		Scope:    SampleScopeOutbox,
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
		Source:   SampleSourceApp,
		Metric:   SampleMetricAIRuns,
		Scope:    SampleScopeQueued,
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

func TestPostgresSampleStoreFailsClosedOnAnyPollutedSeriesRow(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	for _, test := range []struct {
		name            string
		validObservedAt time.Time
		badObservedAt   time.Time
	}{
		{
			name:            "polluted newest row cannot expose older fresh value",
			validObservedAt: now.Add(-2 * time.Second),
			badObservedAt:   now.Add(-time.Second),
		},
		{
			name:            "polluted older row clears a partially scanned result",
			validObservedAt: now.Add(-time.Second),
			badObservedAt:   now.Add(-2 * time.Second),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE operational_samples`); err != nil {
				t.Fatal(err)
			}
			if err := store.InsertSamples(
				ctx,
				now,
				[]Sample{sampleQueueDepth(test.validObservedAt, 7)},
			); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES ('worker','queue_items','processing',99,'private_unit',$1,NULL)`,
				test.badObservedAt,
			); err != nil {
				t.Fatal(err)
			}
			result, err := store.ReadLatestSamples(ctx, SampleReadRequest{
				Source:   SampleSourceWorker,
				Metric:   SampleMetricQueueItems,
				Scope:    SampleScopeProcessing,
				Limit:    2,
				Now:      now,
				FreshFor: time.Minute,
			})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("ReadLatestSamples() error=%v want ErrInvalid", err)
			}
			if len(result.Samples) != 0 || result.Freshness != SampleFreshnessEmpty {
				t.Fatalf("polluted result=%+v want empty", result)
			}
		})
	}
}

func TestPostgresSampleStoreStableLatestOrderSurvivesRewriteAndReindex(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	if err := store.InsertSamples(ctx, now, []Sample{
		sampleQueueDepth(now, 30),
		sampleQueueDepth(now, 10),
		sampleQueueDepth(now, 20),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
CREATE INDEX operations_test_samples_rewrite_idx
  ON operational_samples(value DESC);
CLUSTER operational_samples USING operations_test_samples_rewrite_idx;
REINDEX INDEX operational_samples_metric_time_idx`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = pool.Exec(
			cleanupCtx,
			`DROP INDEX IF EXISTS operations_test_samples_rewrite_idx`,
		)
	})

	result, err := store.ReadLatestSamples(ctx, SampleReadRequest{
		Source:   SampleSourceWorker,
		Metric:   SampleMetricQueueItems,
		Scope:    SampleScopeProcessing,
		Limit:    3,
		Now:      now,
		FreshFor: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]float64, len(result.Samples))
	for index, sample := range result.Samples {
		got[index] = sample.Value
	}
	want := []float64{20, 10, 30}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stable values=%v want=%v", got, want)
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

func TestPostgresSampleStoreRejectsNilContextAndHandlesCanceledOrClosedStores(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	request := SampleReadRequest{
		Source:   SampleSourceWorker,
		Metric:   SampleMetricQueueItems,
		Scope:    SampleScopeProcessing,
		Limit:    1,
		Now:      now,
		FreshFor: time.Minute,
	}
	t.Run("nil contexts", func(t *testing.T) {
		if err := store.InsertSamples(nil, now, []Sample{sampleQueueDepth(now, 1)}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("InsertSamples(nil) error=%v want ErrInvalid", err)
		}
		if result, err := store.ReadLatestSamples(nil, request); !errors.Is(err, ErrInvalid) || len(result.Samples) != 0 {
			t.Fatalf("ReadLatestSamples(nil) result=%+v error=%v", result, err)
		}
		if deleted, err := store.DeleteExpiredSamples(nil, now, 7); !errors.Is(err, ErrInvalid) || deleted != 0 {
			t.Fatalf("DeleteExpiredSamples(nil) deleted=%d error=%v", deleted, err)
		}
		assertOperationalSampleCount(t, pool, 0)
	})

	t.Run("canceled contexts", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := store.InsertSamples(canceled, now, []Sample{sampleQueueDepth(now, 1)}); !errors.Is(err, context.Canceled) {
			t.Fatalf("InsertSamples(canceled) error=%v want context.Canceled", err)
		}
		if result, err := store.ReadLatestSamples(canceled, request); !errors.Is(err, context.Canceled) || len(result.Samples) != 0 {
			t.Fatalf("ReadLatestSamples(canceled) result=%+v error=%v", result, err)
		}
		if deleted, err := store.DeleteExpiredSamples(canceled, now, 7); !errors.Is(err, context.Canceled) || deleted != 0 {
			t.Fatalf("DeleteExpiredSamples(canceled) deleted=%d error=%v", deleted, err)
		}
		assertOperationalSampleCount(t, pool, 0)
	})

	t.Run("closed pool", func(t *testing.T) {
		closedPool, err := pgxpool.New(ctx, pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		closedPool.Close()
		closedStore := NewPostgresSampleStore(closedPool)
		if err := closedStore.InsertSamples(ctx, now, []Sample{sampleQueueDepth(now, 1)}); err == nil {
			t.Fatal("InsertSamples(closed) error=nil")
		}
		if result, err := closedStore.ReadLatestSamples(ctx, request); err == nil || len(result.Samples) != 0 {
			t.Fatalf("ReadLatestSamples(closed) result=%+v error=%v", result, err)
		}
		if deleted, err := closedStore.DeleteExpiredSamples(ctx, now, 7); err == nil || deleted != 0 {
			t.Fatalf("DeleteExpiredSamples(closed) deleted=%d error=%v", deleted, err)
		}
		assertOperationalSampleCount(t, pool, 0)
	})
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

func TestPostgresSampleStoreMetricsSelectsOneLatestFixedSeries(t *testing.T) {
	ctx := context.Background()
	_, store := migratedSampleStore(t)
	now := sampleTestClock()
	if err := store.InsertSamples(ctx, now, []Sample{
		{
			Source: SampleSourceApp, Metric: SampleMetricServiceUp,
			Scope: SampleScopeApp, Value: 0, Unit: SampleUnitBoolean,
			ObservedAt: now.Add(-2 * time.Minute),
		},
		{
			Source: SampleSourceHost, Metric: SampleMetricServiceUp,
			Scope: SampleScopeApp, Value: 1, Unit: SampleUnitBoolean,
			ObservedAt: now.Add(-time.Minute),
		},
		{
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: 7, Unit: SampleUnitCount,
			ObservedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.LatestMetrics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []Sample{
		{
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: 7, Unit: SampleUnitCount,
			ObservedAt: now,
		},
		{
			Source: SampleSourceHost, Metric: SampleMetricServiceUp,
			Scope: SampleScopeApp, Value: 1, Unit: SampleUnitBoolean,
			ObservedAt: now.Add(-time.Minute),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LatestMetrics()=%#v want=%#v", got, want)
	}
}

func TestPostgresSampleRetentionSkipsLockedRowsWithoutBlockingAndKeepsExactBatches(t *testing.T) {
	ctx := context.Background()
	pool, store := migratedSampleStore(t)
	now := sampleTestClock()
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
)
SELECT 'app','backup_age_seconds','local',value,'seconds',$1,NULL
FROM generate_series(1,1010) AS value`,
		now.AddDate(0, 0, -8),
	); err != nil {
		t.Fatal(err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx)
	rows, err := lockTx.Query(ctx, `
SELECT id
FROM operational_samples
ORDER BY observed_at,id
LIMIT 10
FOR UPDATE`)
	if err != nil {
		t.Fatal(err)
	}
	var lockedIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		lockedIDs = append(lockedIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lockedIDs) != 10 {
		t.Fatalf("locked ids=%d want=10", len(lockedIDs))
	}

	type retentionResult struct {
		deleted int64
		err     error
	}
	result := make(chan retentionResult, 1)
	go func() {
		deleted, deleteErr := store.DeleteExpiredSamples(ctx, now, 7)
		result <- retentionResult{deleted: deleted, err: deleteErr}
	}()
	select {
	case got := <-result:
		if got.err != nil || got.deleted != MaxSampleRetentionBatch {
			t.Fatalf("concurrent retention=(%d,%v)", got.deleted, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retention blocked behind locked rows instead of using SKIP LOCKED")
	}
	assertOperationalSampleCount(t, pool, 10)
	if err := lockTx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal(err)
	}
	deleted, err := store.DeleteExpiredSamples(ctx, now, 7)
	if err != nil || deleted != 10 {
		t.Fatalf("second retention=(%d,%v) want=(10,nil)", deleted, err)
	}
	assertOperationalSampleCount(t, pool, 0)
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
