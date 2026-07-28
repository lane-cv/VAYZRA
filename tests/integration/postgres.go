package integration

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultPostgresURL = "postgres://happylearn:happylearn_dev@127.0.0.1:54329/happylearn?sslmode=disable"

func StartPostgres(t *testing.T) *pgxpool.Pool {
	return startPostgres(t, 0)
}

// QueueOperationsExclusiveBehindShared holds one shared operations admission
// lock and queues an exclusive waiter behind it. The returned function releases
// both locks in order.
func QueueOperationsExclusiveBehindShared(
	t *testing.T,
	pool *pgxpool.Pool,
) func() {
	t.Helper()
	ctx := context.Background()
	shared, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Exec(ctx, `SELECT pg_advisory_lock_shared(845103120)`); err != nil {
		shared.Release()
		t.Fatal(err)
	}
	exclusive, err := pool.Acquire(ctx)
	if err != nil {
		_, _ = shared.Exec(ctx, `SELECT pg_advisory_unlock_shared(845103120)`)
		shared.Release()
		t.Fatal(err)
	}
	var pid int
	if err := exclusive.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		exclusive.Release()
		_, _ = shared.Exec(ctx, `SELECT pg_advisory_unlock_shared(845103120)`)
		shared.Release()
		t.Fatal(err)
	}
	exclusiveResult := make(chan error, 1)
	go func() {
		_, lockErr := exclusive.Exec(ctx, `SELECT pg_advisory_lock(845103120)`)
		exclusiveResult <- lockErr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var queued bool
		if err := pool.QueryRow(ctx, `
SELECT wait_event_type='Lock'
FROM pg_stat_activity
WHERE pid=$1`, pid).Scan(&queued); err != nil {
			exclusive.Release()
			_, _ = shared.Exec(ctx, `SELECT pg_advisory_unlock_shared(845103120)`)
			shared.Release()
			t.Fatal(err)
		}
		if queued {
			break
		}
		if time.Now().After(deadline) {
			exclusive.Release()
			_, _ = shared.Exec(ctx, `SELECT pg_advisory_unlock_shared(845103120)`)
			shared.Release()
			t.Fatal("exclusive operations admission did not queue")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			_, _ = shared.Exec(ctx, `SELECT pg_advisory_unlock_shared(845103120)`)
			shared.Release()
			if err := <-exclusiveResult; err == nil {
				_, _ = exclusive.Exec(ctx, `SELECT pg_advisory_unlock(845103120)`)
			}
			exclusive.Release()
		})
	}
	t.Cleanup(finish)
	return finish
}

func StartPostgresWithMaxConns(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	return startPostgres(t, maxConns)
}

func startPostgres(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("HAPPYLEARN_TEST_DATABASE_URL")
	if url == "" {
		url = defaultPostgresURL
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse postgres test pool config: %v", err)
	}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open postgres test pool: %v", err)
	}
	lockConn, err := pool.Acquire(context.Background())
	if err != nil {
		pool.Close()
		t.Fatalf("acquire postgres test lock: %v", err)
	}
	if _, err := lockConn.Exec(context.Background(), "SELECT pg_advisory_lock(845103119)"); err != nil {
		lockConn.Release()
		pool.Close()
		t.Fatalf("lock postgres test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = lockConn.Exec(context.Background(), "SELECT pg_advisory_unlock(845103119)")
		lockConn.Release()
		pool.Close()
	})
	return pool
}
