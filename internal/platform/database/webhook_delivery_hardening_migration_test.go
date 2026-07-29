package database_test

import (
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestWebhookDeliveryHardeningMigrationRepairsV24RowsAndAddsConstraints(
	t *testing.T,
) {
	pool, ctx := migratedOperationsMonitoring(t)
	provider, closeProvider := migrationProvider(
		t,
		pool.Config().ConnString(),
	)
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 24); err != nil {
		t.Fatal(err)
	}

	var eventAlertID, mismatchedAlertID uuid.UUID
	for key, target := range map[string]*uuid.UUID{
		"hardening_event_alert":      &eventAlertID,
		"hardening_mismatched_alert": &mismatchedAlertID,
	} {
		if err := pool.QueryRow(ctx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,consecutive_failures,version
) VALUES(
  $1,'processing','warning','open',
  clock_timestamp() - interval '1 minute',clock_timestamp(),
  21,20,'Processing queue depth is high',2,4
)
RETURNING id`, key).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	eventIDs := map[string]uuid.UUID{
		"opened":   uuid.New(),
		"upgraded": uuid.New(),
		"resolved": uuid.New(),
	}
	for kind, values := range map[string]struct {
		severity string
		state    string
		version  int
	}{
		"opened": {
			severity: "warning", state: "resolved", version: 2,
		},
		"upgraded": {
			severity: "warning", state: "resolved", version: 3,
		},
		"resolved": {
			severity: "critical", state: "open", version: 4,
		},
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO alert_webhook_events(
  id,alert_id,transition_kind,alert_version,category,severity,state,
  summary,current_value,threshold_value,first_observed_at,last_observed_at,
  enqueued_at
) VALUES(
  $1,$2,$3,$4,'processing',$5,$6,
  'Processing queue depth is high',21,20,
  clock_timestamp() - interval '1 minute',clock_timestamp(),
  clock_timestamp()
)`,
			eventIDs[kind],
			eventAlertID,
			kind,
			values.version,
			values.severity,
			values.state,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO alert_deliveries(
  id,alert_id,event_id,attempt,destination,delivery_state,scheduled_at
) VALUES(
  gen_random_uuid(),$1,$2,1,'webhook','pending',
  clock_timestamp() - interval '1 second'
)`, mismatchedAlertID, eventIDs["opened"]); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}

	for kind, want := range map[string]struct {
		severity string
		state    string
	}{
		"opened":   {severity: "warning", state: "open"},
		"upgraded": {severity: "critical", state: "open"},
		"resolved": {severity: "critical", state: "resolved"},
	} {
		var severity, state string
		if err := pool.QueryRow(ctx, `
SELECT severity,state
FROM alert_webhook_events
WHERE id=$1`, eventIDs[kind]).Scan(&severity, &state); err != nil {
			t.Fatal(err)
		}
		if severity != want.severity || state != want.state {
			t.Fatalf(
				"kind=%s severity=%q state=%q",
				kind,
				severity,
				state,
			)
		}
	}
	var repairedAlertID uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT alert_id
FROM alert_deliveries
WHERE event_id=$1`, eventIDs["opened"]).Scan(&repairedAlertID); err != nil {
		t.Fatal(err)
	}
	if repairedAlertID != eventAlertID {
		t.Fatalf(
			"delivery alert=%s want=%s",
			repairedAlertID,
			eventAlertID,
		)
	}

	var constraints, indexes int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM pg_constraint
   WHERE conname IN (
     'alert_webhook_events_identity_key',
     'alert_webhook_events_snapshot_check',
     'alert_deliveries_event_alert_fkey'
   )),
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname='public' AND indexname IN (
     'alert_deliveries_effective_due_claim_idx',
     'alert_deliveries_active_event_claim_idx',
     'alert_deliveries_succeeded_event_idx'
   ))`).Scan(&constraints, &indexes); err != nil {
		t.Fatal(err)
	}
	if constraints != 3 || indexes != 3 {
		t.Fatalf("constraints=%d indexes=%d", constraints, indexes)
	}
}

func TestWebhookDeliveryHardeningMigrationRejectsInvalidRelationshipsAndSnapshots(
	t *testing.T,
) {
	pool, ctx := migratedOperationsMonitoring(t)
	var firstAlertID, secondAlertID uuid.UUID
	for key, target := range map[string]*uuid.UUID{
		"hardening_first":  &firstAlertID,
		"hardening_second": &secondAlertID,
	} {
		if err := pool.QueryRow(ctx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,consecutive_failures,version
) VALUES(
  $1,'processing','critical','open',
  clock_timestamp() - interval '1 minute',clock_timestamp(),
  101,100,'Processing queue depth is high',2,5
)
RETURNING id`, key).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	validEventID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO alert_webhook_events(
  id,alert_id,transition_kind,alert_version,category,severity,state,
  summary,current_value,threshold_value,first_observed_at,last_observed_at,
  enqueued_at
) VALUES(
  $1,$2,'upgraded',5,'processing','critical','acknowledged',
  'Processing queue depth is high',101,100,
  clock_timestamp() - interval '1 minute',clock_timestamp(),
  clock_timestamp()
)`, validEventID, firstAlertID); err != nil {
		t.Fatalf("valid acknowledged upgrade: %v", err)
	}
	operationsMonitoringTx(t, ctx, pool, func(tx pgx.Tx) {
		for _, test := range []struct {
			name       string
			statement  string
			code       string
			constraint string
		}{
			{
				name: "opened resolved",
				statement: webhookInvalidSnapshotInsert(
					firstAlertID,
					"opened",
					6,
					"warning",
					"resolved",
				),
				code: "23514", constraint: "alert_webhook_events_snapshot_check",
			},
			{
				name: "upgraded warning",
				statement: webhookInvalidSnapshotInsert(
					firstAlertID,
					"upgraded",
					7,
					"warning",
					"open",
				),
				code: "23514", constraint: "alert_webhook_events_snapshot_check",
			},
			{
				name: "resolved open",
				statement: webhookInvalidSnapshotInsert(
					firstAlertID,
					"resolved",
					8,
					"critical",
					"open",
				),
				code: "23514", constraint: "alert_webhook_events_snapshot_check",
			},
			{
				name: "mismatched delivery alert",
				statement: `
INSERT INTO alert_deliveries(
  id,alert_id,event_id,attempt,destination,delivery_state,scheduled_at
) VALUES(
  gen_random_uuid(),'` + secondAlertID.String() + `','` +
					validEventID.String() + `',1,'webhook','pending',clock_timestamp()
)`,
				code: "23503", constraint: "alert_deliveries_event_alert_fkey",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := tx.Exec(ctx, `SAVEPOINT webhook_hardening_invalid`); err != nil {
					t.Fatal(err)
				}
				_, err := tx.Exec(ctx, test.statement)
				assertOperationsMonitoringPostgresError(
					t,
					err,
					test.code,
					test.constraint,
				)
				if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT webhook_hardening_invalid`); err != nil {
					t.Fatal(err)
				}
			})
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT webhook_hardening_update`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `
UPDATE alert_webhook_events
SET severity='warning'
WHERE id=$1`, validEventID)
		assertOperationsMonitoringPostgresError(
			t,
			err,
			"23514",
			"alert_webhook_events_snapshot_check",
		)
	})
}

func TestWebhookDeliveryHardeningMigrationDownTo24RestoresV24Shape(
	t *testing.T,
) {
	pool, ctx := migratedOperationsMonitoring(t)
	provider, closeProvider := migrationProvider(
		t,
		pool.Config().ConnString(),
	)
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 24); err != nil {
		t.Fatal(err)
	}
	var (
		newConstraints int
		newIndexes     int
		oldForeignKey  int
		oldDueIndex    int
	)
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM pg_constraint
   WHERE conname IN (
     'alert_webhook_events_identity_key',
     'alert_webhook_events_snapshot_check',
     'alert_deliveries_event_alert_fkey'
   )),
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname='public' AND indexname IN (
     'alert_deliveries_effective_due_claim_idx',
     'alert_deliveries_active_event_claim_idx',
     'alert_deliveries_succeeded_event_idx'
   )),
  (SELECT count(*) FROM pg_constraint
   WHERE conname='alert_deliveries_event_id_fkey'),
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname='public'
     AND indexname='alert_deliveries_due_claim_idx')`).
		Scan(
			&newConstraints,
			&newIndexes,
			&oldForeignKey,
			&oldDueIndex,
		); err != nil {
		t.Fatal(err)
	}
	if newConstraints != 0 ||
		newIndexes != 0 ||
		oldForeignKey != 1 ||
		oldDueIndex != 1 {
		t.Fatalf(
			"new constraints=%d new indexes=%d old fk=%d old due=%d",
			newConstraints,
			newIndexes,
			oldForeignKey,
			oldDueIndex,
		)
	}
}

func webhookInvalidSnapshotInsert(
	alertID uuid.UUID,
	kind string,
	version int,
	severity string,
	state string,
) string {
	return `
INSERT INTO alert_webhook_events(
  id,alert_id,transition_kind,alert_version,category,severity,state,
  summary,current_value,threshold_value,first_observed_at,last_observed_at,
  enqueued_at
) VALUES(
  gen_random_uuid(),'` + alertID.String() + `','` + kind + `',` +
		strconv.Itoa(version) + `,'processing','` + severity + `','` +
		state + `','Processing queue depth is high',21,20,
  clock_timestamp() - interval '1 minute',clock_timestamp(),
  clock_timestamp()
)`
}
