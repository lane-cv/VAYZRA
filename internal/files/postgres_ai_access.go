package files

import (
	"context"

	"github.com/google/uuid"
)

func (s *PostgresStore) ResolveAIStatus(ctx context.Context, actor Principal, requestedID uuid.UUID) (AIFileStatus, error) {
	var out AIFileStatus
	err := s.pool.QueryRow(ctx, `
SELECT fv.id,m.id,fv.processing_state,coalesce(fv.failure_category,''),coalesce(fv.detected_mime,''),fv.size_bytes,
 (fv.browser_playable OR EXISTS(
   SELECT 1 FROM file_previews fp WHERE fp.file_version_id=fv.id AND fp.processing_state='ready'
 ))
FROM ai_message_files mf
JOIN ai_messages m ON m.id=mf.message_id AND m.role='student' AND m.sender_user_id=$1
JOIN ai_threads t ON t.id=m.thread_id AND t.student_id=$1
JOIN users actor ON actor.id=t.student_id AND actor.role='student' AND actor.status='active' AND actor.deleted_at IS NULL
JOIN file_versions fv ON fv.id=mf.file_version_id AND fv.id=$2 AND fv.purpose='ai_attachment'
 AND fv.processing_state='ready' AND fv.scan_result='clean' AND fv.created_by=$1
JOIN files f ON f.id=fv.file_id AND f.created_by=$1 AND f.deleted_at IS NULL`,
		actor.User.ID, requestedID).Scan(
		&out.FileVersionID, &out.MessageID, &out.ProcessingState, &out.FailureCategory, &out.DetectedMIME, &out.Size, &out.PreviewAvailable)
	if err != nil {
		return AIFileStatus{}, mapStoreError(err)
	}
	return out, nil
}

func (s *PostgresStore) ResolveAIAccess(ctx context.Context, actor Principal, requestedID uuid.UUID) (AIDelivery, error) {
	var out AIDelivery
	err := s.pool.QueryRow(ctx, `
SELECT fv.id,m.id,
 CASE WHEN fv.browser_playable THEN fv.object_key ELSE fp.object_key END,
 mf.display_name,
 CASE WHEN fv.browser_playable THEN fv.detected_mime ELSE fp.content_type END,
 CASE WHEN fv.browser_playable THEN fv.size_bytes ELSE fp.size_bytes END,
 NOT fv.browser_playable,
 fv.browser_playable
FROM ai_message_files mf
JOIN ai_messages m ON m.id=mf.message_id AND m.role='student' AND m.sender_user_id=$1
JOIN ai_threads t ON t.id=m.thread_id AND t.student_id=$1
JOIN users actor ON actor.id=t.student_id AND actor.role='student' AND actor.status='active' AND actor.deleted_at IS NULL
JOIN file_versions fv ON fv.id=mf.file_version_id AND fv.id=$2 AND fv.purpose='ai_attachment'
 AND fv.processing_state='ready' AND fv.scan_result='clean' AND fv.created_by=$1
JOIN files f ON f.id=fv.file_id AND f.created_by=$1 AND f.deleted_at IS NULL
LEFT JOIN LATERAL (
 SELECT object_key,content_type,size_bytes FROM file_previews
 WHERE file_version_id=fv.id AND processing_state='ready'
 ORDER BY CASE preview_kind WHEN 'pdf' THEN 1 WHEN 'page' THEN 2 WHEN 'thumbnail' THEN 3 WHEN 'poster' THEN 4 WHEN 'ai_text' THEN 5 ELSE 6 END,id LIMIT 1
) fp ON true
WHERE fv.browser_playable OR fp.object_key IS NOT NULL
ORDER BY m.created_at,m.id LIMIT 1`, actor.User.ID, requestedID).Scan(
		&out.VersionID, &out.MessageID, &out.ObjectKey, &out.DisplayName, &out.ContentType, &out.Size, &out.Preview, &out.Playable)
	if err != nil {
		return AIDelivery{}, mapStoreError(err)
	}
	return out, nil
}

var _ AIAccessStore = (*PostgresStore)(nil)
