package processing

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var stableCategory = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) LeaseNext(ctx context.Context, owner string, now time.Time, duration time.Duration) (Job, error) {
	if s.pool == nil || owner == "" || len(owner) > 128 || duration <= 0 {
		return Job{}, errors.New("invalid lease request")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
WITH exhausted_candidates AS (
  SELECT id FROM file_processing_jobs
  WHERE state='running' AND lease_until<$1 AND attempts>=4
  ORDER BY lease_until,id
  FOR UPDATE SKIP LOCKED
), exhausted AS (
  UPDATE file_processing_jobs j
  SET state='failed',lease_owner=NULL,lease_until=NULL,last_failure_category='lease_expired',updated_at=$1
  FROM exhausted_candidates c WHERE j.id=c.id
  RETURNING j.file_version_id
)
UPDATE file_versions fv
SET processing_state='failed',failure_category='lease_expired'
FROM exhausted WHERE fv.id=exhausted.file_version_id`, now); err != nil {
		return Job{}, err
	}
	var job Job
	err = tx.QueryRow(ctx, `
WITH candidate AS (
  SELECT id FROM file_processing_jobs
  WHERE attempts<4 AND available_at <= $2
    AND (state='queued' OR (state='running' AND lease_until < $2))
  ORDER BY available_at,created_at,id
  FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE file_processing_jobs j
SET state='running',lease_owner=$1,lease_until=$3,attempts=attempts+1,updated_at=$2
FROM candidate
WHERE j.id=candidate.id
RETURNING j.id,j.file_version_id,j.kind,j.state,j.attempts,j.lease_owner,j.lease_until`, owner, now, now.Add(duration)).Scan(
		&job.ID, &job.FileVersionID, &job.Kind, &job.State, &job.Attempts, &job.LeaseOwner, &job.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE file_versions SET processing_state='processing',failure_category=NULL WHERE id=$1 AND processing_state IN ('pending_scan','processing','failed')`, job.FileVersionID)
	if err != nil {
		return Job{}, err
	}
	if tag.RowsAffected() != 1 {
		return Job{}, errors.New("processing file version is not claimable")
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *PostgresStore) Heartbeat(ctx context.Context, id uuid.UUID, owner string, leaseUntil time.Time) error {
	if s.pool == nil || id == uuid.Nil || owner == "" || leaseUntil.IsZero() {
		return errors.New("invalid heartbeat")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE file_processing_jobs SET lease_until=$3,updated_at=now() WHERE id=$1 AND state='running' AND lease_owner=$2 AND lease_until>now() AND $3>now()`, id, owner, leaseUntil)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *PostgresStore) Complete(ctx context.Context, job Job, result Result) error {
	if s.pool == nil || job.ID == uuid.Nil || job.FileVersionID == uuid.Nil || job.LeaseOwner == "" || result.DetectedMIME == "" || result.ScanResult != "clean" {
		return errors.New("invalid completion")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var versionID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT file_version_id FROM file_processing_jobs WHERE id=$1 AND state='running' AND lease_owner=$2 AND lease_until>now() FOR UPDATE`, job.ID, job.LeaseOwner).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	if versionID != job.FileVersionID {
		return ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, `UPDATE file_versions SET processing_state='ready',detected_mime=$2,scan_result=$3,browser_playable=$4,video_container=NULLIF($5,''),video_codec=NULLIF($6,''),video_duration_ms=$7,video_width=$8,video_height=$9,failure_category=NULL WHERE id=$1`, job.FileVersionID, result.DetectedMIME, result.ScanResult, result.BrowserPlayable, result.VideoContainer, result.VideoCodec, result.VideoDurationMS, result.VideoWidth, result.VideoHeight); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE file_processing_jobs SET state='completed',lease_owner=NULL,lease_until=NULL,last_failure_category=NULL,updated_at=now() WHERE id=$1`, job.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Fail(ctx context.Context, job Job, failure Failure) error {
	if s.pool == nil || job.ID == uuid.Nil || job.FileVersionID == uuid.Nil || job.LeaseOwner == "" || !stableCategory.MatchString(failure.Category) || (!failure.Permanent && failure.RetryAt.IsZero()) {
		return errors.New("invalid failure")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var versionID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT file_version_id FROM file_processing_jobs WHERE id=$1 AND state='running' AND lease_owner=$2 AND lease_until>now() FOR UPDATE`, job.ID, job.LeaseOwner).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	if versionID != job.FileVersionID {
		return ErrLeaseLost
	}
	if failure.Permanent || job.Attempts >= MaxAttempts {
		if _, err := tx.Exec(ctx, `UPDATE file_processing_jobs SET state='failed',lease_owner=NULL,lease_until=NULL,last_failure_category=$2,updated_at=now() WHERE id=$1`, job.ID, failure.Category); err != nil {
			return err
		}
		terminalState := "failed"
		var scanResult *string
		if failure.Rejected {
			terminalState = "rejected"
			rejected := "rejected"
			scanResult = &rejected
		}
		if _, err := tx.Exec(ctx, `UPDATE file_versions SET processing_state=$2,scan_result=COALESCE($3,scan_result),failure_category=$4 WHERE id=$1`, job.FileVersionID, terminalState, scanResult, failure.Category); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE file_processing_jobs SET state='queued',available_at=$2,lease_owner=NULL,lease_until=NULL,last_failure_category=$3,updated_at=now() WHERE id=$1`, job.ID, failure.RetryAt, failure.Category); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE file_versions SET processing_state='pending_scan',failure_category=$2 WHERE id=$1`, job.FileVersionID, failure.Category); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
