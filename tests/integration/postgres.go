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
	t.Cleanup(pool.Close)
	return pool
}
