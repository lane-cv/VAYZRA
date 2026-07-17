package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestRunCreateTeacherCreatesOnlyOneAdmin(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(t.TempDir(), "teacher-password")
	if err := os.WriteFile(passwordFile, []byte("Temporary Password 42!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hasher := auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16})
	var output bytes.Buffer
	deps := dependencies{
		loadConfig: func(func(string) string) (config.Config, error) { return config.Config{DatabaseURL: "test"}, nil },
		open: func(ctx context.Context, _ string) (*pgxpool.Pool, error) {
			return pgxpool.New(ctx, pool.Config().ConnString())
		},
		migrate:  database.Migrate,
		newUsers: func(pool *pgxpool.Pool) auth.UserStore { return auth.NewPostgresUserStore(pool) },
		hash:     hasher.Hash,
		stdout:   &output,
	}
	args := []string{"create-teacher", "--username", "teacher_admin", "--display-name", "老师", "--password-file", passwordFile}
	if err := run(ctx, args, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewPostgresUserStore(pool).FindByUsername(ctx, "teacher_admin"); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, args, deps); err == nil || err.Error() != "teacher administrator already exists" {
		t.Fatalf("second run error=%v", err)
	}
}
