package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/database"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestHostSamplerLiveIntegration(t *testing.T) {
	if os.Getenv("HAPPYLEARN_HOST_METRICS_LIVE") != "1" {
		t.Skip("set HAPPYLEARN_HOST_METRICS_LIVE=1 via the live gate")
	}
	databaseURL := requireLiveEnvironment(t, "HAPPYLEARN_TEST_DATABASE_URL")
	redisAddress := requireLiveEnvironment(t, "HAPPYLEARN_TEST_REDIS_ADDR")
	samplerBinary := requireLiveEnvironment(
		t,
		"HAPPYLEARN_HOST_METRICS_LIVE_SAMPLER",
	)
	repositoryRoot := requireLiveEnvironment(
		t,
		"HAPPYLEARN_HOST_METRICS_REPOSITORY_ROOT",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE operational_samples`); err != nil {
		t.Fatal(err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	nonces, err := operations.NewRedisHostNonceStore(redisClient)
	if err != nil {
		t.Fatal(err)
	}

	const secretValue = "live-host-hmac-secret"
	handler, err := operations.NewInternalHandler(operations.InternalHTTPConfig{
		MetricsBearerSecret:   "live-metrics-bearer",
		HostMetricsHMACSecret: []byte(secretValue),
		Clock:                 time.Now,
		Metrics:               liveMetricsSource{},
		Samples:               operations.NewPostgresSampleStore(pool),
		Nonces:                nonces,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:9090")
	if err != nil {
		t.Fatalf("listen on fixed internal endpoint: %v", err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		err := <-serveResult
		if err != nil && err != http.ErrServerClosed {
			t.Errorf("internal server: %v", err)
		}
	})

	fixtureRoot := t.TempDir()
	secretPath := filepath.Join(fixtureRoot, "host-hmac")
	if err := os.WriteFile(secretPath, []byte(secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(fixtureRoot, "backup")
	if err := os.Mkdir(backupPath, 0o700); err != nil {
		t.Fatal(err)
	}
	backupPath, err = filepath.EvalSymlinks(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(fixtureRoot, "bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLiveExecutable(t, fakeBin, "timeout", `#!/usr/bin/env bash
set -euo pipefail
shift
exec "$@"
`)
	writeLiveExecutable(t, fakeBin, "docker", `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *" ps --format json"* ]]; then
  printf '%s\n' '[{"ID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","Service":"backup","State":"exited","Health":""},{"ID":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","Service":"redis","State":"exited","Health":""},{"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Service":"app","State":"running","Health":"healthy","RestartCount":3}]'
  exit 0
fi
if [[ "$*" == *"stats --no-stream "* &&
      "$*" == *"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"* ]]; then
  printf '%s\n' '{"CPUPerc":"12.50%","MemUsage":"1.234MiB / 2GiB"}'
  exit 0
fi
exit 31
`)
	writeLiveExecutable(t, fakeBin, "df", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' 'Filesystem 1024-blocks Used Available Capacity Mounted on'
if [[ "${*: -1}" == "/" ]]; then
  printf '%s\n' '/dev/root 100 38 62 38% /'
elif [[ "${*: -1}" == "$HAPPYLEARN_BACKUP_HOST_PATH" ]]; then
  printf '%s\n' '/dev/backup 100 75 25 75% /backup'
else
  exit 32
fi
`)

	runCtx, runCancel := context.WithTimeout(ctx, 20*time.Second)
	defer runCancel()
	command := exec.CommandContext(
		runCtx,
		"bash",
		filepath.Join(repositoryRoot, "scripts", "collect-host-metrics.sh"),
		"--environment",
		"development",
	)
	command.Dir = repositoryRoot
	command.Env = append(
		os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOST_SAMPLER_BIN="+samplerBinary,
		"HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE="+secretPath,
		"HAPPYLEARN_BACKUP_HOST_PATH="+backupPath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("collect host metrics: %v output=%q", err, output)
	}
	if string(output) != "host metrics collection: PASS\n" {
		t.Fatalf("collector output=%q", output)
	}

	assertLiveHostSamples(t, ctx, pool)
	size, err := redisClient.DBSize(ctx).Result()
	if err != nil || size != 1 {
		t.Fatalf("redis nonce count=(%d,%v) want=(1,nil)", size, err)
	}
	keys, err := redisClient.Keys(
		ctx,
		"happylearn:operations:host-nonce:*",
	).Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("redis nonce keys=(%v,%v)", keys, err)
	}
}

type liveMetricsSource struct{}

func (liveMetricsSource) LatestMetrics(
	context.Context,
	time.Time,
) ([]operations.Sample, error) {
	return []operations.Sample{}, nil
}

type liveExpectedSample struct {
	value float64
	unit  string
}

func assertLiveHostSamples(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	expected := make(map[string]liveExpectedSample, 27)
	for _, scope := range []string{
		"caddy", "app", "worker", "postgres", "redis", "object_store",
	} {
		up := float64(0)
		cpu := float64(0)
		memory := float64(0)
		limit := float64(0)
		if scope == "app" {
			up = 1
			cpu = 12.5
			memory = 1293943
			limit = 2147483648
		}
		expected["service_up/"+scope] = liveExpectedSample{up, "boolean"}
		expected["host_service_cpu_percent/"+scope] = liveExpectedSample{cpu, "percent"}
		expected["host_service_memory_bytes/"+scope] = liveExpectedSample{memory, "bytes"}
		expected["host_service_memory_limit_bytes/"+scope] = liveExpectedSample{limit, "bytes"}
	}
	expected["host_service_restarts/app"] = liveExpectedSample{3, "count"}
	expected["filesystem_used_percent/root"] = liveExpectedSample{38, "percent"}
	expected["filesystem_used_percent/backup"] = liveExpectedSample{75, "percent"}

	rows, err := pool.Query(ctx, `
SELECT metric_name,scope,value,unit
FROM operational_samples
WHERE source='host'
ORDER BY metric_name,scope,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actual := make(map[string]liveExpectedSample, len(expected))
	for rows.Next() {
		var metric, scope, unit string
		var value float64
		if err := rows.Scan(&metric, &scope, &value, &unit); err != nil {
			t.Fatal(err)
		}
		key := metric + "/" + scope
		if _, duplicate := actual[key]; duplicate {
			t.Fatalf("duplicate host series %q", key)
		}
		actual[key] = liveExpectedSample{value, unit}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("sample count=%d want=%d actual=%v", len(actual), len(expected), actual)
	}
	for key, want := range expected {
		if got, exists := actual[key]; !exists || got != want {
			t.Fatalf("sample %q=(%+v,%t) want=%+v", key, got, exists, want)
		}
	}
	var distinctObservedAt int
	if err := pool.QueryRow(ctx, `
SELECT count(DISTINCT observed_at)
FROM operational_samples
WHERE source='host'`).Scan(&distinctObservedAt); err != nil {
		t.Fatal(err)
	}
	if distinctObservedAt != 1 {
		t.Fatalf("distinct observed_at=%d want=1", distinctObservedAt)
	}
}

func requireLiveEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func writeLiveExecutable(
	t *testing.T,
	directory string,
	name string,
	body string,
) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
