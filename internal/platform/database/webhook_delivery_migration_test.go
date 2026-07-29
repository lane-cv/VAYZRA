package database_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestWebhookDeliveryMigrationHasTransitionIdentityAndDurableClaims(t *testing.T) {
	pool, ctx := migratedOperationsMonitoring(t)

	var (
		eventTable  string
		eventUnique int
		columns     int
		constraints int
		indexes     int
	)
	if err := pool.QueryRow(ctx, `
SELECT
  COALESCE(to_regclass('public.alert_webhook_events')::text,''),
  (SELECT count(*) FROM pg_constraint
   WHERE conrelid=to_regclass('public.alert_webhook_events')
     AND conname='alert_webhook_events_transition_key'),
  (SELECT count(*) FROM information_schema.columns
   WHERE table_schema='public' AND table_name='alert_deliveries'
     AND column_name IN (
       'event_id','delivery_state','scheduled_at',
       'claim_owner','claim_token','claim_expires_at'
     )),
  (SELECT count(*) FROM pg_constraint
   WHERE conrelid='public.alert_deliveries'::regclass
     AND conname IN (
       'alert_deliveries_attempt_check',
       'alert_deliveries_state_check',
       'alert_deliveries_event_check',
       'alert_deliveries_claim_check',
       'alert_deliveries_outcome_check',
       'alert_deliveries_timing_check'
     )),
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname='public' AND indexname IN (
     'alert_deliveries_alert_attempt_key',
     'alert_deliveries_event_attempt_key',
     'alert_deliveries_due_claim_idx'
   ))`).
		Scan(&eventTable, &eventUnique, &columns, &constraints, &indexes); err != nil {
		t.Fatal(err)
	}
	if eventTable != "alert_webhook_events" ||
		eventUnique != 1 ||
		columns != 6 ||
		constraints != 6 ||
		indexes != 3 {
		t.Fatalf(
			"event=%q unique=%d columns=%d constraints=%d indexes=%d",
			eventTable,
			eventUnique,
			columns,
			constraints,
			indexes,
		)
	}

	var (
		legacyIndex string
		eventIndex  string
		dueIndex    string
	)
	for name, target := range map[string]*string{
		"alert_deliveries_alert_attempt_key": &legacyIndex,
		"alert_deliveries_event_attempt_key": &eventIndex,
		"alert_deliveries_due_claim_idx":     &dueIndex,
	} {
		if err := pool.QueryRow(ctx, `
SELECT indexdef FROM pg_indexes
WHERE schemaname='public' AND indexname=$1`, name).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if !containsAll(
		legacyIndex,
		"UNIQUE",
		"(alert_id, attempt, destination)",
		"WHERE (event_id IS NULL)",
	) ||
		!containsAll(
			eventIndex,
			"UNIQUE",
			"(event_id, attempt, destination)",
			"WHERE (event_id IS NOT NULL)",
		) ||
		!containsAll(
			dueIndex,
			"(scheduled_at, event_id, attempt)",
			"WHERE (delivery_state = ANY",
		) {
		t.Fatalf(
			"legacy=%q event=%q due=%q",
			legacyIndex,
			eventIndex,
			dueIndex,
		)
	}
}

func TestWebhookDeliveryMigrationRejectsInvalidTransitionJobs(t *testing.T) {
	pool, ctx := migratedOperationsMonitoring(t)
	operationsMonitoringTx(t, ctx, pool, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, webhookEventFixtureSQL); err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			name       string
			statement  string
			code       string
			constraint string
		}{
			{
				name: "attempt four",
				statement: `
INSERT INTO alert_deliveries(
  id,alert_id,event_id,attempt,destination,delivery_state,scheduled_at
)
SELECT gen_random_uuid(),alert_id,id,4,'webhook','pending',enqueued_at
FROM alert_webhook_events`,
				code:       "23514",
				constraint: "alert_deliveries_attempt_check",
			},
			{
				name: "pending claim",
				statement: `
INSERT INTO alert_deliveries(
  id,alert_id,event_id,attempt,destination,delivery_state,scheduled_at,
  claim_owner,claim_token,claim_expires_at
)
SELECT gen_random_uuid(),alert_id,id,1,'webhook','pending',enqueued_at,
       'worker',gen_random_uuid(),enqueued_at + interval '1 minute'
FROM alert_webhook_events`,
				code:       "23514",
				constraint: "alert_deliveries_claim_check",
			},
			{
				name: "duplicate transition",
				statement: `
INSERT INTO alert_webhook_events(
  id,alert_id,transition_kind,alert_version,category,severity,state,
  summary,current_value,threshold_value,first_observed_at,last_observed_at,
  enqueued_at
)
SELECT gen_random_uuid(),alert_id,transition_kind,alert_version,category,
       severity,state,summary,current_value,threshold_value,first_observed_at,
       last_observed_at,enqueued_at
FROM alert_webhook_events`,
				code:       "23505",
				constraint: "alert_webhook_events_transition_key",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := tx.Exec(ctx, `SAVEPOINT webhook_invalid`); err != nil {
					t.Fatal(err)
				}
				_, err := tx.Exec(ctx, test.statement)
				assertOperationsMonitoringPostgresError(
					t,
					err,
					test.code,
					test.constraint,
				)
				if _, rollbackErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT webhook_invalid`); rollbackErr != nil {
					t.Fatal(rollbackErr)
				}
			})
		}
	})
}

func TestWebhookDeliveryMigrationDownTo23RestoresLegacyDeliveries(t *testing.T) {
	pool, ctx := migratedOperationsMonitoring(t)
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 23); err != nil {
		t.Fatal(err)
	}

	var (
		eventTable string
		newColumns int
		oldIndex   int
	)
	if err := pool.QueryRow(ctx, `
SELECT
  COALESCE(to_regclass('public.alert_webhook_events')::text,''),
  (SELECT count(*) FROM information_schema.columns
   WHERE table_schema='public' AND table_name='alert_deliveries'
     AND column_name IN (
       'event_id','delivery_state','scheduled_at',
       'claim_owner','claim_token','claim_expires_at'
     )),
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname='public'
     AND indexname='alert_deliveries_alert_attempt_key')`).
		Scan(&eventTable, &newColumns, &oldIndex); err != nil {
		t.Fatal(err)
	}
	if eventTable != "" || newColumns != 0 || oldIndex != 1 {
		t.Fatalf(
			"event=%q newColumns=%d oldIndex=%d",
			eventTable,
			newColumns,
			oldIndex,
		)
	}
}

const webhookEventFixtureSQL = `
WITH alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    current_value,threshold_value,summary,consecutive_failures,version
  ) VALUES(
    'webhook_migration','processing','warning','open',
    clock_timestamp(),clock_timestamp(),21,20,
    'Processing queue depth is high',2,2
  )
  RETURNING *
)
INSERT INTO alert_webhook_events(
  id,alert_id,transition_kind,alert_version,category,severity,state,
  summary,current_value,threshold_value,first_observed_at,last_observed_at,
  enqueued_at
)
SELECT gen_random_uuid(),id,'opened',version,category,severity,state,summary,
       current_value,threshold_value,first_observed_at,last_observed_at,
       clock_timestamp()
FROM alert`

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
