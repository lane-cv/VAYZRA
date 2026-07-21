package database_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"happylearn.local/app/db/migrations"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestSecureFileAccessMigrationUpHandlesExistingV5Logs(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := pool.Exec(ctx, `TRUNCATE file_access_logs`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 5); err != nil {
		t.Fatal(err)
	}

	var logID string
	if err := pool.QueryRow(ctx, migrationLogFixtureSQL(false)).Scan(&logID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 6); err != nil {
		t.Fatalf("upgrade v5 database with existing immutable log: %v", err)
	}
	var requestedID, result, reason, ip string
	if err := pool.QueryRow(ctx, `SELECT requested_file_version_id::text,result,reason_code,ip::text FROM file_access_logs WHERE id=$1`, logID).Scan(&requestedID, &result, &reason, &ip); err != nil {
		t.Fatal(err)
	}
	if requestedID == "" || result != "allow" || reason != "" || ip != "0.0.0.0/32" {
		t.Fatalf("backfill requested=%q result=%q reason=%q ip=%q", requestedID, result, reason, ip)
	}
	if _, err := pool.Exec(ctx, `UPDATE file_access_logs SET request_id='mutated' WHERE id=$1`, logID); err == nil {
		t.Fatal("immutable trigger was not restored after upgrade")
	}
	if _, err := provider.DownTo(ctx, 5); err != nil {
		t.Fatalf("representable non-empty history must downgrade: %v", err)
	}
	var retained bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM file_access_logs WHERE id=$1 AND file_version_id IS NOT NULL)`, logID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("representable v5 access log was not retained by downgrade")
	}
}

func TestSecureFileAccessMigrationDownRejectsUnrepresentableLogsAtomically(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE file_access_logs`); err != nil {
		t.Fatal(err)
	}
	var logID string
	if err := pool.QueryRow(ctx, migrationLogFixtureSQL(true)).Scan(&logID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 5); err == nil {
		t.Fatal("downgrade accepted a denied log that v5 cannot represent")
	}
	var applied, columnPresent, rowPresent bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=6 AND is_applied)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='file_access_logs' AND column_name='result')`).Scan(&columnPresent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM file_access_logs WHERE id=$1)`, logID).Scan(&rowPresent); err != nil {
		t.Fatal(err)
	}
	if !applied || !columnPresent || !rowPresent {
		t.Fatalf("failed downgrade was not atomic: applied=%t column=%t row=%t", applied, columnPresent, rowPresent)
	}
}

func TestSecureFileAccessMigrationDownLocksBeforeCheckingConcurrentLogs(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	const applicationName = "happylearn-down-lock-gate"
	provider, closeProvider := namedMigrationProvider(t, pool.Config().ConnString(), applicationName)
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE file_access_logs`); err != nil {
		t.Fatal(err)
	}

	insertTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer insertTx.Rollback(ctx)
	var logID string
	if err := insertTx.QueryRow(ctx, migrationLogFixtureSQL(true)).Scan(&logID); err != nil {
		t.Fatal(err)
	}

	downCtx, cancelDown := context.WithCancel(context.Background())
	defer cancelDown()
	downResult := make(chan error, 1)
	go func() {
		_, err := provider.DownTo(downCtx, 5)
		downResult <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	var pollErr error
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks l
				JOIN pg_stat_activity a ON a.pid=l.pid
				JOIN pg_class c ON c.oid=l.relation
				WHERE a.application_name=$1
				  AND c.relname='file_access_logs'
				  AND l.mode='AccessExclusiveLock'
				  AND NOT l.granted
				  AND a.wait_event_type='Lock'
			)`, applicationName).Scan(&waiting); err != nil {
			pollErr = err
			break
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pollErr != nil || !waiting {
		cancelDown()
		_ = insertTx.Rollback(ctx)
		select {
		case <-downResult:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled downgrade goroutine did not finish")
		}
		if pollErr != nil {
			t.Fatalf("inspect migration lock: %v", pollErr)
		}
		t.Fatal("downgrade did not wait for an ACCESS EXCLUSIVE lock on file_access_logs")
	}
	if err := insertTx.Commit(ctx); err != nil {
		cancelDown()
		select {
		case <-downResult:
		case <-time.After(5 * time.Second):
			t.Fatal("downgrade goroutine did not finish after commit failure cancellation")
		}
		t.Fatal(err)
	}
	select {
	case err := <-downResult:
		if err == nil || !strings.Contains(err.Error(), "SQLSTATE 55000") {
			t.Fatalf("downgrade err=%v, want representability SQLSTATE 55000", err)
		}
	case <-time.After(5 * time.Second):
		cancelDown()
		select {
		case <-downResult:
		case <-time.After(5 * time.Second):
			t.Fatal("timed-out downgrade goroutine did not finish after cancellation")
		}
		t.Fatal("downgrade did not finish after concurrent insert committed")
	}

	var applied, columnPresent, triggerPresent, rowPresent bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=6 AND is_applied)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='file_access_logs' AND column_name='result')`).Scan(&columnPresent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='file_access_logs_immutable' AND NOT tgisinternal)`).Scan(&triggerPresent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM file_access_logs WHERE id=$1 AND result='deny')`, logID).Scan(&rowPresent); err != nil {
		t.Fatal(err)
	}
	if !applied || !columnPresent || !triggerPresent || !rowPresent {
		t.Fatalf("failed concurrent downgrade was not atomic: applied=%t column=%t trigger=%t row=%t", applied, columnPresent, triggerPresent, rowPresent)
	}
}

func TestSecureFileAccessMigrationDownSucceedsWithoutLogs(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE file_access_logs`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 5); err != nil {
		t.Fatalf("clean downgrade: %v", err)
	}
	var columnPresent, triggerPresent bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='file_access_logs' AND column_name='result')`).Scan(&columnPresent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='file_access_logs_immutable' AND NOT tgisinternal)`).Scan(&triggerPresent); err != nil {
		t.Fatal(err)
	}
	if columnPresent || !triggerPresent {
		t.Fatalf("clean downgrade result column=%t immutable trigger=%t", columnPresent, triggerPresent)
	}
}

func registerMigrationProviderCleanup(t *testing.T, provider *goose.Provider, closeProvider func()) {
	t.Helper()
	t.Cleanup(closeProvider)
	t.Cleanup(func() {
		if _, err := provider.UpTo(context.Background(), 7); err != nil {
			t.Errorf("restore latest migration: %v", err)
		}
	})
}
func migrationProvider(t *testing.T, connectionString string) (*goose.Provider, func()) {
	t.Helper()
	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return provider, func() { _ = db.Close() }
}

func namedMigrationProvider(t *testing.T, connectionString, applicationName string) (*goose.Provider, func()) {
	t.Helper()
	separator := "?"
	if strings.Contains(connectionString, "?") {
		separator = "&"
	}
	return migrationProvider(t, connectionString+separator+"application_name="+applicationName)
}

func migrationLogFixtureSQL(denied bool) string {
	resultColumns := "file_version_id,"
	resultValues := "v.id,"
	if denied {
		resultColumns = "file_version_id,requested_file_version_id,result,reason_code,ip,"
		resultValues = "NULL,v.id,'deny','policy','127.0.0.1',"
	}
	return `WITH u AS (
		INSERT INTO users(username,display_name,role,password_hash,must_change_password)
		VALUES ('mig-'||replace(gen_random_uuid()::text,'-',''),'Migration Student','student','hash',false) RETURNING id
	), f AS (
		INSERT INTO files(created_by) SELECT id FROM u RETURNING id,created_by
	), v AS (
		INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by)
		SELECT id,1,'migration/'||gen_random_uuid()::text,'fixture.pdf','application/pdf',1,repeat('a',64),'ready',created_by FROM f RETURNING id
	)
	INSERT INTO file_access_logs(actor_user_id,` + resultColumns + `access_policy,request_id)
	SELECT u.id,` + resultValues + `'preview','migration-gate' FROM u,v RETURNING id::text`
}
