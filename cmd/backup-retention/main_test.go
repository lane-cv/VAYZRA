package main

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func backupSnapshotID(value byte) string {
	return fmt.Sprintf("%064x", value)
}

func TestRetentionPolicyUsesShanghaiCalendarBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	runID := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	policy, err := retentionPolicy(now, runID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Now != now ||
		policy.CurrentRunID != runID ||
		policy.Location == nil ||
		policy.Location.String() != "Asia/Shanghai" ||
		policy.LocalDaily != 7 ||
		policy.RemoteDaily != 30 ||
		policy.RemoteMonthly != 12 ||
		policy.PreReleaseProtectFor != 30*24*time.Hour {
		t.Fatalf("policy=%+v", policy)
	}
}

func retentionRows(
	runID uuid.UUID,
	state backup.State,
	repository backup.Repository,
	snapshotID string,
	requestedAt time.Time,
) []retentionArtifactRow {
	return []retentionArtifactRow{
		{
			RunID: runID, Trigger: backup.TriggerScheduled, State: state,
			RequestedAt: requestedAt, ArtifactPresent: true,
			Kind:       backup.ArtifactDatabaseDump,
			Repository: repository, ArtifactSnapshotID: snapshotID,
			RunSnapshotID: snapshotID,
		},
		{
			RunID: runID, Trigger: backup.TriggerScheduled, State: state,
			RequestedAt: requestedAt, ArtifactPresent: true,
			Kind:       backup.ArtifactObjectSnapshot,
			Repository: repository, ArtifactSnapshotID: snapshotID,
			RunSnapshotID: snapshotID,
		},
		{
			RunID: runID, Trigger: backup.TriggerScheduled, State: state,
			RequestedAt: requestedAt, ArtifactPresent: true,
			Kind:       backup.ArtifactManifest,
			Repository: repository, ArtifactSnapshotID: snapshotID,
			RunSnapshotID: snapshotID,
		},
	}
}

func TestBuildRetentionRepositoryStateRequiresThreeMatchingArtifacts(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	currentRun := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	lastRun := uuid.MustParse("10000000-0000-4000-8000-000000000002")
	evictedRun := uuid.MustParse("10000000-0000-4000-8000-000000000003")
	currentID := backupSnapshotID(1)
	lastID := backupSnapshotID(2)
	evictedID := backupSnapshotID(3)
	rows := append(
		retentionRows(
			currentRun,
			backup.StateVerifying,
			backup.RepositoryLocal,
			currentID,
			now,
		),
		retentionRows(
			lastRun,
			backup.StateSucceeded,
			backup.RepositoryLocal,
			lastID,
			now.Add(-24*time.Hour),
		)...,
	)
	rows = append(rows, retentionRows(
		evictedRun,
		backup.StateDegraded,
		backup.RepositoryLocal,
		evictedID,
		now.Add(-8*24*time.Hour),
	)...)
	candidates := []backup.Artifact{
		{
			BackupRunID: evictedRun, Repository: backup.RepositoryLocal,
			Kind: backup.ArtifactDatabaseDump, SnapshotID: evictedID,
		},
		{
			BackupRunID: evictedRun, Repository: backup.RepositoryLocal,
			Kind: backup.ArtifactObjectSnapshot, SnapshotID: evictedID,
		},
		{
			BackupRunID: evictedRun, Repository: backup.RepositoryLocal,
			Kind: backup.ArtifactManifest, SnapshotID: evictedID,
		},
	}
	got, err := buildRetentionRepositoryState(
		rows,
		candidates,
		backup.RepositoryLocal,
		currentRun,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentSnapshotID != currentID ||
		got.LastGoodSnapshotID != lastID ||
		!slices.Equal(
			got.CandidateSnapshotIDs,
			[]string{evictedID},
		) ||
		!slices.Equal(
			got.CommittedSnapshotIDs,
			[]string{currentID, lastID, evictedID},
		) {
		t.Fatalf("state=%+v", got)
	}

	for name, mutate := range map[string]func([]retentionArtifactRow) []retentionArtifactRow{
		"missing-kind": func(value []retentionArtifactRow) []retentionArtifactRow {
			return value[:len(value)-1]
		},
		"duplicate-kind": func(value []retentionArtifactRow) []retentionArtifactRow {
			value[len(value)-1].Kind = backup.ArtifactDatabaseDump
			return value
		},
		"different-artifact-id": func(value []retentionArtifactRow) []retentionArtifactRow {
			value[len(value)-1].ArtifactSnapshotID = backupSnapshotID(9)
			return value
		},
		"different-run-id": func(value []retentionArtifactRow) []retentionArtifactRow {
			value[len(value)-1].RunSnapshotID = backupSnapshotID(9)
			return value
		},
		"failed-state": func(value []retentionArtifactRow) []retentionArtifactRow {
			for index := range value {
				value[index].State = backup.StateFailed
			}
			return value
		},
		"extra-kind": func(value []retentionArtifactRow) []retentionArtifactRow {
			return append(value, retentionArtifactRow{
				RunID: value[0].RunID, Trigger: value[0].Trigger,
				State: value[0].State, RequestedAt: value[0].RequestedAt,
				Kind:               backup.ArtifactRecoveryReport,
				Repository:         value[0].Repository,
				ArtifactSnapshotID: value[0].ArtifactSnapshotID,
				RunSnapshotID:      value[0].RunSnapshotID,
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := retentionRows(
				currentRun,
				backup.StateVerifying,
				backup.RepositoryLocal,
				currentID,
				now,
			)
			if _, err := buildRetentionRepositoryState(
				mutate(value),
				nil,
				backup.RepositoryLocal,
				currentRun,
			); err == nil {
				t.Fatal("unsafe artifact set accepted")
			}
		})
	}
}

func TestBuildRetentionRepositoryStateRejectsCandidateOutsideSuccessSet(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	currentRun := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	unknownRun := uuid.MustParse("10000000-0000-4000-8000-000000000099")
	if _, err := buildRetentionRepositoryState(
		retentionRows(
			currentRun,
			backup.StateSyncing,
			backup.RepositoryRemote,
			backupSnapshotID(1),
			now,
		),
		[]backup.Artifact{{
			BackupRunID: unknownRun, Repository: backup.RepositoryRemote,
			Kind: backup.ArtifactManifest, SnapshotID: backupSnapshotID(99),
		}},
		backup.RepositoryRemote,
		currentRun,
	); err == nil {
		t.Fatal("candidate outside committed success set accepted")
	}
}

func TestBuildRetentionRepositoryStateRejectsEligibleRunWithoutArtifacts(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	for _, repository := range []backup.Repository{
		backup.RepositoryLocal,
		backup.RepositoryRemote,
	} {
		t.Run(string(repository), func(t *testing.T) {
			currentRun := uuid.MustParse("10000000-0000-4000-8000-000000000001")
			missingRun := uuid.MustParse("10000000-0000-4000-8000-000000000002")
			currentState := backup.StateVerifying
			missingState := backup.StateDegraded
			if repository == backup.RepositoryRemote {
				currentState = backup.StateSyncing
				missingState = backup.StateSucceeded
			}
			rows := retentionRows(
				currentRun,
				currentState,
				repository,
				backupSnapshotID(1),
				now,
			)
			rows = append(rows, retentionArtifactRow{
				RunID: missingRun, Trigger: backup.TriggerScheduled,
				State: missingState, RequestedAt: now.Add(-24 * time.Hour),
				Repository: repository, ArtifactPresent: false,
			})
			if _, err := buildRetentionRepositoryState(
				rows,
				nil,
				repository,
				currentRun,
			); err == nil {
				t.Fatal("eligible terminal run without artifacts was omitted")
			}
		})
	}
}

func TestLoadRetentionArtifactRowsUsesCurrentRemoteArtifactsBeforeCompletion(
	t *testing.T,
) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`TRUNCATE restore_verifications,backup_artifacts,backup_runs CASCADE`,
	); err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	requestedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	localSnapshotID := backupSnapshotID(1)
	remoteSnapshotID := backupSnapshotID(2)
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,started_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  manifest_sha256,logical_bytes,stored_bytes,local_expires_at
) VALUES(
  $1,'current-remote-evidence','manual','syncing',$2::timestamptz,$2::timestamptz,
  20,'age-key-1',$3,decode(repeat('11',32),'hex'),42,21,
  $2::timestamptz+interval '7 days'
)`,
		runID,
		requestedAt,
		localSnapshotID,
	); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []backup.ArtifactKind{
		backup.ArtifactDatabaseDump,
		backup.ArtifactObjectSnapshot,
		backup.ArtifactManifest,
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,
  verified_at,expires_at
) VALUES(
  $1,$2,'remote',$3,decode(repeat('22',32),'hex'),7,
  $4::timestamptz,$4::timestamptz+interval '30 days'
)`,
			runID,
			kind,
			remoteSnapshotID,
			requestedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := loadRetentionArtifactRows(
		ctx,
		pool,
		backup.RepositoryRemote,
		runID,
	)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.ArtifactSnapshotID != remoteSnapshotID ||
			row.RunSnapshotID != remoteSnapshotID {
			t.Fatalf("row=%+v", row)
		}
	}
}

func TestLoadRetentionArtifactRowsIncludesEligibleRunWithoutArtifacts(
	t *testing.T,
) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`TRUNCATE restore_verifications,backup_artifacts,backup_runs CASCADE`,
	); err != nil {
		t.Fatal(err)
	}
	currentRun := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	missingRun := uuid.MustParse("10000000-0000-4000-8000-000000000002")
	requestedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,started_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  remote_snapshot_id,manifest_sha256,logical_bytes,stored_bytes,
  local_expires_at,remote_expires_at
) VALUES
  ($1,'current-zero-artifact-test','manual','syncing',
   $3::timestamptz,$3::timestamptz,NULL,20,'age-key-1',$4,
   NULL,decode(repeat('11',32),'hex'),42,21,
   $3::timestamptz+interval '7 days',NULL),
  ($2,'missing-zero-artifact-test','scheduled','succeeded',
   $3::timestamptz-interval '1 day',$3::timestamptz-interval '1 day',
   $3::timestamptz-interval '1 day',20,'age-key-1',$5,$6,
   decode(repeat('22',32),'hex'),42,21,
   $3::timestamptz+interval '6 days',$3::timestamptz+interval '29 days')`,
		currentRun,
		missingRun,
		requestedAt,
		backupSnapshotID(1),
		backupSnapshotID(2),
		backupSnapshotID(3),
	); err != nil {
		t.Fatal(err)
	}
	rowsToInsert := retentionRows(
		currentRun,
		backup.StateSyncing,
		backup.RepositoryRemote,
		backupSnapshotID(4),
		requestedAt,
	)
	rowsToInsert = append(rowsToInsert, retentionRows(
		missingRun,
		backup.StateSucceeded,
		backup.RepositoryLocal,
		backupSnapshotID(2),
		requestedAt.Add(-24*time.Hour),
	)...)
	for _, row := range rowsToInsert {
		if _, err := pool.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,
  verified_at,expires_at
) VALUES(
  $1,$2,$3,$4,decode(repeat('33',32),'hex'),7,
  $5::timestamptz,$5::timestamptz+interval '7 days'
)`,
			row.RunID,
			row.Kind,
			row.Repository,
			row.ArtifactSnapshotID,
			row.RequestedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := loadRetentionArtifactRows(
		ctx,
		pool,
		backup.RepositoryRemote,
		currentRun,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows=%+v", rows)
	}
	if _, err := buildRetentionRepositoryState(
		rows,
		nil,
		backup.RepositoryRemote,
		currentRun,
	); err == nil {
		t.Fatal("eligible terminal run without artifacts was not fail closed")
	}
}
