package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestOperationsMonitoringMigrationContracts(t *testing.T) {
	pool, ctx := migratedOperationsMonitoring(t)

	var tables, constraints, indexes int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('operational_samples','operational_alerts','alert_deliveries')),
  (SELECT count(*) FROM pg_constraint c
   JOIN pg_class r ON r.oid=c.conrelid
   JOIN pg_namespace n ON n.oid=r.relnamespace
   WHERE n.nspname='public'
     AND (r.relname,c.conname) IN (
       ('operational_samples','operational_samples_source_check'),
       ('operational_samples','operational_samples_value_check'),
       ('operational_alerts','operational_alerts_severity_check'),
       ('operational_alerts','operational_alerts_state_check'),
       ('operational_alerts','operational_alerts_value_check'),
       ('operational_alerts','operational_alerts_acknowledgement_check'),
       ('operational_alerts','operational_alerts_resolution_check'),
       ('alert_deliveries','alert_deliveries_outcome_check'))),
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname='public' AND indexname IN (
     'operational_samples_metric_time_idx',
     'operational_alerts_open_dedupe_key',
     'operational_alerts_state_time_idx',
     'alert_deliveries_alert_attempt_key'))`).
		Scan(&tables, &constraints, &indexes); err != nil {
		t.Fatal(err)
	}
	if tables != 3 || constraints != 8 || indexes != 4 {
		t.Fatalf(
			"tables=%d constraints=%d indexes=%d",
			tables,
			constraints,
			indexes,
		)
	}

	var unresolvedDefinition string
	if err := pool.QueryRow(ctx, `
SELECT indexdef
FROM pg_indexes
WHERE schemaname='public'
  AND indexname='operational_alerts_open_dedupe_key'`).
		Scan(&unresolvedDefinition); err != nil {
		t.Fatal(err)
	}
	if unresolvedDefinition !=
		"CREATE UNIQUE INDEX operational_alerts_open_dedupe_key ON public.operational_alerts USING btree (dedupe_key) WHERE (state <> 'resolved'::text)" {
		t.Fatalf("unexpected unresolved dedupe index: %s", unresolvedDefinition)
	}
}

func TestOperationsMonitoringMigrationRejectsInvalidShapes(t *testing.T) {
	pool, ctx := migratedOperationsMonitoring(t)

	for _, test := range []struct {
		name       string
		statement  string
		code       string
		constraint string
	}{
		{
			"unknown sample source",
			`INSERT INTO operational_samples(
			   source,metric_name,scope,value,unit,observed_at
			 ) VALUES('browser','disk_used_percent','host',1,'percent',now())`,
			"23514",
			"operational_samples_source_check",
		},
		{
			"NaN sample",
			`INSERT INTO operational_samples(
			   source,metric_name,scope,value,unit,observed_at
			 ) VALUES('host','disk_used_percent','host','NaN','percent',now())`,
			"23514",
			"operational_samples_value_check",
		},
		{
			"infinite sample",
			`INSERT INTO operational_samples(
			   source,metric_name,scope,value,unit,observed_at
			 ) VALUES('host','disk_used_percent','host','Infinity','percent',now())`,
			"23514",
			"operational_samples_value_check",
		},
		{
			"empty metric",
			`INSERT INTO operational_samples(
			   source,metric_name,scope,value,unit,observed_at
			 ) VALUES('host','','host',1,'percent',now())`,
			"23514",
			"operational_samples_value_check",
		},
		{
			"invalid alert severity",
			`INSERT INTO operational_alerts(
			   dedupe_key,category,severity,state,first_observed_at,
			   last_observed_at,current_value,threshold_value,summary
			 ) VALUES(
			   'disk:host','capacity','emergency','open',now(),now(),95,90,'Disk'
			 )`,
			"23514",
			"operational_alerts_severity_check",
		},
		{
			"invalid alert state",
			`INSERT INTO operational_alerts(
			   dedupe_key,category,severity,state,first_observed_at,
			   last_observed_at,current_value,threshold_value,summary
			 ) VALUES(
			   'disk:host','capacity','warning','muted',now(),now(),95,90,'Disk'
			 )`,
			"23514",
			"operational_alerts_state_check",
		},
		{
			"NaN alert value",
			`INSERT INTO operational_alerts(
			   dedupe_key,category,severity,state,first_observed_at,
			   last_observed_at,current_value,threshold_value,summary
			 ) VALUES(
			   'disk:host','capacity','warning','open',now(),now(),'NaN',90,'Disk'
			 )`,
			"23514",
			"operational_alerts_value_check",
		},
		{
			"open alert with acknowledgement",
			`WITH actor AS (
			   INSERT INTO users(
			     username,display_name,role,password_hash
			   ) VALUES(
			     'monitoring_actor','Monitoring Actor','student','password-hash'
			   ) RETURNING id
			 )
			 INSERT INTO operational_alerts(
			   dedupe_key,category,severity,state,first_observed_at,
			   last_observed_at,acknowledged_by,acknowledged_at,
			   current_value,threshold_value,summary
			 )
			 SELECT
			   'disk:host','capacity','warning','open',now(),now(),id,now(),
			   95,90,'Disk'
			 FROM actor`,
			"23514",
			"operational_alerts_acknowledgement_check",
		},
		{
			"resolved alert without resolution time",
			`INSERT INTO operational_alerts(
			   dedupe_key,category,severity,state,first_observed_at,
			   last_observed_at,current_value,threshold_value,summary
			 ) VALUES(
			   'disk:host','capacity','warning','resolved',now(),now(),70,90,'Disk'
			 )`,
			"23514",
			"operational_alerts_resolution_check",
		},
		{
			"delivery attempt above limit",
			validAlertCTE("delivery-attempt") + `
			 INSERT INTO alert_deliveries(
			   alert_id,attempt,destination,outcome,started_at,finished_at
			 ) SELECT id,5,'webhook','failed',now(),now() FROM alert`,
			"23514",
			"alert_deliveries_attempt_check",
		},
		{
			"unknown delivery outcome",
			validAlertCTE("delivery-outcome") + `
			 INSERT INTO alert_deliveries(
			   alert_id,attempt,destination,outcome,started_at,finished_at
			 ) SELECT id,1,'webhook','retrying',now(),now() FROM alert`,
			"23514",
			"alert_deliveries_outcome_check",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			operationsMonitoringTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, test.statement)
				assertOperationsMonitoringPostgresError(
					t,
					err,
					test.code,
					test.constraint,
				)
			})
		})
	}
}

func TestOperationsMonitoringMigrationDedupeAndValidBoundaries(t *testing.T) {
	pool, ctx := migratedOperationsMonitoring(t)
	operationsMonitoringTx(t, ctx, pool, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES
  ('app','active_students','global',0,'count',now(),now()),
  ('host','disk_used_percent','host',100,'percent',now(),now())`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary
) VALUES(
  'disk:host','capacity','critical','open',now(),now(),100,90,'磁盘空间不足'
)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT duplicate_unresolved_alert`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary
) VALUES(
  'disk:host','capacity','warning','open',now(),now(),95,75,'磁盘空间告警'
)`)
		assertOperationsMonitoringPostgresError(
			t,
			err,
			"23505",
			"operational_alerts_open_dedupe_key",
		)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT duplicate_unresolved_alert`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE operational_alerts
SET state='resolved',resolved_at=now()
WHERE dedupe_key='disk:host';
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary
) VALUES(
  'disk:host','capacity','warning','open',now(),now(),76,75,'磁盘空间告警'
)`); err != nil {
			t.Fatalf("reuse resolved dedupe key: %v", err)
		}
		if _, err := tx.Exec(ctx, `
WITH alert AS (
  SELECT id FROM operational_alerts
  WHERE dedupe_key='disk:host' AND state='open'
)
INSERT INTO alert_deliveries(
  alert_id,attempt,destination,outcome,http_status_class,
  started_at,finished_at
)
SELECT id,1,'webhook','succeeded',2,now(),now() FROM alert`); err != nil {
			t.Fatalf("insert valid delivery: %v", err)
		}
	})
}

func TestOperationsMonitoringMigrationDownTo20KeepsBackupSchema(t *testing.T) {
	pool, ctx := migratedOperationsMonitoring(t)
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 20); err != nil {
		t.Fatal(err)
	}

	var monitoringTables, backupTables int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('operational_samples','operational_alerts','alert_deliveries')),
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('backup_runs','backup_artifacts','restore_verifications'))`).
		Scan(&monitoringTables, &backupTables); err != nil {
		t.Fatal(err)
	}
	if monitoringTables != 0 || backupTables != 3 {
		t.Fatalf(
			"monitoring tables=%d backup tables=%d",
			monitoringTables,
			backupTables,
		)
	}
}

func validAlertCTE(key string) string {
	return `
WITH alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    current_value,threshold_value,summary
  ) VALUES(
    '` + key + `','capacity','warning','open',now(),now(),95,90,'Disk'
  ) RETURNING id
)`
}

func migratedOperationsMonitoring(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func operationsMonitoringTx(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	test func(pgx.Tx),
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback operations monitoring transaction: %v", err)
		}
	}()
	test(tx)
}

func assertOperationsMonitoringPostgresError(
	t *testing.T,
	err error,
	code string,
	constraint string,
) {
	t.Helper()
	if err == nil {
		t.Fatal("constraint accepted invalid mutation")
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) ||
		postgresError.Code != code ||
		postgresError.ConstraintName != constraint {
		t.Fatalf(
			"expected PostgreSQL code=%s constraint=%s, got %v",
			code,
			constraint,
			err,
		)
	}
}
