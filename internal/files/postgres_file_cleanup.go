package files

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"happylearn.local/app/internal/audit"
)

func (s *PostgresStore) ClaimFileCleanup(ctx context.Context, now time.Time, owner string, lease time.Duration) (FileCleanupCandidate, bool, error) {
	if s == nil || s.pool == nil || now.IsZero() || owner == "" || len(owner) > 128 || lease <= 0 {
		return FileCleanupCandidate{}, false, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return FileCleanupCandidate{}, false, err
	}
	defer tx.Rollback(context.Background())
	var candidate FileCleanupCandidate
	var actorID uuid.UUID
	err = tx.QueryRow(ctx, `
SELECT f.id,fv.id,fv.object_key,fv.created_by
FROM files f JOIN file_versions fv ON fv.file_id=f.id
WHERE (f.deleted_at IS NOT NULL
 OR (fv.purpose='qa_attachment' AND NOT EXISTS(SELECT 1 FROM qa_message_files qmf WHERE qmf.file_version_id=fv.id))
 OR (fv.purpose='ai_attachment' AND NOT EXISTS(SELECT 1 FROM ai_message_files amf WHERE amf.file_version_id=fv.id)))
 AND fv.purged_at IS NULL AND fv.retention_until<=$1
 AND (fv.cleanup_state IS NULL OR fv.cleanup_state='pending' OR (fv.cleanup_state='deleting' AND fv.cleanup_lease_until<$1))
 AND NOT EXISTS(SELECT 1 FROM file_processing_jobs j WHERE j.file_version_id=fv.id AND j.state IN ('queued','running'))
 AND NOT EXISTS(SELECT 1 FROM lesson_draft_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id)
 AND NOT EXISTS(SELECT 1 FROM lesson_revision_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id)
ORDER BY fv.retention_until,fv.id
FOR UPDATE OF f,fv SKIP LOCKED LIMIT 1`, now).Scan(&candidate.FileID, &candidate.VersionID, &candidate.OriginalKey, &actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FileCleanupCandidate{}, false, nil
	}
	if err != nil {
		return FileCleanupCandidate{}, false, err
	}
	if err := lockFileVersions(ctx, tx, candidate.FileID); err != nil {
		return FileCleanupCandidate{}, false, err
	}
	referenced, err := fileReferenced(ctx, tx, candidate.FileID)
	if err != nil {
		return FileCleanupCandidate{}, false, err
	}
	if referenced {
		return FileCleanupCandidate{}, false, ErrFileInUse
	}
	rows, err := tx.Query(ctx, `SELECT object_key FROM file_previews WHERE file_version_id=$1 ORDER BY id FOR SHARE`, candidate.VersionID)
	if err != nil {
		return FileCleanupCandidate{}, false, err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return FileCleanupCandidate{}, false, err
		}
		candidate.PreviewKeys = append(candidate.PreviewKeys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return FileCleanupCandidate{}, false, err
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `UPDATE file_versions SET cleanup_state='deleting',cleanup_lease_owner=$2,cleanup_lease_until=$3,cleanup_attempts=cleanup_attempts+1 WHERE id=$1`, candidate.VersionID, owner, now.Add(lease)); err != nil {
		return FileCleanupCandidate{}, false, err
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{ActorUserID: actorID, Action: "file.cleanup_scheduled", TargetType: "file_version", TargetID: candidate.VersionID.String(), Metadata: map[string]any{"previewCount": strconv.Itoa(len(candidate.PreviewKeys))}, RequestID: "maintenance-cleanup", IP: net.ParseIP("127.0.0.1")}); err != nil {
		return FileCleanupCandidate{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox_events(kind,payload)
 SELECT 'file.cleanup',jsonb_build_object('fileId',$1::uuid,'fileVersionId',$2::uuid)
 WHERE NOT EXISTS(SELECT 1 FROM outbox_events WHERE kind='file.cleanup' AND published_at IS NULL AND payload->>'fileVersionId'=$2::text)`, candidate.FileID, candidate.VersionID); err != nil {
		return FileCleanupCandidate{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FileCleanupCandidate{}, false, err
	}
	return candidate, true, nil
}

func (s *PostgresStore) CompleteFileCleanup(ctx context.Context, candidate FileCleanupCandidate, owner string, now time.Time) error {
	if s == nil || s.pool == nil || candidate.FileID == uuid.Nil || candidate.VersionID == uuid.Nil || owner == "" || now.IsZero() {
		return ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var fileID, actorID uuid.UUID
	var objectKey string
	err = tx.QueryRow(ctx, `SELECT file_id,created_by,object_key FROM file_versions WHERE id=$1 AND cleanup_state='deleting' AND cleanup_lease_owner=$2 AND cleanup_lease_until>$3 FOR UPDATE`, candidate.VersionID, owner, now).Scan(&fileID, &actorID, &objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUploadConflict
	}
	if err != nil {
		return err
	}
	if fileID != candidate.FileID || objectKey != candidate.OriginalKey {
		return ErrUploadConflict
	}
	if err := lockFileVersions(ctx, tx, candidate.FileID); err != nil {
		return err
	}
	referenced, err := fileReferenced(ctx, tx, candidate.FileID)
	if err != nil {
		return err
	}
	if referenced {
		return ErrFileInUse
	}
	if _, err := tx.Exec(ctx, `DELETE FROM file_previews WHERE file_version_id=$1`, candidate.VersionID); err != nil {
		return err
	}
	tombstone := "purged/" + candidate.VersionID.String()
	if _, err := tx.Exec(ctx, `UPDATE file_versions SET object_key=$2,display_name='deleted',declared_mime='application/octet-stream',detected_mime=NULL,size_bytes=1,sha256=repeat('0',64),processing_state='failed',scan_result=NULL,browser_playable=false,video_container=NULL,video_codec=NULL,video_duration_ms=NULL,video_width=NULL,video_height=NULL,failure_category='retention_purged',purged_at=$3,cleanup_state='purged',cleanup_lease_owner=NULL,cleanup_lease_until=NULL WHERE id=$1`, candidate.VersionID, tombstone, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE files SET deleted_at=COALESCE(deleted_at,$2) WHERE id=$1`, candidate.FileID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE outbox_events SET published_at=$2 WHERE kind='file.cleanup' AND published_at IS NULL AND payload->>'fileVersionId'=$1::text`, candidate.VersionID, now); err != nil {
		return err
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{ActorUserID: actorID, Action: "file.cleanup_completed", TargetType: "file_version", TargetID: candidate.VersionID.String(), Metadata: map[string]any{}, RequestID: "maintenance-cleanup", IP: net.ParseIP("127.0.0.1")}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ClaimProcessingArtifactCleanup(ctx context.Context, now time.Time, owner string, lease time.Duration) (ProcessingArtifactCleanupCandidate, bool, error) {
	if s == nil || s.pool == nil || now.IsZero() || owner == "" || len(owner) > 128 || lease <= 0 {
		return ProcessingArtifactCleanupCandidate{}, false, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProcessingArtifactCleanupCandidate{}, false, err
	}
	defer tx.Rollback(context.Background())

	var candidate ProcessingArtifactCleanupCandidate
	var versionID, actorID uuid.UUID
	err = tx.QueryRow(ctx, `
SELECT a.id,a.object_key,a.file_version_id,fv.created_by
FROM file_processing_artifacts a
JOIN file_processing_jobs j ON j.id=a.processing_job_id
JOIN file_versions fv ON fv.id=a.file_version_id
WHERE a.cleanup_attempts<1000
  AND (a.cleanup_lease_until IS NULL OR a.cleanup_lease_until<$1)
  AND (
    a.state='delete_pending'
    OR (
      a.state IN ('reserved','stored')
      AND (
        j.state IN ('queued','completed','failed')
        OR (j.state='running' AND (j.attempts>a.attempt_no OR j.lease_until<$1))
      )
    )
  )
ORDER BY a.updated_at,a.id
FOR UPDATE OF a SKIP LOCKED LIMIT 1`, now).Scan(&candidate.ID, &candidate.ObjectKey, &versionID, &actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessingArtifactCleanupCandidate{}, false, nil
	}
	if err != nil {
		return ProcessingArtifactCleanupCandidate{}, false, err
	}
	command, err := tx.Exec(ctx, `
UPDATE file_processing_artifacts
SET state='delete_pending',cleanup_lease_owner=$2,cleanup_lease_until=$3,
    cleanup_attempts=cleanup_attempts+1,updated_at=$4
WHERE id=$1`, candidate.ID, owner, now.Add(lease), now)
	if err != nil {
		return ProcessingArtifactCleanupCandidate{}, false, err
	}
	if command.RowsAffected() != 1 {
		return ProcessingArtifactCleanupCandidate{}, false, ErrUploadConflict
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
		ActorUserID: actorID,
		Action:      "file.processing_artifact_cleanup_scheduled",
		TargetType:  "file_version",
		TargetID:    versionID.String(),
		Metadata:    map[string]any{},
		RequestID:   "maintenance-cleanup",
		IP:          net.ParseIP("127.0.0.1"),
	}); err != nil {
		return ProcessingArtifactCleanupCandidate{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProcessingArtifactCleanupCandidate{}, false, err
	}
	return candidate, true, nil
}

func (s *PostgresStore) CompleteProcessingArtifactCleanup(ctx context.Context, candidate ProcessingArtifactCleanupCandidate, owner string, now time.Time) error {
	if s == nil || s.pool == nil || candidate.ID == uuid.Nil || candidate.ObjectKey == "" || owner == "" || now.IsZero() {
		return ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())

	var versionID, actorID uuid.UUID
	var objectKey string
	err = tx.QueryRow(ctx, `
SELECT a.file_version_id,a.object_key,fv.created_by
FROM file_processing_artifacts a
JOIN file_versions fv ON fv.id=a.file_version_id
WHERE a.id=$1 AND a.state='delete_pending'
  AND a.cleanup_lease_owner=$2 AND a.cleanup_lease_until>$3
FOR UPDATE OF a`, candidate.ID, owner, now).Scan(&versionID, &objectKey, &actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUploadConflict
	}
	if err != nil {
		return err
	}
	if objectKey != candidate.ObjectKey {
		return ErrUploadConflict
	}
	command, err := tx.Exec(ctx, `DELETE FROM file_processing_artifacts WHERE id=$1`, candidate.ID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrUploadConflict
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
		ActorUserID: actorID,
		Action:      "file.processing_artifact_cleanup_completed",
		TargetType:  "file_version",
		TargetID:    versionID.String(),
		Metadata:    map[string]any{},
		RequestID:   "maintenance-cleanup",
		IP:          net.ParseIP("127.0.0.1"),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockFileVersions(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT id FROM file_versions WHERE file_id=$1 ORDER BY id FOR UPDATE`, fileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ignored uuid.UUID
		if err := rows.Scan(&ignored); err != nil {
			return err
		}
	}
	return rows.Err()
}

func fileReferenced(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) (bool, error) {
	var referenced bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lesson_draft_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=$1)
	 OR EXISTS(SELECT 1 FROM lesson_revision_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=$1)
	 OR EXISTS(SELECT 1 FROM qa_message_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=$1)
	 OR EXISTS(SELECT 1 FROM ai_message_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=$1)`, fileID).Scan(&referenced)
	return referenced, err
}

var _ FileCleanupStore = (*PostgresStore)(nil)
var _ ProcessingArtifactCleanupStore = (*PostgresStore)(nil)
