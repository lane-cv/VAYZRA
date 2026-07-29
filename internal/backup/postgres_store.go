package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
)

const backupClaimAdvisoryKey int64 = 845103121

type PostgresStore struct {
	pool *pgxpool.Pool

	beforeCommit func(string, *pgxpool.Conn) error
	afterCommit  func(string, *pgxpool.Conn) error
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) commit(
	ctx context.Context,
	operation string,
	tx pgx.Tx,
	conn *pgxpool.Conn,
) error {
	if s.beforeCommit != nil {
		if err := s.beforeCommit(operation, conn); err != nil {
			rollbackBackupTx(tx)
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if s.afterCommit != nil {
		return s.afterCommit(operation, conn)
	}
	return nil
}

func rollbackBackupTx(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Run, error) {
	if s == nil || s.pool == nil || validateCreate(input) != nil {
		return Run{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, backupClaimAdvisoryKey); err != nil {
		return Run{}, ErrUnavailable
	}
	existing, err := scanRun(tx.QueryRow(ctx, runSelect+`
WHERE trigger_kind=$1 AND idempotency_key=$2`,
		input.Trigger, input.IdempotencyKey,
	))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Run{}, ErrUnavailable
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrUnavailable
	}
	var active bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM backup_runs
  WHERE state NOT IN ('succeeded','degraded','failed')
)`).Scan(&active); err != nil {
		return Run{}, ErrUnavailable
	}
	if active {
		return Run{}, ErrAlreadyQueued
	}
	var requestedBy any
	if input.RequestedBy != uuid.Nil {
		requestedBy = input.RequestedBy
	}
	run, err := scanRun(tx.QueryRow(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_by,requested_at
) VALUES($1,$2,$3,'queued',$4,$5)
RETURNING `+runColumns,
		input.ID, input.IdempotencyKey, input.Trigger, requestedBy, input.RequestedAt.UTC(),
	))
	if err != nil {
		return Run{}, mapPostgresWriteError(err)
	}
	if input.Trigger == TriggerManual {
		if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
			ActorUserID: input.RequestedBy,
			Action:      "operations.backup_requested",
			TargetType:  "backup_run",
			TargetID:    run.ID.String(),
			Metadata:    map[string]any{},
			RequestID:   input.RequestID,
			IP:          append([]byte(nil), input.IP...),
		}); err != nil {
			return Run{}, ErrUnavailable
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, ErrUnavailable
	}
	return run, nil
}

func (s *PostgresStore) Claim(
	ctx context.Context,
	owner uuid.UUID,
	lease time.Duration,
) (Run, error) {
	return s.claim(ctx, uuid.Nil, owner, lease, "claim")
}

func (s *PostgresStore) ClaimRunByID(
	ctx context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	lease time.Duration,
) (Run, error) {
	if runID == uuid.Nil {
		return Run{}, ErrInvalid
	}
	return s.claim(ctx, runID, owner, lease, "claim_by_id")
}

func (s *PostgresStore) claim(
	ctx context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	lease time.Duration,
	operation string,
) (Run, error) {
	if s == nil || s.pool == nil || owner == uuid.Nil ||
		lease < time.Second || lease > 24*time.Hour {
		return Run{}, ErrInvalid
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	defer func() {
		if conn != nil {
			conn.Release()
		}
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, backupClaimAdvisoryKey); err != nil {
		return Run{}, ErrUnavailable
	}
	var current Run
	if runID == uuid.Nil {
		current, err = scanRun(tx.QueryRow(ctx, runSelect+`
	WHERE state NOT IN ('succeeded','degraded','failed')
	ORDER BY (owner_id IS NULL),requested_at,id
	FOR UPDATE
	LIMIT 1`))
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrNoClaimableRun
		}
	} else {
		current, err = scanRun(tx.QueryRow(
			ctx,
			runSelect+`WHERE id=$1 FOR UPDATE`,
			runID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrNotFound
		}
	}
	if err != nil {
		return Run{}, ErrUnavailable
	}
	if current.State == StateSucceeded ||
		current.State == StateDegraded ||
		current.State == StateFailed {
		return Run{}, ErrNoClaimableRun
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return Run{}, ErrUnavailable
	}
	if current.OwnerID != uuid.Nil && current.LeaseExpiresAt != nil &&
		current.LeaseExpiresAt.After(databaseNow) {
		return Run{}, ErrActiveClaim
	}
	run, err := scanRun(tx.QueryRow(ctx, `
UPDATE backup_runs
SET owner_id=$2,
    lease_expires_at=$3,
    lease_generation=lease_generation+1,
    started_at=COALESCE(started_at,$4)
WHERE id=$1
RETURNING `+runColumns,
		current.ID, owner, databaseNow.Add(lease), databaseNow,
	))
	if err != nil {
		return Run{}, mapPostgresWriteError(err)
	}
	commitErr := s.commit(ctx, operation, tx, conn)
	if commitErr == nil {
		return run, nil
	}
	rollbackBackupTx(tx)
	conn.Release()
	conn = nil
	reconciled, err := s.reconcileClaim(owner, run)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	return reconciled, nil
}

func (s *PostgresStore) reconcileClaim(owner uuid.UUID, intended Run) (Run, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var durable Run
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var conn *pgxpool.Conn
		conn, err = s.pool.Acquire(ctx)
		if err != nil {
			continue
		}
		durable, err = scanRun(conn.QueryRow(ctx, runSelect+`
	WHERE id=$1
	  AND owner_id=$2
	  AND lease_expires_at > clock_timestamp()
	LIMIT 1`, intended.ID, owner))
		conn.Release()
		if err == nil {
			break
		}
	}
	if err != nil {
		return Run{}, err
	}
	if durable.ID != intended.ID ||
		durable.OwnerID != owner ||
		durable.LeaseGeneration != intended.LeaseGeneration ||
		durable.State != intended.State ||
		!equalTimePointer(durable.StartedAt, intended.StartedAt) ||
		!equalTimePointer(durable.LeaseExpiresAt, intended.LeaseExpiresAt) {
		return Run{}, ErrUnavailable
	}
	return durable, nil
}

func (s *PostgresStore) Renew(
	ctx context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	generation int64,
	lease time.Duration,
) (Run, error) {
	if s == nil || s.pool == nil || runID == uuid.Nil || owner == uuid.Nil ||
		generation < 1 || lease < time.Second || lease > 24*time.Hour {
		return Run{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	current, err := scanRun(tx.QueryRow(
		ctx,
		runSelect+`WHERE id=$1 FOR UPDATE`,
		runID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, ErrUnavailable
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return Run{}, ErrUnavailable
	}
	if current.OwnerID != owner ||
		current.LeaseGeneration != generation ||
		current.LeaseExpiresAt == nil ||
		!current.LeaseExpiresAt.After(databaseNow) {
		return Run{}, ErrStaleOwner
	}
	renewed, err := scanRun(tx.QueryRow(ctx, `
UPDATE backup_runs
SET lease_expires_at=$5
WHERE id=$1
  AND owner_id=$2
  AND lease_generation=$3
  AND state NOT IN ('succeeded','degraded','failed')
  AND lease_expires_at=$4
RETURNING `+runColumns,
		runID, owner, generation, current.LeaseExpiresAt, databaseNow.Add(lease),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrStaleOwner
	}
	if err != nil {
		return Run{}, mapPostgresWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, ErrUnavailable
	}
	return renewed, nil
}

func (s *PostgresStore) Transition(ctx context.Context, input TransitionInput) (Run, error) {
	if s == nil || s.pool == nil ||
		input.RunID == uuid.Nil || input.OwnerID == uuid.Nil ||
		input.LeaseGeneration < 1 ||
		!validState(input.From) || !validState(input.To) ||
		!ValidTransition(input.From, input.To) ||
		input.At.IsZero() || !validSafeError(input.ErrorCategory, input.ErrorTraceID) {
		return Run{}, ErrInvalidTransition
	}
	if (input.To == StateSucceeded || input.To == StateDegraded) &&
		(input.Evidence == nil || validateEvidence(*input.Evidence) != nil) {
		return Run{}, ErrInvalid
	}
	if input.Evidence != nil && validateEvidence(*input.Evidence) != nil {
		return Run{}, ErrInvalid
	}
	if input.To != StateFailed && input.To != StateDegraded &&
		(input.ErrorCategory != "" || input.ErrorTraceID != "") {
		return Run{}, ErrInvalid
	}
	if input.To == StateDegraded && input.ErrorCategory == "" {
		return Run{}, ErrInvalid
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	defer func() {
		if conn != nil {
			conn.Release()
		}
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	current, err := scanRun(tx.QueryRow(
		ctx,
		runSelect+`WHERE id=$1 FOR UPDATE`,
		input.RunID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, ErrUnavailable
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return Run{}, ErrUnavailable
	}
	if current.OwnerID != input.OwnerID ||
		current.LeaseGeneration != input.LeaseGeneration ||
		current.LeaseExpiresAt == nil ||
		!current.LeaseExpiresAt.After(databaseNow) {
		return Run{}, ErrStaleOwner
	}
	if current.State != input.From {
		return Run{}, ErrInvalidTransition
	}

	values := transitionEvidence(input.Evidence)
	terminal := input.To == StateSucceeded || input.To == StateDegraded || input.To == StateFailed
	var finishedAt any
	var leaseOwner any = input.OwnerID
	if terminal {
		finishedAt = input.At.UTC()
		leaseOwner = nil
	}
	run, err := scanRun(tx.QueryRow(ctx, `
UPDATE backup_runs
SET state=$4,
    finished_at=$5::timestamptz,
    database_migration_version=COALESCE($6::bigint,database_migration_version),
    encryption_key_id=COALESCE($7::text,encryption_key_id),
    local_snapshot_id=COALESCE($8::text,local_snapshot_id),
    remote_snapshot_id=COALESCE($9::text,remote_snapshot_id),
    manifest_sha256=COALESCE($10::bytea,manifest_sha256),
    logical_bytes=COALESCE($11::bigint,logical_bytes),
    stored_bytes=COALESCE($12::bigint,stored_bytes),
    local_expires_at=COALESCE($13::timestamptz,local_expires_at),
    remote_expires_at=COALESCE($14::timestamptz,remote_expires_at),
    error_category=$15,
    error_trace_id=$16,
    owner_id=$17::uuid,
    lease_expires_at=CASE WHEN $17::uuid IS NULL THEN NULL ELSE lease_expires_at END
WHERE id=$1
  AND state=$2
  AND owner_id=$3
  AND lease_generation=$18
  AND lease_expires_at=$19
RETURNING `+runColumns,
		input.RunID, input.From, input.OwnerID, input.To, finishedAt,
		values.databaseMigrationVersion, values.encryptionKeyID,
		values.localSnapshotID, values.remoteSnapshotID,
		values.manifestSHA256, values.logicalBytes, values.storedBytes,
		values.localExpiresAt, values.remoteExpiresAt,
		input.ErrorCategory, input.ErrorTraceID, leaseOwner,
		input.LeaseGeneration, current.LeaseExpiresAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrInvalidTransition
	}
	if err != nil {
		return Run{}, mapPostgresWriteError(err)
	}
	commitErr := s.commit(ctx, "transition", tx, conn)
	if commitErr == nil {
		return run, nil
	}
	rollbackBackupTx(tx)
	conn.Release()
	conn = nil
	reconciled, err := s.reconcileTransition(input, run)
	if err != nil {
		return Run{}, ErrUnavailable
	}
	return reconciled, nil
}

func (s *PostgresStore) reconcileTransition(
	input TransitionInput,
	intended Run,
) (Run, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	query := runSelect + `WHERE id=$1`
	args := []any{input.RunID}
	if input.To != StateSucceeded &&
		input.To != StateDegraded &&
		input.To != StateFailed {
		query += `
  AND owner_id=$2
  AND lease_generation=$3
  AND lease_expires_at > clock_timestamp()`
		args = append(args, input.OwnerID, input.LeaseGeneration)
	}
	var durable Run
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var conn *pgxpool.Conn
		conn, err = s.pool.Acquire(ctx)
		if err != nil {
			continue
		}
		durable, err = scanRun(conn.QueryRow(ctx, query, args...))
		conn.Release()
		if err == nil {
			break
		}
	}
	if err != nil {
		return Run{}, err
	}
	if !transitionRunMatches(durable, intended, input) {
		return Run{}, ErrUnavailable
	}
	return durable, nil
}

func transitionRunMatches(durable, intended Run, input TransitionInput) bool {
	if durable.ID != input.RunID ||
		durable.State != input.To ||
		durable.LeaseGeneration != input.LeaseGeneration ||
		durable.ErrorCategory != intended.ErrorCategory ||
		durable.ErrorTraceID != intended.ErrorTraceID ||
		!equalTimePointer(durable.FinishedAt, intended.FinishedAt) ||
		!equalInt64Pointer(
			durable.DatabaseMigrationVersion,
			intended.DatabaseMigrationVersion,
		) ||
		durable.EncryptionKeyID != intended.EncryptionKeyID ||
		durable.LocalSnapshotID != intended.LocalSnapshotID ||
		durable.RemoteSnapshotID != intended.RemoteSnapshotID ||
		!bytes.Equal(durable.ManifestSHA256, intended.ManifestSHA256) ||
		!equalInt64Pointer(durable.LogicalBytes, intended.LogicalBytes) ||
		!equalInt64Pointer(durable.StoredBytes, intended.StoredBytes) ||
		!equalTimePointer(durable.LocalExpiresAt, intended.LocalExpiresAt) ||
		!equalTimePointer(durable.RemoteExpiresAt, intended.RemoteExpiresAt) {
		return false
	}
	terminal := input.To == StateSucceeded ||
		input.To == StateDegraded ||
		input.To == StateFailed
	if terminal {
		return durable.OwnerID == uuid.Nil && durable.LeaseExpiresAt == nil
	}
	return durable.OwnerID == input.OwnerID &&
		equalTimePointer(durable.LeaseExpiresAt, intended.LeaseExpiresAt)
}

func equalTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func equalInt64Pointer(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *PostgresStore) AddArtifact(ctx context.Context, artifact Artifact) error {
	if s == nil || s.pool == nil || validateArtifact(artifact) != nil {
		return ErrInvalid
	}
	artifact.VerifiedAt = artifact.VerifiedAt.UTC().Truncate(time.Microsecond)
	artifact.ExpiresAt = artifact.ExpiresAt.UTC().Truncate(time.Microsecond)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback(ctx)
	var currentOwner *uuid.UUID
	var currentGeneration int64
	var leaseExpiresAt *time.Time
	err = tx.QueryRow(ctx, `
SELECT owner_id,lease_generation,lease_expires_at
FROM backup_runs
WHERE id=$1
FOR UPDATE`, artifact.BackupRunID).Scan(
		&currentOwner,
		&currentGeneration,
		&leaseExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return ErrUnavailable
	}
	if currentOwner == nil || *currentOwner != artifact.OwnerID ||
		currentGeneration != artifact.LeaseGeneration ||
		leaseExpiresAt == nil || !leaseExpiresAt.After(databaseNow) {
		return ErrStaleOwner
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(backup_run_id,kind,repository) DO NOTHING`,
		artifact.BackupRunID, artifact.Kind, artifact.Repository,
		artifact.SnapshotID, artifact.SHA256, artifact.SizeBytes,
		artifact.VerifiedAt.UTC(), artifact.ExpiresAt.UTC(),
	)
	if err != nil {
		return mapPostgresWriteError(err)
	}
	if tag.RowsAffected() == 1 {
		if err := tx.Commit(ctx); err != nil {
			return ErrUnavailable
		}
		return nil
	}
	var existing Artifact
	err = tx.QueryRow(ctx, `
SELECT backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
FROM backup_artifacts
WHERE backup_run_id=$1 AND kind=$2 AND repository=$3`,
		artifact.BackupRunID, artifact.Kind, artifact.Repository,
	).Scan(
		&existing.BackupRunID, &existing.Kind, &existing.Repository,
		&existing.SnapshotID, &existing.SHA256, &existing.SizeBytes,
		&existing.VerifiedAt, &existing.ExpiresAt,
	)
	if err != nil {
		return ErrUnavailable
	}
	if existing.SnapshotID != artifact.SnapshotID ||
		string(existing.SHA256) != string(artifact.SHA256) ||
		existing.SizeBytes != artifact.SizeBytes ||
		!existing.VerifiedAt.Equal(artifact.VerifiedAt) ||
		!existing.ExpiresAt.Equal(artifact.ExpiresAt) {
		return ErrInvalid
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *PostgresStore) List(
	ctx context.Context,
	filter Filter,
) ([]RunSummary, Cursor, error) {
	if s == nil || s.pool == nil || filter.Limit < 1 || filter.Limit > 100 ||
		(filter.Before.RequestedAt.IsZero() != (filter.Before.ID == uuid.Nil)) {
		return nil, Cursor{}, ErrInvalid
	}
	var beforeAt, beforeID any
	if !filter.Before.IsZero() {
		beforeAt = filter.Before.RequestedAt.UTC()
		beforeID = filter.Before.ID
	}
	rows, err := s.pool.Query(ctx, `
SELECT id,trigger_kind,state,requested_at,started_at,finished_at,
       logical_bytes,stored_bytes,local_expires_at,remote_expires_at,error_category
FROM backup_runs
WHERE (
  $1::timestamptz IS NULL
  OR (requested_at,id) < ($1::timestamptz,$2::uuid)
)
ORDER BY requested_at DESC,id DESC
LIMIT $3`,
		beforeAt, beforeID, filter.Limit+1,
	)
	if err != nil {
		return nil, Cursor{}, ErrUnavailable
	}
	defer rows.Close()
	items := make([]RunSummary, 0, filter.Limit+1)
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(
			&item.ID, &item.Trigger, &item.State, &item.RequestedAt,
			&item.StartedAt, &item.FinishedAt, &item.LogicalBytes,
			&item.StoredBytes, &item.LocalExpiresAt, &item.RemoteExpiresAt,
			&item.ErrorCategory,
		); err != nil {
			return nil, Cursor{}, ErrUnavailable
		}
		item.RequestedAt = item.RequestedAt.UTC()
		item.StartedAt = cloneTime(item.StartedAt)
		item.FinishedAt = cloneTime(item.FinishedAt)
		item.LocalExpiresAt = cloneTime(item.LocalExpiresAt)
		item.RemoteExpiresAt = cloneTime(item.RemoteExpiresAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, Cursor{}, ErrUnavailable
	}
	var next Cursor
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
		last := items[len(items)-1]
		next = Cursor{RequestedAt: last.RequestedAt, ID: last.ID}
	}
	return items, next, nil
}

func (s *PostgresStore) Get(ctx context.Context, id uuid.UUID) (RunDetail, error) {
	if s == nil || s.pool == nil || id == uuid.Nil {
		return RunDetail{}, ErrNotFound
	}
	run, err := scanRun(s.pool.QueryRow(ctx, runSelect+`WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, ErrUnavailable
	}
	detail := RunDetail{Run: run, Artifacts: []Artifact{}, RestoreVerifications: []RestoreVerification{}}
	rows, err := s.pool.Query(ctx, `
SELECT backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
FROM backup_artifacts
WHERE backup_run_id=$1
ORDER BY repository,kind`, id)
	if err != nil {
		return RunDetail{}, ErrUnavailable
	}
	for rows.Next() {
		var artifact Artifact
		if err := rows.Scan(
			&artifact.BackupRunID, &artifact.Kind, &artifact.Repository,
			&artifact.SnapshotID, &artifact.SHA256, &artifact.SizeBytes,
			&artifact.VerifiedAt, &artifact.ExpiresAt,
		); err != nil {
			rows.Close()
			return RunDetail{}, ErrUnavailable
		}
		artifact.VerifiedAt = artifact.VerifiedAt.UTC()
		artifact.ExpiresAt = artifact.ExpiresAt.UTC()
		detail.Artifacts = append(detail.Artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RunDetail{}, ErrUnavailable
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `
SELECT id,backup_run_id,state,started_at,finished_at,restored_migration_version,
       database_row_counts,checked_object_count,missing_object_count,
       unexpected_object_count,session_revocation_verified,rto_seconds,
       report_sha256,error_category,error_trace_id
FROM restore_verifications
WHERE backup_run_id=$1
ORDER BY started_at DESC NULLS LAST,id DESC`, id)
	if err != nil {
		return RunDetail{}, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var verification RestoreVerification
		var rowCounts []byte
		if err := rows.Scan(
			&verification.ID, &verification.BackupRunID, &verification.State,
			&verification.StartedAt, &verification.FinishedAt,
			&verification.RestoredMigrationVersion, &rowCounts,
			&verification.CheckedObjectCount, &verification.MissingObjectCount,
			&verification.UnexpectedObjectCount,
			&verification.SessionRevocationVerified, &verification.RTOSeconds,
			&verification.ReportSHA256, &verification.ErrorCategory,
			&verification.ErrorTraceID,
		); err != nil {
			return RunDetail{}, ErrUnavailable
		}
		verification.DatabaseRowCounts, err = decodeRestoreRowCounts(rowCounts)
		if err != nil {
			return RunDetail{}, ErrUnavailable
		}
		verification.StartedAt = cloneTime(verification.StartedAt)
		verification.FinishedAt = cloneTime(verification.FinishedAt)
		detail.RestoreVerifications = append(detail.RestoreVerifications, verification)
	}
	if err := rows.Err(); err != nil {
		return RunDetail{}, ErrUnavailable
	}
	return detail, nil
}

func decodeRestoreRowCounts(raw []byte) (map[string]int64, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return nil, ErrUnavailable
	}
	counts := make(map[string]int64, len(members))
	for table, encoded := range members {
		if _, ok := allowedRestoreRowCountTables[table]; !ok ||
			bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
			return nil, ErrUnavailable
		}
		var count int64
		if err := json.Unmarshal(encoded, &count); err != nil || count < 0 {
			return nil, ErrUnavailable
		}
		counts[table] = count
	}
	return counts, nil
}

func (s *PostgresStore) RetentionCandidates(
	ctx context.Context,
	policy RetentionPolicy,
) ([]Artifact, error) {
	if s == nil || s.pool == nil ||
		policy.Now.IsZero() || policy.Location == nil ||
		policy.LocalDaily < 1 || policy.RemoteDaily < 1 ||
		policy.RemoteMonthly < 1 || policy.PreReleaseProtectFor < 0 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `
SELECT r.id,r.trigger_kind,r.state,r.requested_at,
       a.backup_run_id,a.kind,a.repository,a.snapshot_id,a.sha256,
       a.size_bytes,a.verified_at,a.expires_at
FROM backup_runs r
JOIN backup_artifacts a ON a.backup_run_id=r.id
WHERE (
    a.repository='local' AND r.state IN ('succeeded','degraded')
  )
  OR (
    a.repository='remote' AND r.state='succeeded'
  )
  OR (
    r.id=$1
    AND r.state IN ('verifying','syncing')
    AND (
      a.repository='local'
      OR (
        a.repository='remote'
        AND r.state='syncing'
      )
    )
  )
ORDER BY a.repository,r.requested_at DESC,r.id DESC,a.kind`,
		policy.CurrentRunID,
	)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	points := make([]retentionArtifactPoint, 0)
	for rows.Next() {
		var point retentionArtifactPoint
		if err := rows.Scan(
			&point.runID, &point.trigger, &point.state, &point.requestedAt,
			&point.artifact.BackupRunID, &point.artifact.Kind,
			&point.artifact.Repository, &point.artifact.SnapshotID,
			&point.artifact.SHA256, &point.artifact.SizeBytes,
			&point.artifact.VerifiedAt, &point.artifact.ExpiresAt,
		); err != nil {
			return nil, ErrUnavailable
		}
		point.requestedAt = point.requestedAt.UTC()
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return selectRetentionCandidates(points, policy), nil
}

type transitionValues struct {
	databaseMigrationVersion any
	encryptionKeyID          any
	localSnapshotID          any
	remoteSnapshotID         any
	manifestSHA256           any
	logicalBytes             any
	storedBytes              any
	localExpiresAt           any
	remoteExpiresAt          any
}

func transitionEvidence(evidence *RecoveryEvidence) transitionValues {
	if evidence == nil {
		return transitionValues{}
	}
	var remoteSnapshotID any
	if evidence.RemoteSnapshotID != "" {
		remoteSnapshotID = evidence.RemoteSnapshotID
	}
	return transitionValues{
		databaseMigrationVersion: evidence.DatabaseMigrationVersion,
		encryptionKeyID:          evidence.EncryptionKeyID,
		localSnapshotID:          evidence.LocalSnapshotID,
		remoteSnapshotID:         remoteSnapshotID,
		manifestSHA256:           append([]byte(nil), evidence.ManifestSHA256...),
		logicalBytes:             evidence.LogicalBytes,
		storedBytes:              evidence.StoredBytes,
		localExpiresAt:           evidence.LocalExpiresAt.UTC(),
		remoteExpiresAt:          evidence.RemoteExpiresAt,
	}
}

type rowScanner interface {
	Scan(...any) error
}

const runColumns = `
id,idempotency_key,trigger_kind,state,requested_by,requested_at,
started_at,finished_at,database_migration_version,encryption_key_id,
local_snapshot_id,remote_snapshot_id,manifest_sha256,logical_bytes,
stored_bytes,local_expires_at,remote_expires_at,error_category,error_trace_id,
owner_id,lease_expires_at,lease_generation`

const runSelect = `SELECT ` + runColumns + ` FROM backup_runs `

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var requestedBy, ownerID *uuid.UUID
	var encryptionKeyID, localSnapshotID, remoteSnapshotID *string
	if err := row.Scan(
		&run.ID, &run.IdempotencyKey, &run.Trigger, &run.State,
		&requestedBy, &run.RequestedAt, &run.StartedAt, &run.FinishedAt,
		&run.DatabaseMigrationVersion, &encryptionKeyID,
		&localSnapshotID, &remoteSnapshotID, &run.ManifestSHA256,
		&run.LogicalBytes, &run.StoredBytes, &run.LocalExpiresAt,
		&run.RemoteExpiresAt, &run.ErrorCategory, &run.ErrorTraceID,
		&ownerID, &run.LeaseExpiresAt, &run.LeaseGeneration,
	); err != nil {
		return Run{}, err
	}
	if requestedBy != nil {
		run.RequestedBy = *requestedBy
	}
	if ownerID != nil {
		run.OwnerID = *ownerID
	}
	if encryptionKeyID != nil {
		run.EncryptionKeyID = *encryptionKeyID
	}
	if localSnapshotID != nil {
		run.LocalSnapshotID = *localSnapshotID
	}
	if remoteSnapshotID != nil {
		run.RemoteSnapshotID = *remoteSnapshotID
	}
	return normalizeRun(run), nil
}

func mapPostgresWriteError(err error) error {
	if err == nil {
		return nil
	}
	return ErrUnavailable
}

type retentionArtifactPoint struct {
	runID       uuid.UUID
	trigger     Trigger
	state       State
	requestedAt time.Time
	artifact    Artifact
}

type retentionRun struct {
	runID       uuid.UUID
	trigger     Trigger
	requestedAt time.Time
	repository  Repository
	artifacts   []Artifact
}

func selectRetentionCandidates(
	points []retentionArtifactPoint,
	policy RetentionPolicy,
) []Artifact {
	type runKey struct {
		id         uuid.UUID
		repository Repository
	}
	byRun := make(map[runKey]*retentionRun)
	for _, point := range points {
		key := runKey{id: point.runID, repository: point.artifact.Repository}
		run := byRun[key]
		if run == nil {
			run = &retentionRun{
				runID: point.runID, trigger: point.trigger,
				requestedAt: point.requestedAt, repository: point.artifact.Repository,
			}
			byRun[key] = run
		}
		run.artifacts = append(run.artifacts, point.artifact)
	}
	byRepository := map[Repository][]*retentionRun{
		RepositoryLocal:  {},
		RepositoryRemote: {},
	}
	for _, run := range byRun {
		byRepository[run.repository] = append(byRepository[run.repository], run)
	}
	keep := make(map[runKey]bool)
	for repository, runs := range byRepository {
		sort.Slice(runs, func(i, j int) bool {
			if !runs[i].requestedAt.Equal(runs[j].requestedAt) {
				return runs[i].requestedAt.After(runs[j].requestedAt)
			}
			return runs[i].runID.String() > runs[j].runID.String()
		})
		if len(runs) == 0 {
			continue
		}
		// Every repository keeps its newest valid point even when policy inputs
		// or calendar boundaries later change.
		keep[runKey{id: runs[0].runID, repository: repository}] = true
		dailyLimit := policy.LocalDaily
		if repository == RepositoryRemote {
			dailyLimit = policy.RemoteDaily
		}
		dailySeen := make(map[string]bool)
		dailyKept := 0
		for _, run := range runs {
			day := retentionDay(run.requestedAt, policy.Location)
			if dailySeen[day] {
				continue
			}
			dailySeen[day] = true
			if dailyKept < dailyLimit {
				keep[runKey{id: run.runID, repository: repository}] = true
				dailyKept++
			}
		}
		if repository == RepositoryRemote {
			monthlySeen := make(map[string]bool)
			monthlyKept := 0
			for _, run := range runs {
				month := retentionMonth(run.requestedAt, policy.Location)
				if monthlySeen[month] {
					continue
				}
				monthlySeen[month] = true
				if monthlyKept < policy.RemoteMonthly {
					keep[runKey{id: run.runID, repository: repository}] = true
					monthlyKept++
				}
			}
		}
		for _, run := range runs {
			if run.trigger != TriggerPreRelease {
				continue
			}
			protectionStarts := policy.Now.Add(-policy.PreReleaseProtectFor)
			if !run.requestedAt.Before(protectionStarts) {
				keep[runKey{id: run.runID, repository: repository}] = true
			}
		}
	}
	candidates := make([]Artifact, 0)
	for key, run := range byRun {
		if keep[key] {
			continue
		}
		candidates = append(candidates, run.artifacts...)
	}
	sortArtifacts(candidates)
	return candidates
}

func retentionDay(at time.Time, location *time.Location) string {
	return at.In(location).Format("2006-01-02")
}

func retentionMonth(at time.Time, location *time.Location) string {
	return at.In(location).Format("2006-01")
}

func sortArtifacts(artifacts []Artifact) {
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Repository != artifacts[j].Repository {
			return artifacts[i].Repository < artifacts[j].Repository
		}
		if artifacts[i].BackupRunID != artifacts[j].BackupRunID {
			return artifacts[i].BackupRunID.String() < artifacts[j].BackupRunID.String()
		}
		return artifacts[i].Kind < artifacts[j].Kind
	})
}

var _ Store = (*PostgresStore)(nil)
