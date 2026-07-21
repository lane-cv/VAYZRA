package files

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"happylearn.local/app/internal/audit"
)

func (s *PostgresStore) ListFiles(ctx context.Context, filter FileFilter, cursor Cursor) (FilePage, error) {
	var afterTime any
	var afterID any
	if !cursor.AfterCreatedAt.IsZero() {
		afterTime, afterID = cursor.AfterCreatedAt, cursor.AfterID
	}
	rows, err := s.pool.Query(ctx, `
WITH latest AS (
 SELECT DISTINCT ON (file_id) id,file_id,version,display_name,declared_mime,COALESCE(detected_mime,'') AS detected_mime,size_bytes,processing_state,
  COALESCE(failure_category,'') AS failure_category,browser_playable,created_at,retention_until
 FROM file_versions ORDER BY file_id,version DESC,id DESC
)
SELECT f.id,f.created_at,f.deleted_at,
 v.id,v.file_id,v.version,v.display_name,v.declared_mime,v.detected_mime,v.size_bytes,v.processing_state,v.failure_category,
 COALESCE((SELECT processing_state FROM file_previews WHERE file_version_id=v.id ORDER BY created_at DESC,id DESC LIMIT 1),''),
 v.browser_playable,v.created_at,v.retention_until,
 (SELECT count(*) FROM (
   SELECT lesson_id FROM lesson_draft_files WHERE file_version_id IN (SELECT id FROM file_versions WHERE file_id=f.id)
   UNION ALL
   SELECT revision_id FROM lesson_revision_files WHERE file_version_id IN (SELECT id FROM file_versions WHERE file_id=f.id)
  ) refs)
FROM files f JOIN latest v ON v.file_id=f.id
WHERE f.deleted_at IS NULL
 AND ($1='' OR strpos(lower(v.display_name),$1)>0)
 AND ($2='' OR CASE $2
   WHEN 'document' THEN COALESCE(NULLIF(v.detected_mime,''),v.declared_mime)='application/pdf'
   WHEN 'image' THEN COALESCE(NULLIF(v.detected_mime,''),v.declared_mime) LIKE 'image/%'
   WHEN 'office' THEN COALESCE(NULLIF(v.detected_mime,''),v.declared_mime) LIKE 'application/vnd.openxmlformats-officedocument.%'
   WHEN 'video' THEN COALESCE(NULLIF(v.detected_mime,''),v.declared_mime) LIKE 'video/%'
   WHEN 'text' THEN COALESCE(NULLIF(v.detected_mime,''),v.declared_mime) IN ('text/plain','text/markdown')
   ELSE false END)
 AND ($3='' OR v.processing_state=$3)
 AND ($4='' OR
   ($4='referenced' AND (EXISTS(SELECT 1 FROM lesson_draft_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id) OR EXISTS(SELECT 1 FROM lesson_revision_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id))) OR
   ($4='unreferenced' AND NOT EXISTS(SELECT 1 FROM lesson_draft_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id) AND NOT EXISTS(SELECT 1 FROM lesson_revision_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id)) OR
   ($4='draft' AND EXISTS(SELECT 1 FROM lesson_draft_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id)) OR
   ($4='published' AND EXISTS(SELECT 1 FROM lesson_revision_files b JOIN file_versions x ON x.id=b.file_version_id WHERE x.file_id=f.id)))
 AND ($5::timestamptz IS NULL OR f.created_at >= $5)
 AND ($6::timestamptz IS NULL OR f.created_at <= $6)
 AND ($7::timestamptz IS NULL OR (f.created_at,f.id) < ($7,$8))
ORDER BY f.created_at DESC,f.id DESC LIMIT $9`, filter.Name, filter.Type, filter.State, filter.Reference, filter.CreatedFrom, filter.CreatedTo, afterTime, afterID, cursor.Limit+1)
	if err != nil {
		return FilePage{}, err
	}
	defer rows.Close()
	items := make([]FileListItem, 0, cursor.Limit+1)
	for rows.Next() {
		var item FileListItem
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.DeletedAt,
			&item.Latest.ID, &item.Latest.FileID, &item.Latest.Version, &item.Latest.DisplayName, &item.Latest.DeclaredMIME, &item.Latest.DetectedMIME,
			&item.Latest.Size, &item.Latest.ProcessingState, &item.Latest.FailureCategory, &item.Latest.PreviewState, &item.Latest.BrowserPlayable,
			&item.Latest.CreatedAt, &item.Latest.RetentionUntil, &item.References); err != nil {
			return FilePage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return FilePage{}, err
	}
	page := FilePage{Items: items}
	if len(page.Items) > cursor.Limit {
		page.Items = page.Items[:cursor.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeFileCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *PostgresStore) FileDetail(ctx context.Context, fileID uuid.UUID) (FileDetail, error) {
	var detail FileDetail
	if err := s.pool.QueryRow(ctx, `SELECT id,created_at,deleted_at FROM files WHERE id=$1 AND deleted_at IS NULL`, fileID).Scan(&detail.ID, &detail.CreatedAt, &detail.DeletedAt); err != nil {
		return FileDetail{}, mapStoreError(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT fv.id,fv.file_id,fv.version,fv.display_name,fv.declared_mime,COALESCE(fv.detected_mime,''),fv.size_bytes,fv.processing_state,COALESCE(fv.failure_category,''),COALESCE((SELECT processing_state FROM file_previews WHERE file_version_id=fv.id ORDER BY created_at DESC,id DESC LIMIT 1),''),fv.browser_playable,fv.created_at,fv.retention_until FROM file_versions fv WHERE fv.file_id=$1 ORDER BY fv.version DESC,fv.id DESC`, fileID)
	if err != nil {
		return FileDetail{}, err
	}
	for rows.Next() {
		var version FileVersionDetail
		if err := scanFileVersion(rows, &version); err != nil {
			rows.Close()
			return FileDetail{}, err
		}
		detail.Versions = append(detail.Versions, version)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return FileDetail{}, err
	}
	rows.Close()
	refs, err := s.pool.Query(ctx, `
SELECT DISTINCT 'draft',d.lesson_id,d.title,NULL::uuid FROM lesson_draft_files b JOIN file_versions fv ON fv.id=b.file_version_id JOIN lesson_drafts d ON d.lesson_id=b.lesson_id WHERE fv.file_id=$1
UNION
SELECT DISTINCT 'published',r.lesson_id,r.title,r.id FROM lesson_revision_files b JOIN file_versions fv ON fv.id=b.file_version_id JOIN lesson_revisions r ON r.id=b.revision_id WHERE fv.file_id=$1
ORDER BY 1,3,2`, fileID)
	if err != nil {
		return FileDetail{}, err
	}
	defer refs.Close()
	for refs.Next() {
		var ref FileReference
		var revisionID *uuid.UUID
		if err := refs.Scan(&ref.Kind, &ref.LessonID, &ref.LessonTitle, &revisionID); err != nil {
			return FileDetail{}, err
		}
		if revisionID != nil {
			ref.RevisionID = *revisionID
		}
		detail.References = append(detail.References, ref)
	}
	return detail, refs.Err()
}

func (s *PostgresStore) FileVersion(ctx context.Context, id uuid.UUID) (FileVersionDetail, error) {
	var version FileVersionDetail
	err := scanFileVersion(s.pool.QueryRow(ctx, `SELECT fv.id,fv.file_id,fv.version,fv.display_name,fv.declared_mime,COALESCE(fv.detected_mime,''),fv.size_bytes,fv.processing_state,COALESCE(fv.failure_category,''),COALESCE((SELECT processing_state FROM file_previews WHERE file_version_id=fv.id ORDER BY created_at DESC,id DESC LIMIT 1),''),fv.browser_playable,fv.created_at,fv.retention_until FROM file_versions fv JOIN files f ON f.id=fv.file_id WHERE fv.id=$1 AND f.deleted_at IS NULL`, id), &version)
	return version, mapStoreError(err)
}

func (s *PostgresStore) RetryFile(ctx context.Context, actor Principal, versionID uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var state, category string
	if err := tx.QueryRow(ctx, `SELECT processing_state,COALESCE(failure_category,'') FROM file_versions WHERE id=$1 FOR UPDATE`, versionID).Scan(&state, &category); err != nil {
		return mapStoreError(err)
	}
	if state != "failed" || !retryableFailure(category) {
		return ErrFileNotRetryable
	}
	if _, err := tx.Exec(ctx, `INSERT INTO file_processing_jobs(file_version_id,kind) VALUES($1,'process_file')`, versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE file_versions SET processing_state='pending_scan',failure_category=NULL WHERE id=$1`, versionID); err != nil {
		return err
	}
	if err := writeFileAudit(ctx, tx, actor, "file.processing_retried", "file_version", versionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ReplaceFile(ctx context.Context, actor Principal, fileID, uploadedVersionID uuid.UUID, retentionUntil time.Time) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var sourceFileID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT file_id FROM file_versions WHERE id=$1`, uploadedVersionID).Scan(&sourceFileID); err != nil {
		return mapStoreError(err)
	}
	if sourceFileID == fileID {
		return ErrInvalid
	}
	if err := lockDraftBindingsForFile(ctx, tx, fileID); err != nil {
		return err
	}
	fileLocks, err := tx.Query(ctx, `SELECT id FROM files WHERE id=ANY($1) AND deleted_at IS NULL ORDER BY id FOR UPDATE`, []uuid.UUID{fileID, sourceFileID})
	if err != nil {
		return err
	}
	lockedFiles := 0
	for fileLocks.Next() {
		var ignored uuid.UUID
		if err := fileLocks.Scan(&ignored); err != nil {
			fileLocks.Close()
			return err
		}
		lockedFiles++
	}
	if err := fileLocks.Err(); err != nil {
		fileLocks.Close()
		return err
	}
	fileLocks.Close()
	if lockedFiles != 2 {
		return ErrNotFound
	}
	versionLocks, err := tx.Query(ctx, `SELECT id FROM file_versions WHERE file_id=ANY($1) ORDER BY id FOR UPDATE`, []uuid.UUID{fileID, sourceFileID})
	if err != nil {
		return err
	}
	for versionLocks.Next() {
		var ignored uuid.UUID
		if err := versionLocks.Scan(&ignored); err != nil {
			versionLocks.Close()
			return err
		}
	}
	if err := versionLocks.Err(); err != nil {
		versionLocks.Close()
		return err
	}
	versionLocks.Close()
	var sourceVersion int64
	var lockedSourceFileID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT file_id,version FROM file_versions WHERE id=$1`, uploadedVersionID).Scan(&lockedSourceFileID, &sourceVersion); err != nil {
		return mapStoreError(err)
	}
	if lockedSourceFileID != sourceFileID || sourceVersion != 1 {
		return ErrInvalid
	}
	var sourceVersions int
	var referenced bool
	if err := tx.QueryRow(ctx, `SELECT count(*),EXISTS(SELECT 1 FROM lesson_draft_files WHERE file_version_id=$1) OR EXISTS(SELECT 1 FROM lesson_revision_files WHERE file_version_id=$1) FROM file_versions WHERE file_id=$2`, uploadedVersionID, sourceFileID).Scan(&sourceVersions, &referenced); err != nil {
		return err
	}
	if sourceVersions != 1 || referenced {
		return ErrFileInUse
	}
	var previousVersionID uuid.UUID
	var previousVersion int64
	if err := tx.QueryRow(ctx, `SELECT id,version FROM file_versions WHERE file_id=$1 ORDER BY version DESC,id DESC LIMIT 1 FOR UPDATE`, fileID).Scan(&previousVersionID, &previousVersion); err != nil {
		return err
	}
	nextVersion := previousVersion + 1
	if _, err := tx.Exec(ctx, `UPDATE file_versions SET retention_until=$2 WHERE id=$1`, previousVersionID, retentionUntil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE file_versions SET file_id=$2,version=$3,retention_until=NULL WHERE id=$1`, uploadedVersionID, fileID, nextVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `WITH affected AS (
 UPDATE lesson_draft_files SET file_version_id=$2 WHERE file_version_id=$1 RETURNING lesson_id
)
UPDATE lesson_drafts d SET lock_version=d.lock_version+1,updated_at=now(),updated_by=$3
FROM affected a WHERE d.lesson_id=a.lesson_id`, previousVersionID, uploadedVersionID, actor.User.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE id=$1`, sourceFileID); err != nil {
		return err
	}
	if err := writeFileAudit(ctx, tx, actor, "file.replaced", "file", fileID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) RollbackFile(ctx context.Context, actor Principal, fileID, lessonID, versionID uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var bindingID, currentVersionID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT b.id,b.file_version_id FROM lesson_drafts d JOIN lesson_draft_files b ON b.lesson_id=d.lesson_id JOIN file_versions fv ON fv.id=b.file_version_id WHERE d.lesson_id=$1 AND fv.file_id=$2 FOR UPDATE OF d,b`, lessonID, fileID).Scan(&bindingID, &currentVersionID); err != nil {
		return mapStoreError(err)
	}
	var fileLock uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM files WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, fileID).Scan(&fileLock); err != nil {
		return mapStoreError(err)
	}
	if err := lockFileVersions(ctx, tx, fileID); err != nil {
		return err
	}
	var retention *time.Time
	var state string
	if err := tx.QueryRow(ctx, `SELECT retention_until,processing_state FROM file_versions WHERE id=$1 AND file_id=$2`, versionID, fileID).Scan(&retention, &state); err != nil {
		return mapStoreError(err)
	}
	if retention != nil && !retention.After(time.Now()) {
		return ErrFileVersionExpired
	}
	if state != "ready" {
		return ErrInvalid
	}
	if currentVersionID != versionID {
		if _, err := tx.Exec(ctx, `UPDATE lesson_draft_files SET file_version_id=$2 WHERE id=$1`, bindingID, versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE lesson_drafts SET lock_version=lock_version+1,updated_at=now(),updated_by=$2 WHERE lesson_id=$1`, lessonID, actor.User.ID); err != nil {
			return err
		}
	}
	if err := writeFileAudit(ctx, tx, actor, "file.draft_rolled_back", "file_version", versionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockDraftBindingsForFile(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT d.lesson_id,b.id FROM lesson_drafts d JOIN lesson_draft_files b ON b.lesson_id=d.lesson_id JOIN file_versions fv ON fv.id=b.file_version_id WHERE fv.file_id=$1 ORDER BY d.lesson_id,b.id FOR UPDATE OF d,b`, fileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var lessonID, bindingID uuid.UUID
		if err := rows.Scan(&lessonID, &bindingID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *PostgresStore) DeleteFile(ctx context.Context, actor Principal, fileID uuid.UUID, retentionUntil time.Time) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var deletedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT deleted_at FROM files WHERE id=$1 FOR UPDATE`, fileID).Scan(&deletedAt); err != nil {
		return mapStoreError(err)
	}
	locked, err := tx.Query(ctx, `SELECT id FROM file_versions WHERE file_id=$1 ORDER BY id FOR UPDATE`, fileID)
	if err != nil {
		return err
	}
	for locked.Next() {
		var ignored uuid.UUID
		if err := locked.Scan(&ignored); err != nil {
			locked.Close()
			return err
		}
	}
	if err := locked.Err(); err != nil {
		locked.Close()
		return err
	}
	locked.Close()
	var referenced bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lesson_draft_files b JOIN file_versions fv ON fv.id=b.file_version_id WHERE fv.file_id=$1) OR EXISTS(SELECT 1 FROM lesson_revision_files b JOIN file_versions fv ON fv.id=b.file_version_id WHERE fv.file_id=$1)`, fileID).Scan(&referenced); err != nil {
		return err
	}
	if referenced {
		return ErrFileInUse
	}
	if deletedAt == nil {
		deleted := retentionUntil.Add(-FileRetentionPeriod)
		if _, err := tx.Exec(ctx, `UPDATE files SET deleted_at=$2 WHERE id=$1`, fileID, deleted); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE file_versions SET retention_until=$2,cleanup_state=CASE WHEN purged_at IS NULL THEN 'pending' ELSE cleanup_state END,cleanup_lease_owner=NULL,cleanup_lease_until=NULL WHERE file_id=$1`, fileID, retentionUntil); err != nil {
			return err
		}
		if err := writeFileAudit(ctx, tx, actor, "file.delete_requested", "file", fileID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func scanFileVersion(row rowScanner, version *FileVersionDetail) error {
	return row.Scan(&version.ID, &version.FileID, &version.Version, &version.DisplayName, &version.DeclaredMIME, &version.DetectedMIME, &version.Size, &version.ProcessingState, &version.FailureCategory, &version.PreviewState, &version.BrowserPlayable, &version.CreatedAt, &version.RetentionUntil)
}

func writeFileAudit(ctx context.Context, tx pgx.Tx, actor Principal, action, targetType string, id uuid.UUID) error {
	return audit.NewPostgresWriter(tx).Write(ctx, audit.Event{ActorUserID: actor.User.ID, Action: action, TargetType: targetType, TargetID: id.String(), Metadata: map[string]any{}, RequestID: actor.RequestID, IP: actor.IP})
}

var _ FileCenterStore = (*PostgresStore)(nil)
