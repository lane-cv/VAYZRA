package integration

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultPostgresURL = "postgres://happylearn:happylearn_dev@127.0.0.1:54329/happylearn?sslmode=disable"

func StartPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("HAPPYLEARN_TEST_DATABASE_URL")
	if url == "" {
		url = defaultPostgresURL
	}
	pool, err := pgxpool.New(context.Background(), url)
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
