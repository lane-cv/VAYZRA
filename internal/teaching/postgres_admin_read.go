package teaching

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) PublicationQuery(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return s.q.Query(ctx, sql, args...)
}
func (s *PostgresStore) PublicationQueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.q.QueryRow(ctx, sql, args...)
}

func (s *PostgresStore) EligibleAudienceUsers(ctx context.Context, ids []uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=ANY($1) AND role='student' AND status='active' AND deleted_at IS NULL`, ids).Scan(&count)
	return count, mapTeachingError(err)
}

func (s *PostgresStore) ListAdminCatalog(ctx context.Context, in AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error) {
	rows, err := s.q.Query(ctx, `WITH items(rank,id,parent_id,kind,name,description,sort_key,archived_at,published) AS (
		SELECT 1,g.id,NULL::uuid,'grade',g.name,''::text,g.sort_key,g.archived_at,false FROM grades g
		UNION ALL SELECT 2,t.id,t.grade_id,'term',t.name,'',t.sort_key,CASE WHEN g.archived_at IS NOT NULL THEN g.archived_at ELSE t.archived_at END,false FROM terms t JOIN grades g ON g.id=t.grade_id
		UNION ALL SELECT 3,s.id,s.term_id,'subject',s.name,'',s.sort_key,COALESCE(g.archived_at,t.archived_at,s.archived_at),false FROM subjects s JOIN terms t ON t.id=s.term_id JOIN grades g ON g.id=t.grade_id
		UNION ALL SELECT 4,c.id,c.subject_id,'chapter',c.name,c.description,c.sort_key,COALESCE(g.archived_at,t.archived_at,s.archived_at,c.archived_at),false FROM chapters c JOIN subjects s ON s.id=c.subject_id JOIN terms t ON t.id=s.term_id JOIN grades g ON g.id=t.grade_id
		UNION ALL SELECT 5,l.id,l.chapter_id,'lesson',d.title,d.summary,d.sort_key,COALESCE(g.archived_at,t.archived_at,s.archived_at,c.archived_at,l.archived_at),l.published_revision_id IS NOT NULL FROM lessons l JOIN lesson_drafts d ON d.lesson_id=l.id JOIN chapters c ON c.id=l.chapter_id JOIN subjects s ON s.id=c.subject_id JOIN terms t ON t.id=s.term_id JOIN grades g ON g.id=t.grade_id
	) SELECT rank,id,COALESCE(parent_id,'00000000-0000-0000-0000-000000000000'::uuid),kind,name,description,sort_key,archived_at,published FROM items
	WHERE ($1='' OR kind=$1) AND ($2::uuid='00000000-0000-0000-0000-000000000000' OR parent_id=$2) AND ($3 OR archived_at IS NULL)
	AND (($4=0) OR (rank,sort_key,id)>($4,$5,$6)) ORDER BY rank,sort_key,id LIMIT $7`, in.Kind, in.ParentID, in.IncludeArchived, in.After.Rank, in.After.SortKey, in.After.ID, in.Limit)
	if err != nil {
		return nil, AdminCatalogCursor{}, mapTeachingError(err)
	}
	defer rows.Close()
	items := make([]AdminCatalogItem, 0, in.Limit)
	var next AdminCatalogCursor
	for rows.Next() {
		var item AdminCatalogItem
		var rank int
		if err := rows.Scan(&rank, &item.ID, &item.ParentID, &item.Kind, &item.Name, &item.Description, &item.SortKey, &item.ArchivedAt, &item.Published); err != nil {
			return nil, AdminCatalogCursor{}, mapTeachingError(err)
		}
		items = append(items, item)
		next = AdminCatalogCursor{Rank: rank, SortKey: item.SortKey, ID: item.ID}
	}
	if err := rows.Err(); err != nil {
		return nil, AdminCatalogCursor{}, mapTeachingError(err)
	}
	if len(items) < in.Limit {
		next = AdminCatalogCursor{}
	}
	return items, next, nil
}

func (s *PostgresStore) GetAdminLesson(ctx context.Context, id uuid.UUID) (AdminLessonDetail, error) {
	var out AdminLessonDetail
	err := s.q.QueryRow(ctx, `SELECT id,chapter_id,COALESCE(published_revision_id,'00000000-0000-0000-0000-000000000000'::uuid),archived_at FROM lessons WHERE id=$1`, id).Scan(&out.Lesson.ID, &out.Lesson.ChapterID, &out.Lesson.PublishedRevisionID, &out.Lesson.ArchivedAt)
	if err != nil {
		return AdminLessonDetail{}, mapTeachingError(err)
	}
	out.Draft, err = s.GetDraft(ctx, id)
	if err != nil {
		return AdminLessonDetail{}, err
	}
	if out.Lesson.PublishedRevisionID != uuid.Nil {
		rev, err := s.getRevision(ctx, out.Lesson.PublishedRevisionID)
		if err != nil {
			return AdminLessonDetail{}, err
		}
		out.Published = &rev
	}
	return out, nil
}

func (s *PostgresStore) ListAdminRevisions(ctx context.Context, lessonID uuid.UUID, limit int, after RevisionCursor) ([]Revision, RevisionCursor, error) {
	rows, err := s.q.Query(ctx, `SELECT id FROM lesson_revisions WHERE lesson_id=$1 AND ($2=0 OR (version,id)<($2,$3)) ORDER BY version DESC,id DESC LIMIT $4`, lessonID, after.Version, after.ID, limit)
	if err != nil {
		return nil, RevisionCursor{}, mapTeachingError(err)
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, RevisionCursor{}, mapTeachingError(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, RevisionCursor{}, mapTeachingError(err)
	}
	revisions := make([]Revision, 0, len(ids))
	var next RevisionCursor
	for _, id := range ids {
		rev, err := s.getRevision(ctx, id)
		if err != nil {
			return nil, RevisionCursor{}, err
		}
		revisions = append(revisions, rev)
		next = RevisionCursor{Version: rev.Version, ID: rev.ID}
	}
	if len(revisions) < limit {
		next = RevisionCursor{}
	}
	return revisions, next, nil
}

func (s *PostgresStore) getRevision(ctx context.Context, id uuid.UUID) (Revision, error) {
	var r Revision
	err := s.q.QueryRow(ctx, `SELECT id,lesson_id,version,source_draft_version,title,summary,body_markdown,sort_key,published_by,published_at FROM lesson_revisions WHERE id=$1`, id).Scan(&r.ID, &r.LessonID, &r.Version, &r.SourceDraftVersion, &r.Title, &r.Summary, &r.BodyMarkdown, &r.SortKey, &r.PublishedBy, &r.PublishedAt)
	if err != nil {
		return Revision{}, mapTeachingError(err)
	}
	r.PublishedAt = r.PublishedAt.UTC()
	if err := s.q.QueryRow(ctx, `SELECT mode FROM lesson_revision_audiences WHERE revision_id=$1`, id).Scan(&r.Audience.Mode); err != nil {
		return Revision{}, mapTeachingError(err)
	}
	rows, err := s.q.Query(ctx, `SELECT user_id FROM lesson_revision_audience_users WHERE revision_id=$1 ORDER BY user_id`, id)
	if err != nil {
		return Revision{}, mapTeachingError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid uuid.UUID
		if err := rows.Scan(&uid); err != nil {
			return Revision{}, err
		}
		r.Audience.UserIDs = append(r.Audience.UserIDs, uid)
	}
	if err := rows.Err(); err != nil {
		return Revision{}, err
	}
	rows, err = s.q.Query(ctx, `SELECT id,url,title,description,sort_key FROM lesson_revision_external_videos WHERE revision_id=$1 ORDER BY sort_key,id`, id)
	if err != nil {
		return Revision{}, mapTeachingError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v ExternalVideo
		if err := rows.Scan(&v.ID, &v.URL, &v.Title, &v.Description, &v.SortKey); err != nil {
			return Revision{}, err
		}
		r.ExternalVideos = append(r.ExternalVideos, v)
	}
	if err := rows.Err(); err != nil {
		return Revision{}, err
	}
	return r, nil
}

var _ = errors.Is
