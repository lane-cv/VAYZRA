package backup

import (
	"context"
	"errors"
	"fmt"
	"math"
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

func TestPostgresStoreClaimRunByIDNeverMutatesAnUnrelatedRun(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	firstID := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	targetID := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	for index, runID := range []uuid.UUID{firstID, targetID} {
		if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at
) VALUES($1,$2,'scheduled','queued',$3)`,
			runID,
			fmt.Sprintf("target-claim-%d", index),
			time.Date(2026, 7, 28, 1, index, 0, 0, time.UTC),
		); err != nil {
			t.Fatal(err)
		}
	}
	owner := uuid.New()
	claimed, err := store.ClaimRunByID(ctx, targetID, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != targetID ||
		claimed.OwnerID != owner ||
		claimed.LeaseGeneration != 1 {
		t.Fatalf("claimed=%+v", claimed)
	}
	var firstOwner *uuid.UUID
	var firstLease *time.Time
	var firstGeneration int64
	if err := pool.QueryRow(ctx, `
SELECT owner_id,lease_expires_at,lease_generation
FROM backup_runs
WHERE id=$1`, firstID).Scan(
		&firstOwner,
		&firstLease,
		&firstGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if firstOwner != nil || firstLease != nil || firstGeneration != 0 {
		t.Fatalf(
			"unrelated run mutated: owner=%v lease=%v generation=%d",
			firstOwner,
			firstLease,
			firstGeneration,
		)
	}
	if _, err := store.ClaimRunByID(
		ctx,
		uuid.New(),
		uuid.New(),
		time.Minute,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target err=%v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT owner_id,lease_expires_at,lease_generation
FROM backup_runs
WHERE id=$1`, firstID).Scan(
		&firstOwner,
		&firstLease,
		&firstGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if firstOwner != nil || firstLease != nil || firstGeneration != 0 {
		t.Fatalf(
			"missing target claimed unrelated run: owner=%v lease=%v generation=%d",
			firstOwner,
			firstLease,
			firstGeneration,
		)
	}
}

func TestPostgresStoreClaimRunByIDTakesOverOnlyAfterLeaseExpiry(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled,
		IdempotencyKey: "target-takeover-1",
		RequestedAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldOwner, newOwner := uuid.New(), uuid.New()
	oldClaim, err := store.ClaimRunByID(ctx, run.ID, oldOwner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunByID(
		ctx,
		run.ID,
		newOwner,
		time.Hour,
	); !errors.Is(err, ErrActiveClaim) {
		t.Fatalf("live takeover err=%v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE backup_runs
SET lease_expires_at=clock_timestamp()-interval '1 second'
WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	taken, err := store.ClaimRunByID(ctx, run.ID, newOwner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if taken.ID != run.ID ||
		taken.OwnerID != newOwner ||
		taken.LeaseGeneration != oldClaim.LeaseGeneration+1 {
		t.Fatalf("taken=%+v old=%+v", taken, oldClaim)
	}
	if _, err := store.Renew(
		ctx,
		run.ID,
		oldOwner,
		oldClaim.LeaseGeneration,
		time.Minute,
	); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("stale owner renewed after takeover: %v", err)
	}
}

func TestPostgresStoreClaimSchedulesLeaseAfterLockWait(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "claim-clock-wait-1",
		RequestedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM backup_runs WHERE id=$1 FOR UPDATE`, run.ID); err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		run Run
		err error
	}
	result := make(chan claimResult, 1)
	go func() {
		claimed, claimErr := store.Claim(ctx, uuid.New(), time.Second)
		result <- claimResult{run: claimed, err: claimErr}
	}()
	waitForBlockedBackupQuery(t, pool)
	waitForBackupClock(t, pool, postgresBackupClock(t, pool).Add(1100*time.Millisecond))
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case claimed := <-result:
		if claimed.err != nil {
			t.Fatal(claimed.err)
		}
		databaseNow := postgresBackupClock(t, pool)
		if claimed.run.LeaseExpiresAt == nil ||
			!claimed.run.LeaseExpiresAt.After(databaseNow) ||
			claimed.run.LeaseGeneration != 1 {
			t.Fatalf("claimed=%+v database_now=%s", claimed.run, databaseNow)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim did not return after row lock released")
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
	if taken.LeaseGeneration != 2 {
		t.Fatalf("takeover generation=%d want=2", taken.LeaseGeneration)
	}
	if _, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: oldOwner, LeaseGeneration: 1, From: StateQueued,
		To: StateDraining, At: time.Now().UTC(),
	}); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("stale transition err=%v", err)
	}
	if _, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: newOwner, LeaseGeneration: taken.LeaseGeneration, From: StateQueued,
		To: StateDraining, At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRenewRequiresCurrentOwnerGenerationAndUnexpiredLease(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "renew-fence-key-1",
		RequestedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	claimed, err := store.Claim(ctx, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := store.Renew(
		ctx,
		run.ID,
		owner,
		claimed.LeaseGeneration,
		2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.LeaseGeneration != claimed.LeaseGeneration ||
		renewed.OwnerID != owner ||
		renewed.LeaseExpiresAt == nil ||
		claimed.LeaseExpiresAt == nil ||
		!renewed.LeaseExpiresAt.After(*claimed.LeaseExpiresAt) {
		t.Fatalf("claimed=%+v renewed=%+v", claimed, renewed)
	}
	for _, tc := range []struct {
		name       string
		owner      uuid.UUID
		generation int64
	}{
		{name: "wrong owner", owner: uuid.New(), generation: renewed.LeaseGeneration},
		{name: "wrong generation", owner: owner, generation: renewed.LeaseGeneration + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Renew(
				ctx,
				run.ID,
				tc.owner,
				tc.generation,
				time.Minute,
			); !errors.Is(err, ErrStaleOwner) {
				t.Fatalf("renew err=%v", err)
			}
		})
	}
	if _, err := pool.Exec(ctx, `
UPDATE backup_runs
SET lease_expires_at=clock_timestamp()-interval '1 second'
WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Renew(
		ctx,
		run.ID,
		owner,
		renewed.LeaseGeneration,
		time.Minute,
	); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("expired renew err=%v", err)
	}
}

func TestPostgresStoreTransitionAndArtifactFailClosedAfterLeaseExpiresWhileWaiting(t *testing.T) {
	for _, operation := range []string{"transition", "artifact"} {
		t.Run(operation, func(t *testing.T) {
			pool := backupPool(t)
			store := NewPostgresStore(pool)
			ctx := context.Background()
			run, err := store.Create(ctx, CreateInput{
				ID: uuid.New(), Trigger: TriggerScheduled,
				IdempotencyKey: "expiry-wait-" + operation,
				RequestedAt:    time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			claimed, err := store.Claim(ctx, owner, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			if _, err := blocker.Exec(
				ctx,
				`SELECT id FROM backup_runs WHERE id=$1 FOR UPDATE`,
				run.ID,
			); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				switch operation {
				case "transition":
					_, mutationErr := store.Transition(ctx, TransitionInput{
						RunID: run.ID, OwnerID: owner,
						LeaseGeneration: claimed.LeaseGeneration,
						From:            StateQueued, To: StateDraining,
						At: time.Now().UTC(),
					})
					result <- mutationErr
				case "artifact":
					now := time.Now().UTC()
					result <- store.AddArtifact(ctx, Artifact{
						BackupRunID: run.ID, OwnerID: owner,
						LeaseGeneration: claimed.LeaseGeneration,
						Kind:            ArtifactManifest, Repository: RepositoryLocal,
						SnapshotID: "opaque-expired-artifact", SHA256: make([]byte, 32),
						SizeBytes: 42, VerifiedAt: now,
						ExpiresAt: now.Add(7 * 24 * time.Hour),
					})
				}
			}()
			waitForBlockedBackupQuery(t, pool)
			waitForBackupClock(t, pool, claimed.LeaseExpiresAt.Add(20*time.Millisecond))
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrStaleOwner) {
					t.Fatalf("%s err=%v", operation, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s did not return after row lock released", operation)
			}
		})
	}
}

func TestPostgresStoreReconcilesCommittedClaimAfterResponseLoss(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled, IdempotencyKey: "claim-reconcile-1",
		RequestedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	var terminatedPID int
	store.afterCommit = terminatingBackupCommitHook(
		pool,
		"claim",
		&terminatedPID,
	)
	claimed, err := store.Claim(ctx, owner, time.Minute)
	if err != nil {
		t.Fatalf("claim after committed response loss: %v", err)
	}
	if terminatedPID == 0 ||
		claimed.ID != run.ID ||
		claimed.OwnerID != owner ||
		claimed.LeaseGeneration != 1 ||
		claimed.LeaseExpiresAt == nil ||
		!claimed.LeaseExpiresAt.After(postgresBackupClock(t, pool)) {
		t.Fatalf("claimed=%+v terminated_pid=%d", claimed, terminatedPID)
	}
}

func TestPostgresStoreReconcilesCommittedTargetClaimAfterResponseLoss(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	run, err := store.Create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled,
		IdempotencyKey: "target-claim-reconcile-1",
		RequestedAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	var terminatedPID int
	store.afterCommit = terminatingBackupCommitHook(
		pool,
		"claim_by_id",
		&terminatedPID,
	)
	claimed, err := store.ClaimRunByID(ctx, run.ID, owner, time.Minute)
	if err != nil {
		t.Fatalf("target claim after committed response loss: %v", err)
	}
	if terminatedPID == 0 ||
		claimed.ID != run.ID ||
		claimed.OwnerID != owner ||
		claimed.LeaseGeneration != 1 ||
		claimed.LeaseExpiresAt == nil ||
		!claimed.LeaseExpiresAt.After(postgresBackupClock(t, pool)) {
		t.Fatalf("claimed=%+v terminated_pid=%d", claimed, terminatedPID)
	}
}

func TestPostgresStoreReconcilesCommittedTransitionsAfterResponseLoss(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "nonterminal"
		if terminal {
			name = "terminal"
		}
		t.Run(name, func(t *testing.T) {
			pool := backupPool(t)
			store := NewPostgresStore(pool)
			ctx := context.Background()
			run, err := store.Create(ctx, CreateInput{
				ID: uuid.New(), Trigger: TriggerScheduled,
				IdempotencyKey: "transition-reconcile-" + name,
				RequestedAt:    time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			claimed, err := store.Claim(ctx, owner, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			input := TransitionInput{
				RunID: run.ID, OwnerID: owner,
				LeaseGeneration: claimed.LeaseGeneration,
				From:            StateQueued, To: StateDraining,
				At: time.Now().UTC(),
			}
			if terminal {
				for _, transition := range []struct{ from, to State }{
					{StateQueued, StateDraining},
					{StateDraining, StateSnapshotting},
					{StateSnapshotting, StateEncrypting},
					{StateEncrypting, StateVerifying},
					{StateVerifying, StateSyncing},
				} {
					if _, err := store.Transition(ctx, TransitionInput{
						RunID: run.ID, OwnerID: owner,
						LeaseGeneration: claimed.LeaseGeneration,
						From:            transition.from, To: transition.to,
						At: time.Now().UTC(),
					}); err != nil {
						t.Fatalf("%s -> %s: %v", transition.from, transition.to, err)
					}
				}
				logicalBytes, storedBytes := int64(1234), int64(567)
				evidenceNow := time.Now().UTC().Truncate(time.Microsecond)
				remoteExpiry := evidenceNow.Add(30 * 24 * time.Hour)
				input = TransitionInput{
					RunID: run.ID, OwnerID: owner,
					LeaseGeneration: claimed.LeaseGeneration,
					From:            StateSyncing, To: StateDegraded,
					At: time.Now().UTC(),
					Evidence: &RecoveryEvidence{
						DatabaseMigrationVersion: 20,
						EncryptionKeyID:          "age-key-reconcile",
						LocalSnapshotID:          "local-reconcile",
						RemoteSnapshotID:         "remote-reconcile",
						ManifestSHA256:           []byte("01234567890123456789012345678901"),
						LogicalBytes:             &logicalBytes,
						StoredBytes:              &storedBytes,
						LocalExpiresAt:           evidenceNow.Add(7 * 24 * time.Hour),
						RemoteExpiresAt:          &remoteExpiry,
					},
					ErrorCategory: "remote_unavailable",
					ErrorTraceID:  "trace-reconcile-1",
				}
			}
			var terminatedPID int
			store.afterCommit = terminatingBackupCommitHook(
				pool,
				"transition",
				&terminatedPID,
			)
			transitioned, err := store.Transition(ctx, input)
			if err != nil {
				t.Fatalf("transition after committed response loss: %v", err)
			}
			if terminatedPID == 0 ||
				transitioned.State != input.To ||
				transitioned.LeaseGeneration != input.LeaseGeneration ||
				transitioned.ErrorCategory != input.ErrorCategory ||
				transitioned.ErrorTraceID != input.ErrorTraceID {
				t.Fatalf("transitioned=%+v terminated_pid=%d", transitioned, terminatedPID)
			}
			if terminal {
				if transitioned.OwnerID != uuid.Nil ||
					transitioned.LeaseExpiresAt != nil ||
					transitioned.DatabaseMigrationVersion == nil ||
					*transitioned.DatabaseMigrationVersion != input.Evidence.DatabaseMigrationVersion ||
					transitioned.EncryptionKeyID != input.Evidence.EncryptionKeyID ||
					transitioned.LocalSnapshotID != input.Evidence.LocalSnapshotID ||
					transitioned.RemoteSnapshotID != input.Evidence.RemoteSnapshotID ||
					string(transitioned.ManifestSHA256) != string(input.Evidence.ManifestSHA256) ||
					transitioned.LogicalBytes == nil ||
					*transitioned.LogicalBytes != *input.Evidence.LogicalBytes ||
					transitioned.StoredBytes == nil ||
					*transitioned.StoredBytes != *input.Evidence.StoredBytes ||
					transitioned.LocalExpiresAt == nil ||
					!transitioned.LocalExpiresAt.Equal(input.Evidence.LocalExpiresAt) ||
					transitioned.RemoteExpiresAt == nil ||
					!transitioned.RemoteExpiresAt.Equal(*input.Evidence.RemoteExpiresAt) {
					t.Fatalf("terminal evidence mismatch: %+v", transitioned)
				}
			} else if transitioned.OwnerID != owner ||
				transitioned.LeaseExpiresAt == nil ||
				!transitioned.LeaseExpiresAt.After(postgresBackupClock(t, pool)) {
				t.Fatalf("nonterminal transition=%+v", transitioned)
			}
		})
	}
}

func TestPostgresStoreDoesNotReconcileUncommittedClaimOrTransition(t *testing.T) {
	for _, operation := range []string{"claim", "transition"} {
		t.Run(operation, func(t *testing.T) {
			pool := backupPool(t)
			store := NewPostgresStore(pool)
			ctx := context.Background()
			run, err := store.Create(ctx, CreateInput{
				ID: uuid.New(), Trigger: TriggerScheduled,
				IdempotencyKey: "uncommitted-" + operation,
				RequestedAt:    time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			var claimed Run
			if operation == "transition" {
				claimed, err = store.Claim(ctx, owner, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
			}
			store.beforeCommit = func(committedOperation string, _ *pgxpool.Conn) error {
				if committedOperation == operation {
					return errors.New("injected before commit")
				}
				return nil
			}
			switch operation {
			case "claim":
				_, err = store.Claim(ctx, owner, time.Minute)
			case "transition":
				_, err = store.Transition(ctx, TransitionInput{
					RunID: run.ID, OwnerID: owner,
					LeaseGeneration: claimed.LeaseGeneration,
					From:            StateQueued, To: StateDraining,
					At: time.Now().UTC(),
				})
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("%s err=%v", operation, err)
			}
			durable, err := scanRun(pool.QueryRow(ctx, runSelect+`WHERE id=$1`, run.ID))
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "claim":
				if durable.OwnerID != uuid.Nil ||
					durable.LeaseExpiresAt != nil ||
					durable.LeaseGeneration != 0 {
					t.Fatalf("uncommitted claim persisted: %+v", durable)
				}
			case "transition":
				if durable.State != StateQueued ||
					durable.OwnerID != owner ||
					durable.LeaseGeneration != claimed.LeaseGeneration {
					t.Fatalf("uncommitted transition persisted: %+v", durable)
				}
			}
		})
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
	claimed, err := store.Claim(ctx, owner, time.Hour)
	if err != nil {
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
			RunID: run.ID, OwnerID: owner, LeaseGeneration: claimed.LeaseGeneration,
			From: transition.from, To: transition.to, At: now,
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
		RunID: run.ID, OwnerID: owner, LeaseGeneration: claimed.LeaseGeneration,
		From: StateSyncing, To: StateDegraded, At: now, Evidence: &evidence,
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
	claimed, err := store.Claim(ctx, owner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, TransitionInput{
		RunID: run.ID, OwnerID: owner, LeaseGeneration: claimed.LeaseGeneration,
		From: StateQueued, To: StateSnapshotting, At: time.Now().UTC(),
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
		RunID: run.ID, OwnerID: owner, LeaseGeneration: claimed.LeaseGeneration,
		From: StateQueued, To: StateDraining, At: time.Now().UTC(),
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
	oldClaim, err := store.Claim(ctx, oldOwner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	first := Artifact{
		BackupRunID: run.ID, OwnerID: oldOwner, LeaseGeneration: oldClaim.LeaseGeneration,
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
	newClaim, err := store.Claim(ctx, newOwner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stale := first
	stale.Kind = ArtifactDatabaseDump
	stale.SnapshotID = "opaque-artifact-2"
	if err := store.AddArtifact(ctx, stale); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("stale artifact err=%v", err)
	}
	stale.OwnerID = newOwner
	stale.LeaseGeneration = newClaim.LeaseGeneration
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
	claimed, err := store.Claim(ctx, owner, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 2, 0, 0, 123456789, time.UTC)
	artifact := Artifact{
		BackupRunID: run.ID, OwnerID: owner, LeaseGeneration: claimed.LeaseGeneration,
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
  '{"users":0,"sessions":9223372036854775807}'::jsonb,5,0,0,true,90,
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
		detail.RestoreVerifications[0].DatabaseRowCounts["users"] != 0 ||
		detail.RestoreVerifications[0].DatabaseRowCounts["sessions"] != math.MaxInt64 {
		t.Fatalf("detail=%+v", detail)
	}
	if _, err := store.Get(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestPostgresStoreGetFailsClosedOnMalformedRestoreRowCounts(t *testing.T) {
	pool := backupPool(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)
	if _, err := pool.Exec(ctx, `
ALTER TABLE restore_verifications
DROP CONSTRAINT restore_verifications_row_counts_check`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM restore_verifications`); err != nil {
			t.Errorf("delete malformed restore fixtures: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `
ALTER TABLE restore_verifications
ADD CONSTRAINT restore_verifications_row_counts_check
CHECK (happylearn_valid_restore_row_counts(database_row_counts))`); err != nil {
			t.Errorf("restore row-count constraint: %v", err)
		}
	})
	for _, tc := range []struct {
		name, counts string
	}{
		{name: "null", counts: `null`},
		{name: "null member", counts: `{"users":null}`},
		{name: "unknown key", counts: `{"secret_table":1}`},
		{name: "negative", counts: `{"users":-1}`},
		{name: "fraction", counts: `{"users":1.5}`},
		{name: "string", counts: `{"users":"1"}`},
		{name: "boolean", counts: `{"users":true}`},
		{name: "object", counts: `{"users":{}}`},
		{name: "array", counts: `{"users":[]}`},
		{name: "bigint overflow", counts: `{"users":9223372036854775808}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runID := uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,finished_at
) VALUES($1,$2,'manual','failed',clock_timestamp(),clock_timestamp())`,
				runID,
				"malformed-row-counts-"+runID.String(),
			); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO restore_verifications(backup_run_id,database_row_counts)
VALUES($1,$2::jsonb)`, runID, tc.counts); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get(ctx, runID); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("counts=%s err=%v", tc.counts, err)
			}
		})
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
	localCandidates, err := store.RetentionCandidates(ctx, RetentionPolicy{
		Now: now, Location: shanghai, LocalDaily: 7,
		RemoteDaily: 30, RemoteMonthly: 12,
		PreReleaseProtectFor: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	localGot := make(map[uuid.UUID]bool)
	for _, artifact := range localCandidates {
		if artifact.Repository == RepositoryLocal {
			localGot[artifact.BackupRunID] = true
		}
	}
	if !localGot[localIDs[7]] || !localGot[localIDs[8]] {
		t.Errorf("old local candidates missing")
	}
	if localGot[protected] || localGot[localIDs[0]] {
		t.Errorf("protected local point selected")
	}
	if !localGot[degraded] {
		t.Errorf("degraded local point did not participate in retention")
	}
	if _, err := pool.Exec(
		ctx,
		`TRUNCATE restore_verifications,backup_artifacts,backup_runs CASCADE`,
	); err != nil {
		t.Fatal(err)
	}

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

	remoteCandidates, err := store.RetentionCandidates(ctx, RetentionPolicy{
		Now: now, Location: shanghai, LocalDaily: 7,
		RemoteDaily: 30, RemoteMonthly: 12,
		PreReleaseProtectFor: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteGot := make(map[uuid.UUID]bool)
	for _, artifact := range remoteCandidates {
		if artifact.Repository == RepositoryRemote {
			remoteGot[artifact.BackupRunID] = true
		}
	}
	if !remoteGot[remoteDaily[30]] || !remoteGot[remoteDaily[31]] {
		t.Errorf("old remote daily candidates missing")
	}
	for _, candidate := range []uuid.UUID{
		remoteMonthly[0], remoteMonthly[11], remoteMonthly[12],
	} {
		if !remoteGot[candidate] {
			t.Errorf("remote point outside 30 daily/12 monthly union not selected: %s", candidate)
		}
	}
	for _, retained := range remoteMonthly[1:11] {
		if remoteGot[retained] {
			t.Errorf("retained remote monthly selected: %s", retained)
		}
	}
}

func TestPostgresStoreRetentionCountsVerifiedCurrentRunProspectively(t *testing.T) {
	pool := backupPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC)
	historical := make([]uuid.UUID, 0, 7)
	for daysAgo := 1; daysAgo <= 7; daysAgo++ {
		historical = append(historical, insertRetentionPoint(
			t,
			pool,
			now.AddDate(0, 0, -daysAgo),
			TriggerScheduled,
			StateSucceeded,
			RepositoryLocal,
			fmt.Sprintf("prospective-local-%02d", daysAgo),
		))
	}
	current := insertProspectiveRetentionPoint(
		t,
		pool,
		now,
		TriggerScheduled,
		StateVerifying,
		RepositoryLocal,
		fmt.Sprintf("%064x", 999),
	)

	candidates, err := store.RetentionCandidates(ctx, RetentionPolicy{
		Now: now, Location: shanghai, LocalDaily: 7,
		RemoteDaily: 30, RemoteMonthly: 12,
		PreReleaseProtectFor: 30 * 24 * time.Hour,
		CurrentRunID:         current,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[uuid.UUID]bool)
	for _, artifact := range candidates {
		got[artifact.BackupRunID] = true
	}
	if !got[historical[6]] {
		t.Fatalf("oldest success was not evicted with prospective current: %v", sortedUUIDs(got))
	}
	if got[current] {
		t.Fatalf("prospective current was selected: %v", sortedUUIDs(got))
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
	repositories := []Repository{repository}
	if repository == RepositoryRemote {
		repositories = []Repository{RepositoryLocal, RepositoryRemote}
	}
	for _, artifactRepository := range repositories {
		if _, err := pool.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
) VALUES(
  $1,'manifest',$2,$3,decode(repeat('11',32),'hex'),42,$4,$4
)`,
			id, artifactRepository, snapshot, requestedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func insertProspectiveRetentionPoint(
	t *testing.T,
	pool *pgxpool.Pool,
	requestedAt time.Time,
	trigger Trigger,
	state State,
	repository Repository,
	snapshot string,
) uuid.UUID {
	t.Helper()
	if state != StateVerifying && state != StateSyncing {
		t.Fatalf("invalid prospective state=%s", state)
	}
	id := uuid.New()
	ctx := context.Background()
	remoteSnapshot := any(nil)
	if repository == RepositoryRemote {
		remoteSnapshot = snapshot
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  remote_snapshot_id,manifest_sha256,local_expires_at,remote_expires_at
) VALUES(
  $1,$2,$3,$4,$5::timestamptz,NULL,
  20,'age-key-1',$6,$7,decode(repeat('00',32),'hex'),
  $5::timestamptz+interval '7 days',
  CASE WHEN $7::text IS NULL THEN NULL ELSE $5::timestamptz+interval '30 days' END
)`,
		id,
		"retention-"+id.String(),
		trigger,
		state,
		requestedAt,
		snapshot,
		remoteSnapshot,
	); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []ArtifactKind{
		ArtifactDatabaseDump,
		ArtifactObjectSnapshot,
		ArtifactManifest,
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
) VALUES(
  $1,$2,$3,$4,decode(repeat('11',32),'hex'),42,
  $5::timestamptz,$5::timestamptz+interval '30 days'
)`,
			id,
			kind,
			repository,
			snapshot,
			requestedAt,
		); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestPostgresStoreRetentionCandidatesRejectsEligibleRunWithoutArtifacts(
	t *testing.T,
) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	for _, repository := range []Repository{
		RepositoryLocal,
		RepositoryRemote,
	} {
		t.Run(string(repository), func(t *testing.T) {
			pool := backupPool(t)
			store := NewPostgresStore(pool)
			ctx := context.Background()
			currentState := StateVerifying
			missingState := StateDegraded
			var remoteSnapshot any
			if repository == RepositoryRemote {
				currentState = StateSyncing
				missingState = StateSucceeded
				remoteSnapshot = fmt.Sprintf("%064x", 2)
			}
			current := insertProspectiveRetentionPoint(
				t,
				pool,
				now,
				TriggerScheduled,
				currentState,
				repository,
				fmt.Sprintf("%064x", 1),
			)
			missingID := uuid.New()
			if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  remote_snapshot_id,manifest_sha256,local_expires_at,remote_expires_at
) VALUES(
  $1,$2,'scheduled',$3,$4::timestamptz,$4::timestamptz,
  20,'age-key-1',$5,$6,decode(repeat('44',32),'hex'),
  $4::timestamptz+interval '7 days',
  CASE WHEN $6::text IS NULL THEN NULL
       ELSE $4::timestamptz+interval '30 days' END
)`,
				missingID,
				"retention-missing-"+missingID.String(),
				missingState,
				now.Add(-24*time.Hour),
				fmt.Sprintf("%064x", 2),
				remoteSnapshot,
			); err != nil {
				t.Fatal(err)
			}
			if repository == RepositoryRemote {
				for _, kind := range []ArtifactKind{
					ArtifactDatabaseDump,
					ArtifactObjectSnapshot,
					ArtifactManifest,
				} {
					if _, err := pool.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,
  verified_at,expires_at
) VALUES(
  $1,$2,'local',$3,decode(repeat('55',32),'hex'),7,
  $4::timestamptz,$4::timestamptz+interval '7 days'
)`,
						missingID,
						kind,
						fmt.Sprintf("%064x", 2),
						now.Add(-24*time.Hour),
					); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := store.RetentionCandidates(ctx, RetentionPolicy{
				Now: now, Location: time.UTC, CurrentRunID: current,
				LocalDaily: 7, RemoteDaily: 30, RemoteMonthly: 12,
				PreReleaseProtectFor: 30 * 24 * time.Hour,
			})
			if err == nil {
				t.Fatal("eligible terminal run without artifacts was omitted")
			}
		})
	}
}

func sortedUUIDs(values map[uuid.UUID]bool) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id.String())
	}
	sort.Strings(result)
	return result
}

func postgresBackupClock(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC()
}

func waitForBackupClock(t *testing.T, pool *pgxpool.Pool, target time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !postgresBackupClock(t, pool).Before(target) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("database clock did not reach %s: %v", target, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForBlockedBackupQuery(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM pg_stat_activity
  WHERE datname=current_database()
    AND pid<>pg_backend_pid()
    AND state='active'
    AND wait_event_type='Lock'
    AND query LIKE '%backup_runs%'
)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backup query did not block: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func terminatingBackupCommitHook(
	pool *pgxpool.Pool,
	targetOperation string,
	terminatedPID *int,
) func(string, *pgxpool.Conn) error {
	return func(operation string, conn *pgxpool.Conn) error {
		if operation != targetOperation {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(terminatedPID); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, *terminatedPID); err != nil {
			return err
		}
		return errors.New("ambiguous post-commit connection loss")
	}
}

func timePointer(value time.Time) *time.Time { return &value }
