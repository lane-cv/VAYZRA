package teaching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
)

type postgresQueries interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type PostgresStore struct {
	q    postgresQueries
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{q: pool, pool: pool} }
func (s *PostgresStore) WithinTx(ctx context.Context, fn func(TxStore, audit.Writer) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := fn(&PostgresStore{q: tx}, audit.NewPostgresWriter(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateCatalog(ctx context.Context, in CatalogCreateInput) (CatalogNode, error) {
	var q string
	switch in.Kind {
	case CatalogGrade:
		q = `INSERT INTO grades (name, sort_key) VALUES ($1,$2) RETURNING id, name, sort_key, archived_at`
	case CatalogTerm:
		q = `INSERT INTO terms (grade_id, name, sort_key) SELECT id,$2,$3 FROM grades WHERE id=$1 AND archived_at IS NULL RETURNING id, grade_id, name, sort_key, archived_at`
	case CatalogSubject:
		q = `INSERT INTO subjects (term_id, name, sort_key) SELECT id,$2,$3 FROM terms WHERE id=$1 AND archived_at IS NULL RETURNING id, term_id, name, sort_key, archived_at`
	case CatalogChapter:
		q = `INSERT INTO chapters (subject_id, name, description, sort_key) SELECT id,$2,$3,$4 FROM subjects WHERE id=$1 AND archived_at IS NULL RETURNING id, subject_id, name, description, sort_key, archived_at`
	default:
		return CatalogNode{}, ErrInvalid
	}
	var node CatalogNode
	node.Kind = in.Kind
	var err error
	switch in.Kind {
	case CatalogGrade:
		err = s.q.QueryRow(ctx, q, in.Name, in.SortKey).Scan(&node.ID, &node.Name, &node.SortKey, &node.ArchivedAt)
	case CatalogTerm, CatalogSubject:
		err = s.q.QueryRow(ctx, q, in.ParentID, in.Name, in.SortKey).Scan(&node.ID, &node.ParentID, &node.Name, &node.SortKey, &node.ArchivedAt)
	case CatalogChapter:
		err = s.q.QueryRow(ctx, q, in.ParentID, in.Name, in.Description, in.SortKey).Scan(&node.ID, &node.ParentID, &node.Name, &node.Description, &node.SortKey, &node.ArchivedAt)
	}
	return node, mapTeachingError(err)
}
func (s *PostgresStore) RenameCatalog(ctx context.Context, in CatalogRenameInput) (CatalogNode, error) {
	if in.Kind == CatalogGrade {
		var node CatalogNode
		node.Kind = in.Kind
		err := s.q.QueryRow(ctx, `UPDATE grades SET name=$2, updated_at=now() WHERE id=$1 RETURNING id, name, sort_key, archived_at`, in.ID, in.Name).Scan(&node.ID, &node.Name, &node.SortKey, &node.ArchivedAt)
		return node, mapTeachingError(err)
	}
	table, parent := catalogTable(in.Kind)
	if table == "" {
		return CatalogNode{}, ErrInvalid
	}
	query := fmt.Sprintf(`UPDATE %s SET name=$2, updated_at=now() WHERE id=$1 RETURNING id, %s, name, sort_key, archived_at`, table, parent)
	var node CatalogNode
	node.Kind = in.Kind
	err := s.q.QueryRow(ctx, query, in.ID, in.Name).Scan(&node.ID, &node.ParentID, &node.Name, &node.SortKey, &node.ArchivedAt)
	return node, mapTeachingError(err)
}
func (s *PostgresStore) ReorderCatalog(ctx context.Context, in CatalogReorderInput) error {
	table, _ := catalogTable(in.Kind)
	if table == "" {
		return ErrInvalid
	}
	tag, err := s.q.Exec(ctx, fmt.Sprintf(`UPDATE %s SET sort_key=$2, updated_at=now() WHERE id=$1`, table), in.ID, in.SortKey)
	if err != nil {
		return mapTeachingError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *PostgresStore) ArchiveCatalog(ctx context.Context, in CatalogArchiveInput) error {
	table, _ := catalogTable(in.Kind)
	if table == "" {
		return ErrInvalid
	}
	var archived any
	if in.Archived {
		archived = time.Now().UTC()
	}
	tag, err := s.q.Exec(ctx, fmt.Sprintf(`UPDATE %s SET archived_at=$2, updated_at=now() WHERE id=$1`, table), in.ID, archived)
	if err != nil {
		return mapTeachingError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
func catalogTable(kind CatalogKind) (string, string) {
	switch kind {
	case CatalogGrade:
		return "grades", ""
	case CatalogTerm:
		return "terms", "grade_id"
	case CatalogSubject:
		return "subjects", "term_id"
	case CatalogChapter:
		return "chapters", "subject_id"
	}
	return "", ""
}

func (s *PostgresStore) CreateLesson(ctx context.Context, in CreateLessonInput) (Draft, error) {
	var lessonID uuid.UUID
	err := s.q.QueryRow(ctx, `INSERT INTO lessons (chapter_id) SELECT id FROM chapters WHERE id=$1 AND archived_at IS NULL RETURNING id`, in.ChapterID).Scan(&lessonID)
	if err != nil {
		return Draft{}, mapTeachingError(err)
	}
	_, err = s.q.Exec(ctx, `INSERT INTO lesson_drafts (lesson_id, title, updated_by) VALUES ($1,$2,$3)`, lessonID, in.Title, in.ActorID)
	if err != nil {
		return Draft{}, mapTeachingError(err)
	}
	return s.GetDraft(ctx, lessonID)
}
func (s *PostgresStore) GetDraft(ctx context.Context, lessonID uuid.UUID) (Draft, error) {
	var d Draft
	err := s.q.QueryRow(ctx, `SELECT d.lesson_id,l.chapter_id,d.title,d.summary,d.body_markdown,d.sort_key,d.lock_version,d.updated_at FROM lesson_drafts d JOIN lessons l ON l.id=d.lesson_id WHERE d.lesson_id=$1`, lessonID).Scan(&d.LessonID, &d.ChapterID, &d.Title, &d.Summary, &d.BodyMarkdown, &d.SortKey, &d.LockVersion, &d.UpdatedAt)
	if err != nil {
		return Draft{}, mapTeachingError(err)
	}
	d.UpdatedAt = d.UpdatedAt.UTC()
	if err := s.loadDraftChildren(ctx, &d); err != nil {
		return Draft{}, err
	}
	return d, nil
}
func (s *PostgresStore) loadDraftChildren(ctx context.Context, d *Draft) error {
	if err := s.q.QueryRow(ctx, `SELECT mode FROM lesson_draft_audiences WHERE lesson_id=$1`, d.LessonID).Scan(&d.Audience.Mode); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return mapTeachingError(err)
	}
	rows, err := s.q.Query(ctx, `SELECT user_id FROM lesson_draft_audience_users WHERE lesson_id=$1 ORDER BY user_id`, d.LessonID)
	if err != nil {
		return mapTeachingError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return mapTeachingError(err)
		}
		d.Audience.UserIDs = append(d.Audience.UserIDs, id)
	}
	if err := rows.Err(); err != nil {
		return mapTeachingError(err)
	}
	rows, err = s.q.Query(ctx, `SELECT id,url,title,description,sort_key FROM lesson_draft_external_videos WHERE lesson_id=$1 ORDER BY sort_key,id`, d.LessonID)
	if err != nil {
		return mapTeachingError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v ExternalVideo
		if err := rows.Scan(&v.ID, &v.URL, &v.Title, &v.Description, &v.SortKey); err != nil {
			return mapTeachingError(err)
		}
		d.ExternalVideos = append(d.ExternalVideos, v)
	}
	return mapTeachingError(rows.Err())
}
func (s *PostgresStore) SaveDraft(ctx context.Context, in SaveDraftInput) (Draft, error) {
	row := s.q.QueryRow(ctx, `UPDATE lesson_drafts SET title=$3, summary=$4, body_markdown=$5,
  sort_key=$6, lock_version=lock_version+1, updated_by=$7, updated_at=now()
  WHERE lesson_id=$1 AND lock_version=$2
  RETURNING lesson_id, title, summary, body_markdown, sort_key, lock_version, updated_at`, in.LessonID, in.ExpectedVersion, in.Title, in.Summary, in.BodyMarkdown, in.SortKey, in.ActorID)
	var d Draft
	err := row.Scan(&d.LessonID, &d.Title, &d.Summary, &d.BodyMarkdown, &d.SortKey, &d.LockVersion, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrConflict
	}
	if err != nil {
		return Draft{}, mapTeachingError(err)
	}
	if _, err = s.q.Exec(ctx, `DELETE FROM lesson_draft_audiences WHERE lesson_id=$1`, in.LessonID); err != nil {
		return Draft{}, mapTeachingError(err)
	}
	if _, err = s.q.Exec(ctx, `INSERT INTO lesson_draft_audiences (lesson_id,mode) VALUES ($1,$2)`, in.LessonID, in.Audience.Mode); err != nil {
		return Draft{}, mapTeachingError(err)
	}
	for _, id := range in.Audience.UserIDs {
		if _, err = s.q.Exec(ctx, `INSERT INTO lesson_draft_audience_users (lesson_id,user_id) VALUES ($1,$2)`, in.LessonID, id); err != nil {
			return Draft{}, mapTeachingError(err)
		}
	}
	if _, err = s.q.Exec(ctx, `DELETE FROM lesson_draft_external_videos WHERE lesson_id=$1`, in.LessonID); err != nil {
		return Draft{}, mapTeachingError(err)
	}
	for _, v := range in.ExternalVideos {
		if _, err = s.q.Exec(ctx, `INSERT INTO lesson_draft_external_videos (id,lesson_id,url,title,description,sort_key) VALUES ($1,$2,$3,$4,$5,$6)`, v.ID, in.LessonID, v.URL, v.Title, v.Description, v.SortKey); err != nil {
			return Draft{}, mapTeachingError(err)
		}
	}
	return s.GetDraft(ctx, in.LessonID)
}
func (s *PostgresStore) Publish(ctx context.Context, in PublishInput) (Revision, error) {
	var locked uuid.UUID
	if err := s.q.QueryRow(ctx, `SELECT d.lesson_id FROM lesson_drafts d JOIN lessons l ON l.id=d.lesson_id JOIN chapters c ON c.id=l.chapter_id JOIN subjects s ON s.id=c.subject_id JOIN terms t ON t.id=s.term_id JOIN grades g ON g.id=t.grade_id WHERE d.lesson_id=$1 AND l.archived_at IS NULL AND c.archived_at IS NULL AND s.archived_at IS NULL AND t.archived_at IS NULL AND g.archived_at IS NULL FOR UPDATE OF d,l,c,s,t,g`, in.LessonID).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, ErrNotPublishable
	} else if err != nil {
		return Revision{}, mapTeachingError(err)
	}
	d, err := s.GetDraft(ctx, in.LessonID)
	if err != nil {
		return Revision{}, err
	}
	if d.LockVersion != in.ExpectedVersion {
		return Revision{}, ErrConflict
	}
	var version int64
	if err := s.q.QueryRow(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM lesson_revisions WHERE lesson_id=$1`, in.LessonID).Scan(&version); err != nil {
		return Revision{}, mapTeachingError(err)
	}
	var revision Revision
	revision.LessonID, revision.Version, revision.Title, revision.Summary, revision.BodyMarkdown, revision.SortKey, revision.PublishedBy = in.LessonID, version, d.Title, d.Summary, d.BodyMarkdown, d.SortKey, in.ActorID
	err = s.q.QueryRow(ctx, `INSERT INTO lesson_revisions (lesson_id,version,title,summary,body_markdown,sort_key,published_by) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id,published_at`, in.LessonID, version, d.Title, d.Summary, d.BodyMarkdown, d.SortKey, in.ActorID).Scan(&revision.ID, &revision.PublishedAt)
	if err != nil {
		return Revision{}, mapTeachingError(err)
	}
	revision.PublishedAt = revision.PublishedAt.UTC()
	revision.Audience = d.Audience
	revision.ExternalVideos = append([]ExternalVideo(nil), d.ExternalVideos...)
	if _, err = s.q.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id,mode) VALUES ($1,$2)`, revision.ID, d.Audience.Mode); err != nil {
		return Revision{}, mapTeachingError(err)
	}
	for _, id := range d.Audience.UserIDs {
		if _, err = s.q.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id,user_id) VALUES ($1,$2)`, revision.ID, id); err != nil {
			return Revision{}, mapTeachingError(err)
		}
	}
	revision.ExternalVideos = make([]ExternalVideo, 0, len(d.ExternalVideos))
	for _, v := range d.ExternalVideos {
		snapshot := v
		snapshot.ID = uuid.New()
		revision.ExternalVideos = append(revision.ExternalVideos, snapshot)
		if _, err = s.q.Exec(ctx, `INSERT INTO lesson_revision_external_videos (id,revision_id,url,title,description,sort_key) VALUES ($1,$2,$3,$4,$5,$6)`, snapshot.ID, revision.ID, snapshot.URL, snapshot.Title, snapshot.Description, snapshot.SortKey); err != nil {
			return Revision{}, mapTeachingError(err)
		}
	}
	if _, err = s.q.Exec(ctx, `SELECT finalize_lesson_revision($1)`, revision.ID); err != nil {
		return Revision{}, mapTeachingError(err)
	}
	if _, err = s.q.Exec(ctx, `UPDATE lessons SET published_revision_id=$2,updated_at=now() WHERE id=$1`, in.LessonID, revision.ID); err != nil {
		return Revision{}, mapTeachingError(err)
	}
	payload, _ := json.Marshal(map[string]string{"lesson_id": in.LessonID.String(), "revision_id": revision.ID.String()})
	if _, err = s.q.Exec(ctx, `INSERT INTO outbox_events (kind,payload) VALUES ('lesson.published',$1::jsonb)`, payload); err != nil {
		return Revision{}, mapTeachingError(err)
	}
	return revision, nil
}
func (s *PostgresStore) Withdraw(ctx context.Context, in WithdrawInput) error {
	tag, err := s.q.Exec(ctx, `UPDATE lessons SET published_revision_id=NULL,updated_at=now() WHERE id=$1 AND published_revision_id IS NOT NULL`, in.LessonID)
	if err != nil {
		return mapTeachingError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *PostgresStore) ArchiveLesson(ctx context.Context, lessonID uuid.UUID) error {
	tag, err := s.q.Exec(ctx, `UPDATE lessons SET archived_at=now(), updated_at=now() WHERE id=$1 AND archived_at IS NULL`, lessonID)
	if err != nil {
		return mapTeachingError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) Browse(ctx context.Context, in BrowseInput) ([]CatalogNode, error) {
	rows, err := s.q.Query(ctx, `
WITH authorized_lessons AS (
  SELECT l.chapter_id
  FROM users u
  JOIN lessons l ON l.archived_at IS NULL
  JOIN lesson_revisions r ON r.id = l.published_revision_id
  JOIN lesson_revision_finalizations rf ON rf.revision_id = r.id AND rf.lesson_id = l.id
  JOIN lesson_revision_audiences ra ON ra.revision_id = r.id
  JOIN chapters c ON c.id = l.chapter_id AND c.archived_at IS NULL
  JOIN subjects s ON s.id = c.subject_id AND s.archived_at IS NULL
  JOIN terms t ON t.id = s.term_id AND t.archived_at IS NULL
  JOIN grades g ON g.id = t.grade_id AND g.archived_at IS NULL
  WHERE u.id = $1 AND u.role = 'student' AND u.status = 'active' AND u.deleted_at IS NULL
    AND (ra.mode = 'all' OR EXISTS (
      SELECT 1 FROM lesson_revision_audience_users rau
      WHERE rau.revision_id = r.id AND rau.user_id = $1
    ))
)
SELECT g.id, NULL::uuid, 'grade', g.name, '', g.sort_key, g.archived_at
FROM grades g
WHERE g.archived_at IS NULL AND ($2::uuid IS NULL OR g.id = $2)
  AND EXISTS (SELECT 1 FROM terms t JOIN subjects s ON s.term_id=t.id JOIN chapters c ON c.subject_id=s.id JOIN authorized_lessons al ON al.chapter_id=c.id WHERE t.grade_id=g.id)
UNION ALL
SELECT t.id, t.grade_id, 'term', t.name, '', t.sort_key, t.archived_at
FROM terms t
WHERE t.archived_at IS NULL AND ($2::uuid IS NULL OR t.grade_id = $2) AND ($3::uuid IS NULL OR t.id = $3)
  AND EXISTS (SELECT 1 FROM subjects s JOIN chapters c ON c.subject_id=s.id JOIN authorized_lessons al ON al.chapter_id=c.id WHERE s.term_id=t.id)
UNION ALL
SELECT s.id, s.term_id, 'subject', s.name, '', s.sort_key, s.archived_at
FROM subjects s
WHERE s.archived_at IS NULL AND ($3::uuid IS NULL OR s.term_id = $3) AND ($4::uuid IS NULL OR s.id = $4)
  AND EXISTS (SELECT 1 FROM chapters c JOIN authorized_lessons al ON al.chapter_id=c.id WHERE c.subject_id=s.id)
UNION ALL
SELECT c.id, c.subject_id, 'chapter', c.name, c.description, c.sort_key, c.archived_at
FROM chapters c
WHERE c.archived_at IS NULL AND ($4::uuid IS NULL OR c.subject_id = $4) AND ($5::uuid IS NULL OR c.id = $5)
  AND EXISTS (SELECT 1 FROM authorized_lessons al WHERE al.chapter_id=c.id)
ORDER BY 6, 1`, in.StudentID, nullUUID(in.GradeID), nullUUID(in.TermID), nullUUID(in.SubjectID), nullUUID(in.ChapterID))
	if err != nil {
		return nil, mapTeachingError(err)
	}
	defer rows.Close()
	nodes := make([]CatalogNode, 0)
	for rows.Next() {
		var node CatalogNode
		var parent *uuid.UUID
		if err := rows.Scan(&node.ID, &parent, &node.Kind, &node.Name, &node.Description, &node.SortKey, &node.ArchivedAt); err != nil {
			return nil, mapTeachingError(err)
		}
		if parent != nil {
			node.ParentID = *parent
		}
		nodes = append(nodes, node)
	}
	return nodes, mapTeachingError(rows.Err())
}

const authorizedRevisionJoins = `
FROM users u
JOIN lessons l ON l.id = $2 AND l.archived_at IS NULL
JOIN lesson_revisions r ON r.id = l.published_revision_id
JOIN lesson_revision_finalizations rf ON rf.revision_id = r.id AND rf.lesson_id = l.id
JOIN lesson_revision_audiences ra ON ra.revision_id = r.id
JOIN chapters c ON c.id = l.chapter_id AND c.archived_at IS NULL
JOIN subjects s ON s.id = c.subject_id AND s.archived_at IS NULL
JOIN terms t ON t.id = s.term_id AND t.archived_at IS NULL
JOIN grades g ON g.id = t.grade_id AND g.archived_at IS NULL`

const authorizedRevisionWhere = `
WHERE u.id = $1 AND u.role = 'student' AND u.status = 'active' AND u.deleted_at IS NULL
  AND (ra.mode = 'all' OR EXISTS (
    SELECT 1 FROM lesson_revision_audience_users rau WHERE rau.revision_id = r.id AND rau.user_id = $1
  ))`

func (s *PostgresStore) GetLesson(ctx context.Context, studentID, lessonID uuid.UUID) (Revision, error) {
	var revision Revision
	err := s.q.QueryRow(ctx, `SELECT r.id,r.lesson_id,r.version,r.title,r.summary,r.body_markdown,r.sort_key,r.published_by,r.published_at `+authorizedRevisionJoins+authorizedRevisionWhere, studentID, lessonID).Scan(
		&revision.ID, &revision.LessonID, &revision.Version, &revision.Title, &revision.Summary, &revision.BodyMarkdown, &revision.SortKey, &revision.PublishedBy, &revision.PublishedAt,
	)
	if err != nil {
		return Revision{}, mapTeachingError(err)
	}
	revision.PublishedAt = revision.PublishedAt.UTC()
	rows, err := s.q.Query(ctx, `SELECT v.id,v.url,v.title,v.description,v.sort_key `+authorizedRevisionJoins+` JOIN lesson_revision_external_videos v ON v.revision_id=r.id `+authorizedRevisionWhere+` ORDER BY v.sort_key,v.id`, studentID, lessonID)
	if err != nil {
		return Revision{}, mapTeachingError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var video ExternalVideo
		if err := rows.Scan(&video.ID, &video.URL, &video.Title, &video.Description, &video.SortKey); err != nil {
			return Revision{}, mapTeachingError(err)
		}
		revision.ExternalVideos = append(revision.ExternalVideos, video)
	}
	return revision, mapTeachingError(rows.Err())
}

func (s *PostgresStore) Search(ctx context.Context, in SearchInput) ([]Revision, SearchCursor, error) {
	rows, err := s.q.Query(ctx, `
SELECT r.id,r.lesson_id,r.version,r.title,r.summary,r.body_markdown,r.sort_key,r.published_by,r.published_at
FROM users u
JOIN lessons l ON l.archived_at IS NULL
JOIN lesson_revisions r ON r.id = l.published_revision_id
JOIN lesson_revision_finalizations rf ON rf.revision_id = r.id AND rf.lesson_id = l.id
JOIN lesson_revision_audiences ra ON ra.revision_id = r.id
JOIN chapters c ON c.id = l.chapter_id AND c.archived_at IS NULL
JOIN subjects s ON s.id = c.subject_id AND s.archived_at IS NULL
JOIN terms t ON t.id = s.term_id AND t.archived_at IS NULL
JOIN grades g ON g.id = t.grade_id AND g.archived_at IS NULL
WHERE u.id = $1 AND u.role = 'student' AND u.status = 'active' AND u.deleted_at IS NULL
  AND (ra.mode = 'all' OR EXISTS (SELECT 1 FROM lesson_revision_audience_users rau WHERE rau.revision_id = r.id AND rau.user_id = $1))
  AND (r.title ILIKE $2 ESCAPE E'\\' OR r.body_markdown ILIKE $2 ESCAPE E'\\')
  AND (NOT $3 OR (r.sort_key, r.id) > ($4, $5))
ORDER BY r.sort_key,r.id LIMIT $6`, in.StudentID, "%"+escapeLike(in.Query)+"%", in.After.ID != uuid.Nil, in.After.SortKey, in.After.ID, in.Limit+1)
	if err != nil {
		return nil, SearchCursor{}, mapTeachingError(err)
	}
	defer rows.Close()
	revisions := make([]Revision, 0, in.Limit)
	for rows.Next() {
		var revision Revision
		if err := rows.Scan(&revision.ID, &revision.LessonID, &revision.Version, &revision.Title, &revision.Summary, &revision.BodyMarkdown, &revision.SortKey, &revision.PublishedBy, &revision.PublishedAt); err != nil {
			return nil, SearchCursor{}, mapTeachingError(err)
		}
		revision.PublishedAt = revision.PublishedAt.UTC()
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, SearchCursor{}, mapTeachingError(err)
	}
	var next SearchCursor
	if len(revisions) > in.Limit {
		revisions = revisions[:in.Limit]
		last := revisions[len(revisions)-1]
		next = SearchCursor{SortKey: last.SortKey, ID: last.ID}
	}
	return revisions, next, nil
}

func (s *PostgresStore) UpdateProgress(ctx context.Context, studentID uuid.UUID, in ProgressInput) error {
	var authorized, updated bool
	err := s.q.QueryRow(ctx, `
WITH authorized AS (
  SELECT r.id FROM users u
  JOIN lessons l ON l.archived_at IS NULL
  JOIN lesson_revisions r ON r.id = l.published_revision_id AND r.id = $2
  JOIN lesson_revision_finalizations rf ON rf.revision_id = r.id AND rf.lesson_id = l.id
  JOIN lesson_revision_audiences ra ON ra.revision_id = r.id
  JOIN chapters c ON c.id = l.chapter_id AND c.archived_at IS NULL
  JOIN subjects s ON s.id = c.subject_id AND s.archived_at IS NULL
  JOIN terms t ON t.id = s.term_id AND t.archived_at IS NULL
  JOIN grades g ON g.id = t.grade_id AND g.archived_at IS NULL
  WHERE u.id = $1 AND u.role = 'student' AND u.status = 'active' AND u.deleted_at IS NULL
    AND (ra.mode = 'all' OR EXISTS (SELECT 1 FROM lesson_revision_audience_users rau WHERE rau.revision_id = r.id AND rau.user_id = $1))
), upsert AS (
  INSERT INTO lesson_progress (user_id,revision_id,viewed,anchor,scroll_ratio,observed_at)
  SELECT $1,id,$3,$4,$5,$6 FROM authorized
  ON CONFLICT (user_id,revision_id) DO UPDATE SET
    viewed = lesson_progress.viewed OR EXCLUDED.viewed,
    anchor = EXCLUDED.anchor, scroll_ratio = EXCLUDED.scroll_ratio,
    observed_at = EXCLUDED.observed_at, last_viewed_at = now()
  WHERE lesson_progress.observed_at <= EXCLUDED.observed_at
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM authorized), EXISTS (SELECT 1 FROM upsert)`, studentID, in.RevisionID, in.Viewed, in.Anchor, in.ScrollRatio, in.ObservedAt).Scan(&authorized, &updated)
	if err != nil {
		return mapTeachingError(err)
	}
	if !authorized {
		return ErrNotFound
	}
	return nil
}

func escapeLike(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "%", "\\%")
	return strings.ReplaceAll(v, "_", "\\_")
}
func nullUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func mapTeachingError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		case "23514", "22001", "22P02":
			return ErrInvalid
		}
	}
	return err
}

var _ CatalogStore = (*PostgresStore)(nil)
var _ UnitOfWork = (*PostgresStore)(nil)
