package backup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresStoreCreateIsIdempotentAndRejectsAnotherActiveRun(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	input := CreateInput{
		ID:      uuid.MustParse("10000000-0000-4000-8000-000000000001"),
		Trigger: TriggerPreRelease, IdempotencyKey: "release-key-0001",
		RequestedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	}
	first, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	retry := input
	retry.ID = uuid.MustParse("10000000-0000-4000-8000-000000000002")
	second, err := store.Create(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != input.ID || second.ID != first.ID {
		t.Fatalf("first=%s retry=%s", first.ID, second.ID)
	}
	_, err = store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerPreRelease, IdempotencyKey: "release-key-0002",
		RequestedAt: input.RequestedAt.Add(time.Minute),
	})
	if !errors.Is(err, ErrAlreadyQueued) {
		t.Fatalf("err=%v want ErrAlreadyQueued", err)
	}
}

func TestPostgresStoreManualCreateAuditsOnceInCreateTransaction(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE audit_logs`); err != nil {
		t.Fatal(err)
	}
	var adminID uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' LIMIT 1`).Scan(&adminID)
	if errors.Is(err, pgx.ErrNoRows) {
		adminID = uuid.New()
		_, err = pool.Exec(ctx, `
INSERT INTO users(id,username,display_name,role,password_hash,must_change_password)
VALUES($1,$2,'Backup Store Admin','admin','hash',false)`,
			adminID, "backup_store_"+adminID.String(),
		)
	}
	if err != nil {
		t.Fatal(err)
	}
	input := CreateInput{
		ID: uuid.New(), Trigger: TriggerManual, IdempotencyKey: "manual-audit-001",
		RequestedBy: adminID, RequestedAt: time.Now().UTC(),
		RequestID: "backup-store-audit", IP: []byte{192, 0, 2, 41},
	}
	run, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	retry := input
	retry.ID = uuid.New()
	retried, err := store.Create(ctx, retry)
	if err != nil || retried.ID != run.ID {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_logs
WHERE action='operations.backup_requested'
  AND target_type='backup_run'
  AND target_id=$1
  AND actor_user_id=$2
  AND request_id=$3`,
		run.ID.String(), adminID, input.RequestID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit count=%d want=1", count)
	}
}

func TestPostgresStoreAllowsOnlyOneActiveClaim(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	_, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "2026-07-28",
		RequestedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	owners := []uuid.UUID{uuid.New(), uuid.New()}
	start := make(chan struct{})
	results := make(chan error, len(owners))
	var wg sync.WaitGroup
	for _, owner := range owners {
		wg.Add(1)
		go func(owner uuid.UUID) {
			defer wg.Done()
			<-start
			_, claimErr := store.Claim(ctx, owner, time.Hour)
			results <- claimErr
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	var claimed, rejected int
	for result := range results {
		switch {
		case result == nil:
			claimed++
		case errors.Is(result, ErrActiveClaim):
			rejected++
		default:
			t.Fatalf("unexpected claim error: %v", result)
		}
	}
	if claimed != 1 || rejected != 1 {
		t.Fatalf("claimed=%d rejected=%d", claimed, rejected)
	}
}

func TestPostgresStoreLeaseTakeoverRejectsStaleOwner(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "takeover-key-01",
		RequestedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldOwner, newOwner := uuid.New(), uuid.New()
	if _, err := store.Claim(ctx, oldOwner, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE backup_runs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`,
		run.ID,
	); err != nil {
		t.Fatal(err)
	}
	taken, err := store.Claim(ctx, newOwner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if taken.ID != run.ID || taken.OwnerID != newOwner {
		t.Fatalf("taken=%+v", taken)
	}
	if _, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: oldOwner, From: StateQueued,
		To: StateDraining, At: time.Now().UTC(),
	}); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("stale transition err=%v", err)
	}
	if _, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: newOwner, From: StateQueued,
		To: StateDraining, At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreTransitionsTerminalEvidenceAndClearsLease(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "terminal-key-01",
		RequestedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	if _, err := store.Claim(ctx, owner, time.Hour); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	for _, transition := range []struct{ from, to State }{
		{StateQueued, StateDraining},
		{StateDraining, StateSnapshotting},
		{StateSnapshotting, StateEncrypting},
		{StateEncrypting, StateVerifying},
		{StateVerifying, StateSyncing},
	} {
		if _, err := store.Transition(ctx, TransitionInput{
			RunID: run.ID, OwnerID: owner, From: transition.from,
			To: transition.to, At: now,
		}); err != nil {
			t.Fatalf("%s -> %s: %v", transition.from, transition.to, err)
		}
	}
	evidence := RecoveryEvidence{
		DatabaseMigrationVersion: 20,
		EncryptionKeyID:          "age-key-2026-07",
		LocalSnapshotID:          "0123456789abcdef",
		RemoteSnapshotID:         "fedcba9876543210",
		ManifestSHA256:           make([]byte, 32),
		LocalExpiresAt:           now.Add(7 * 24 * time.Hour),
		RemoteExpiresAt:          timePointer(now.Add(30 * 24 * time.Hour)),
	}
	terminal, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: owner, From: StateSyncing,
		To: StateDegraded, At: now, Evidence: &evidence,
		ErrorCategory: "remote_unavailable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.State != StateDegraded || terminal.FinishedAt == nil ||
		terminal.OwnerID != uuid.Nil || terminal.LeaseExpiresAt != nil ||
		terminal.LocalSnapshotID != evidence.LocalSnapshotID ||
		len(terminal.ManifestSHA256) != 32 ||
		terminal.ErrorCategory != "remote_unavailable" {
		t.Fatalf("terminal=%+v", terminal)
	}
}

func TestPostgresStoreRejectsForbiddenSkipAndExpiredTransition(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "forbidden-key-1",
		RequestedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	if _, err := store.Claim(ctx, owner, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: owner, From: StateQueued,
		To: StateSnapshotting, At: time.Now().UTC(),
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skip err=%v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE backup_runs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`,
		run.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: owner, From: StateQueued,
		To: StateDraining, At: time.Now().UTC(),
	}); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("expired err=%v", err)
	}
}

func TestPostgresStoreArtifactWritesRequireCurrentUnexpiredOwner(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "artifact-owner-1",
		RequestedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldOwner, newOwner := uuid.New(), uuid.New()
	if _, err := store.Claim(ctx, oldOwner, time.Hour); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	first := Artifact{
		BackupRunID: run.ID, OwnerID: oldOwner,
		Kind: ArtifactManifest, Repository: RepositoryLocal,
		SnapshotID: "opaque-artifact-1", SHA256: make([]byte, 32),
		SizeBytes: 42, VerifiedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	if err := store.AddArtifact(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE backup_runs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`,
		run.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, newOwner, time.Hour); err != nil {
		t.Fatal(err)
	}
	stale := first
	stale.Kind = ArtifactDatabaseDump
	stale.SnapshotID = "opaque-artifact-2"
	if err := store.AddArtifact(ctx, stale); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("stale artifact err=%v", err)
	}
	stale.OwnerID = newOwner
	if err := store.AddArtifact(ctx, stale); err != nil {
		t.Fatalf("current owner artifact: %v", err)
	}
}

func TestPostgresStoreArtifactRetryPreservesIdempotencyAcrossDatabaseTimePrecision(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "artifact-time-precision-1",
		RequestedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	if _, err := store.Claim(ctx, owner, time.Hour); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 2, 0, 0, 123456789, time.UTC)
	artifact := Artifact{
		BackupRunID: run.ID, OwnerID: owner,
		Kind: ArtifactManifest, Repository: RepositoryLocal,
		SnapshotID: "opaque-artifact-time", SHA256: make([]byte, 32),
		SizeBytes: 42, VerifiedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	if err := store.AddArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	if err := store.AddArtifact(ctx, artifact); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
}

func TestPostgresStoreListUsesRequestedAtAndIDKeyset(t *testing.T) {
	pool := backupPool(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)
	at := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	ids := []uuid.UUID{
		uuid.MustParse("10000000-0000-4000-8000-000000000001"),
		uuid.MustParse("10000000-0000-4000-8000-000000000002"),
		uuid.MustParse("10000000-0000-4000-8000-000000000003"),
	}
	for i, id := range ids {
		if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(id,idempotency_key,trigger_kind,state,requested_at,finished_at)
VALUES($1,$2,'manual','failed',$3,$3)`,
			id, fmt.Sprintf("list-key-%04d", i), at,
		); err != nil {
			t.Fatal(err)
		}
	}
	first, next, err := store.List(ctx, Filter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != ids[2] || first[1].ID != ids[1] ||
		next.ID != ids[1] || !next.RequestedAt.Equal(at) {
		t.Fatalf("first=%+v next=%+v", first, next)
	}
	second, next, err := store.List(ctx, Filter{Limit: 2, Before: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != ids[0] || !next.IsZero() {
		t.Fatalf("second=%+v next=%+v", second, next)
	}
}

func TestPostgresStoreGetReturnsArtifactsAndRestoreVerifications(t *testing.T) {
	pool := backupPool(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)
	runID := uuid.New()
	at := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(id,idempotency_key,trigger_kind,state,requested_at,finished_at)
VALUES($1,'detail-key-001','manual','failed',$2,$2)`,
		runID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
) VALUES(
  $1,'manifest','local','opaque-snapshot',decode(repeat('00',32),'hex'),42,
  $2::timestamptz,$2::timestamptz+interval '7 days'
)`,
		runID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO restore_verifications(
  id,backup_run_id,state,started_at,finished_at,restored_migration_version,database_row_counts,
  checked_object_count,missing_object_count,unexpected_object_count,
  session_revocation_verified,rto_seconds,report_sha256
) VALUES(
  gen_random_uuid(),$1,'succeeded',$2,$2,20,
  '{"users":3,"secret_table":99}'::jsonb,5,0,0,true,90,
  decode(repeat('11',32),'hex')
)`, runID, at); err != nil {
		t.Fatal(err)
	}
	detail, err := store.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.ID != runID || len(detail.Artifacts) != 1 ||
		detail.Artifacts[0].SnapshotID != "opaque-snapshot" ||
		len(detail.RestoreVerifications) != 1 ||
		detail.RestoreVerifications[0].DatabaseRowCounts["users"] != 3 {
		t.Fatalf("detail=%+v", detail)
	}
	if _, err := store.Get(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestPostgresStoreRetentionLocalSevenRemoteThirtyAndTwelveMonthly(t *testing.T) {
	pool := backupPool(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)
	now := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)

	localIDs := make([]uuid.UUID, 0, 9)
	for daysAgo := 0; daysAgo < 9; daysAgo++ {
		localIDs = append(localIDs, insertRetentionPoint(
			t, pool, now.AddDate(0, 0, -daysAgo), TriggerScheduled,
			StateSucceeded, RepositoryLocal, fmt.Sprintf("local-%02d", daysAgo),
		))
	}
	protected := insertRetentionPoint(
		t, pool, now.AddDate(0, 0, -20), TriggerPreRelease,
		StateSucceeded, RepositoryLocal, "local-pre-release",
	)
	degraded := insertRetentionPoint(
		t, pool, now.AddDate(0, 0, -10), TriggerManual,
		StateDegraded, RepositoryLocal, "local-degraded",
	)

	remoteDaily := make([]uuid.UUID, 0, 32)
	for daysAgo := 0; daysAgo < 32; daysAgo++ {
		remoteDaily = append(remoteDaily, insertRetentionPoint(
			t, pool, now.AddDate(0, 0, -daysAgo), TriggerScheduled,
			StateSucceeded, RepositoryRemote, fmt.Sprintf("remote-daily-%02d", daysAgo),
		))
	}
	remoteMonthly := make([]uuid.UUID, 0, 13)
	for monthsAgo := 1; monthsAgo <= 13; monthsAgo++ {
		remoteMonthly = append(remoteMonthly, insertRetentionPoint(
			t, pool, now.AddDate(0, -monthsAgo, 0), TriggerScheduled,
			StateSucceeded, RepositoryRemote, fmt.Sprintf("remote-monthly-%02d", monthsAgo),
		))
	}

	candidates, err := store.RetentionCandidates(ctx, RetentionPolicy{
		Now: now, Location: shanghai, LocalDaily: 7,
		RemoteDaily: 30, RemoteMonthly: 12,
		PreReleaseProtectFor: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[uuid.UUID]bool)
	for _, artifact := range candidates {
		got[artifact.BackupRunID] = true
	}
	if !got[localIDs[7]] || !got[localIDs[8]] {
		t.Errorf("old local candidates missing: got=%v", sortedUUIDs(got))
	}
	if got[protected] || got[localIDs[0]] {
		t.Errorf("protected local point selected: got=%v", sortedUUIDs(got))
	}
	if !got[degraded] {
		t.Errorf("degraded local point did not participate in retention: got=%v", sortedUUIDs(got))
	}
	if !got[remoteDaily[30]] || !got[remoteDaily[31]] {
		t.Errorf("old remote daily candidates missing: got=%v", sortedUUIDs(got))
	}
	for _, candidate := range []uuid.UUID{
		remoteMonthly[0], remoteMonthly[11], remoteMonthly[12],
	} {
		if !got[candidate] {
			t.Errorf("remote point outside 30 daily/12 monthly union not selected: %s", candidate)
		}
	}
	for _, retained := range remoteMonthly[1:11] {
		if got[retained] {
			t.Errorf("retained remote monthly selected: %s", retained)
		}
	}
}

func backupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`TRUNCATE restore_verifications,backup_artifacts,backup_runs CASCADE`,
	); err != nil {
		t.Fatal(err)
	}
	return pool
}

func insertRetentionPoint(
	t *testing.T,
	pool *pgxpool.Pool,
	requestedAt time.Time,
	trigger Trigger,
	state State,
	repository Repository,
	snapshot string,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  manifest_sha256,local_expires_at
) VALUES(
  $1,$2,$3,$4,$5::timestamptz,$5::timestamptz,20,'age-key-1','local-evidence',
  decode(repeat('00',32),'hex'),$5::timestamptz+interval '7 days'
)`,
		id, "retention-"+id.String(), trigger, state, requestedAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
) VALUES(
  $1,'manifest',$2,$3,decode(repeat('11',32),'hex'),42,$4,$4
)`,
		id, repository, snapshot, requestedAt,
	); err != nil {
		t.Fatal(err)
	}
	return id
}

func sortedUUIDs(values map[uuid.UUID]bool) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id.String())
	}
	sort.Strings(result)
	return result
}

func timePointer(value time.Time) *time.Time { return &value }
