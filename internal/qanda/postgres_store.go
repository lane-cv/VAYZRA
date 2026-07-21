package qanda

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
)

type qandaQueries interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PostgresStore struct{ q qandaQueries }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{q: pool} }

type PostgresNotificationFactory func(pgx.Tx) NotificationWriter

type PostgresUnitOfWork struct {
	pool          *pgxpool.Pool
	notifications PostgresNotificationFactory
}

func NewPostgresUnitOfWork(pool *pgxpool.Pool, notifications PostgresNotificationFactory) *PostgresUnitOfWork {
	return &PostgresUnitOfWork{pool: pool, notifications: notifications}
}

func (u *PostgresUnitOfWork) WithinTx(ctx context.Context, fn func(TxStore, audit.Writer, NotificationWriter) error) error {
	if u == nil || u.pool == nil || u.notifications == nil {
		return errors.New("qanda transaction dependencies are not configured")
	}
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := fn(&PostgresStore{q: tx}, audit.NewPostgresWriter(tx), u.notifications(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ListStudentThreads(ctx context.Context, studentID uuid.UUID, status Status, cursor ThreadCursor) ([]Thread, ThreadCursor, error) {
	if err := s.requireActiveStudent(ctx, studentID); err != nil {
		return nil, ThreadCursor{}, err
	}
	lastMessageAt, id := cursor.LastMessageAt, cursor.ID
	if lastMessageAt.IsZero() {
		lastMessageAt = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
		id = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	}
	rows, err := s.q.Query(ctx, `
		SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at
		FROM qa_threads q
		JOIN users u ON u.id=q.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
		WHERE q.student_id=$1
		  AND ($2='' OR q.status=$2)
		  AND (q.last_message_at,q.id) < ($3,$4)
		ORDER BY q.last_message_at DESC,q.id DESC LIMIT $5`, studentID, status, lastMessageAt, id, cursor.Limit)
	if err != nil {
		return nil, ThreadCursor{}, mapPostgresError(err)
	}
	defer rows.Close()
	threads := make([]Thread, 0, cursor.Limit)
	for rows.Next() {
		thread, err := scanThread(rows)
		if err != nil {
			return nil, ThreadCursor{}, err
		}
		threads = append(threads, thread)
	}
	if err := rows.Err(); err != nil {
		return nil, ThreadCursor{}, mapPostgresError(err)
	}
	var next ThreadCursor
	if len(threads) == cursor.Limit {
		last := threads[len(threads)-1]
		next = ThreadCursor{LastMessageAt: last.LastMessageAt, ID: last.ID, Limit: cursor.Limit}
	}
	return threads, next, nil
}

func (s *PostgresStore) ListAdminThreads(ctx context.Context, filter AdminThreadFilter, cursor ThreadCursor) ([]Thread, ThreadCursor, error) {
	lastMessageAt, id := cursor.LastMessageAt, cursor.ID
	if lastMessageAt.IsZero() {
		lastMessageAt = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
		id = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	}
	rows, err := s.q.Query(ctx, `
		SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at
		FROM qa_threads q JOIN users u ON u.id=q.student_id AND u.role='student' AND u.deleted_at IS NULL
		WHERE ($1='' OR q.status=$1) AND ($2::uuid IS NULL OR q.student_id=$2)
		  AND ($3::timestamptz IS NULL OR q.created_at >= $3) AND ($4::timestamptz IS NULL OR q.created_at <= $4)
		  AND (q.last_message_at,q.id)<($5,$6)
		ORDER BY q.last_message_at DESC,q.id DESC LIMIT $7`, filter.Status, nullableUUID(filter.StudentID), nullableTime(filter.From), nullableTime(filter.To), lastMessageAt, id, cursor.Limit)
	if err != nil {
		return nil, ThreadCursor{}, mapPostgresError(err)
	}
	defer rows.Close()
	threads := make([]Thread, 0, cursor.Limit)
	for rows.Next() {
		thread, scanErr := scanThread(rows)
		if scanErr != nil {
			return nil, ThreadCursor{}, scanErr
		}
		threads = append(threads, thread)
	}
	if err = rows.Err(); err != nil {
		return nil, ThreadCursor{}, mapPostgresError(err)
	}
	var next ThreadCursor
	if len(threads) == cursor.Limit {
		last := threads[len(threads)-1]
		next = ThreadCursor{LastMessageAt: last.LastMessageAt, ID: last.ID, Limit: cursor.Limit}
	}
	return threads, next, nil
}

func (s *PostgresStore) GetAdminThread(ctx context.Context, threadID uuid.UUID) (Thread, error) {
	return scanThread(s.q.QueryRow(ctx, `SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at FROM qa_threads q JOIN users u ON u.id=q.student_id AND u.role='student' AND u.deleted_at IS NULL WHERE q.id=$1`, threadID))
}

func (s *PostgresStore) ListAdminMessages(ctx context.Context, threadID uuid.UUID, cursor MessageCursor) ([]Message, MessageCursor, error) {
	createdAt, id := cursor.CreatedAt, cursor.ID
	if createdAt.IsZero() {
		createdAt = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
		id = uuid.Nil
	}
	rows, err := s.q.Query(ctx, `SELECT m.id,m.thread_id,m.sender_user_id,m.sender_role,m.message_kind,m.body_text,m.created_at FROM qa_messages m JOIN qa_threads q ON q.id=m.thread_id LEFT JOIN users sender ON sender.id=m.sender_user_id AND sender.status='active' AND sender.deleted_at IS NULL WHERE m.thread_id=$1 AND (m.created_at,m.id)>($2,$3) AND ((m.sender_role='student' AND m.sender_user_id=q.student_id AND m.message_kind IN ('initial','student_follow_up')) OR (m.sender_role='admin' AND sender.role='admin' AND m.message_kind='admin_reply')) ORDER BY m.created_at,m.id LIMIT $4`, threadID, createdAt, id, cursor.Limit)
	if err != nil {
		return nil, MessageCursor{}, mapPostgresError(err)
	}
	defer rows.Close()
	messages := make([]Message, 0, cursor.Limit)
	for rows.Next() {
		m, e := scanMessage(rows)
		if e != nil {
			return nil, MessageCursor{}, e
		}
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil {
		return nil, MessageCursor{}, mapPostgresError(err)
	}
	if len(messages) == 0 {
		if _, err = s.GetAdminThread(ctx, threadID); err != nil {
			return nil, MessageCursor{}, err
		}
	}
	for i := range messages {
		attachments, e := s.listAdminMessageAttachments(ctx, messages[i].ID)
		if e != nil {
			return nil, MessageCursor{}, e
		}
		messages[i].Attachments = attachments
	}
	var next MessageCursor
	if len(messages) == cursor.Limit {
		last := messages[len(messages)-1]
		next = MessageCursor{CreatedAt: last.CreatedAt, ID: last.ID, Limit: cursor.Limit}
	}
	return messages, next, nil
}

func (s *PostgresStore) ListTeacherNotes(ctx context.Context, threadID uuid.UUID) ([]TeacherNote, error) {
	rows, err := s.q.Query(ctx, `SELECT n.id,n.thread_id,n.author_user_id,n.body_text,n.created_at FROM teacher_notes n JOIN qa_threads q ON q.id=n.thread_id JOIN users a ON a.id=n.author_user_id AND a.role='admin' WHERE n.thread_id=$1 ORDER BY n.created_at,n.id`, threadID)
	if err != nil {
		return nil, mapPostgresError(err)
	}
	defer rows.Close()
	notes := make([]TeacherNote, 0)
	for rows.Next() {
		var n TeacherNote
		if err := rows.Scan(&n.ID, &n.ThreadID, &n.AuthorUserID, &n.Body, &n.CreatedAt); err != nil {
			return nil, mapPostgresError(err)
		}
		n.CreatedAt = n.CreatedAt.UTC()
		notes = append(notes, n)
	}
	if err = rows.Err(); err != nil {
		return nil, mapPostgresError(err)
	}
	if len(notes) == 0 {
		if _, err = s.GetAdminThread(ctx, threadID); err != nil {
			return nil, err
		}
	}
	return notes, nil
}

func (s *PostgresStore) GetStudentThread(ctx context.Context, studentID, threadID uuid.UUID) (Thread, error) {
	return scanThread(s.q.QueryRow(ctx, `
		SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at
		FROM qa_threads q
		JOIN users u ON u.id=q.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
		WHERE q.id=$1 AND q.student_id=$2`, threadID, studentID))
}

func (s *PostgresStore) ListStudentMessages(ctx context.Context, studentID, threadID uuid.UUID, cursor MessageCursor) ([]Message, MessageCursor, error) {
	createdAt, id := cursor.CreatedAt, cursor.ID
	if createdAt.IsZero() {
		createdAt = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
		id = uuid.Nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT m.id,m.thread_id,m.sender_user_id,m.sender_role,m.message_kind,m.body_text,m.created_at
		FROM qa_messages m
		JOIN qa_threads q ON q.id=m.thread_id
		JOIN users u ON u.id=q.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
		LEFT JOIN users sender_admin ON sender_admin.id=m.sender_user_id AND sender_admin.role='admin' AND sender_admin.status='active' AND sender_admin.deleted_at IS NULL
		WHERE m.thread_id=$1 AND q.student_id=$2 AND (m.created_at,m.id)>($3,$4)
		  AND ((m.sender_role='student' AND m.sender_user_id=q.student_id AND m.message_kind IN ('initial','student_follow_up'))
		    OR (m.sender_role='admin' AND sender_admin.id IS NOT NULL AND m.message_kind='admin_reply'))
		ORDER BY m.created_at,m.id LIMIT $5`, threadID, studentID, createdAt, id, cursor.Limit)
	if err != nil {
		return nil, MessageCursor{}, mapPostgresError(err)
	}
	defer rows.Close()
	messages := make([]Message, 0, cursor.Limit)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, MessageCursor{}, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, MessageCursor{}, mapPostgresError(err)
	}
	if len(messages) == 0 {
		if _, err := s.GetStudentThread(ctx, studentID, threadID); err != nil {
			return nil, MessageCursor{}, err
		}
	}
	for i := range messages {
		attachments, err := s.listStudentMessageAttachments(ctx, studentID, messages[i].ID)
		if err != nil {
			return nil, MessageCursor{}, err
		}
		messages[i].Attachments = attachments
	}
	var next MessageCursor
	if len(messages) == cursor.Limit {
		last := messages[len(messages)-1]
		next = MessageCursor{CreatedAt: last.CreatedAt, ID: last.ID, Limit: cursor.Limit}
	}
	return messages, next, nil
}

func (s *PostgresStore) FindMessageByIdempotency(ctx context.Context, senderID uuid.UUID, key string) (Thread, Message, error) {
	row := s.q.QueryRow(ctx, `
		SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at,
		       m.id,m.thread_id,m.sender_user_id,m.sender_role,m.message_kind,m.body_text,m.created_at
		FROM qa_messages m
		JOIN qa_threads q ON q.id=m.thread_id
		JOIN users u ON u.id=m.sender_user_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
		WHERE m.sender_user_id=$1 AND q.student_id=$1 AND m.idempotency_key=$2`, senderID, key)
	var thread Thread
	var message Message
	var completedAt *time.Time
	if err := row.Scan(
		&thread.ID, &thread.StudentID, &thread.Title, &thread.Status, &thread.Version, &thread.LastMessageAt, &thread.CreatedAt, &thread.UpdatedAt, &completedAt,
		&message.ID, &message.ThreadID, &message.SenderUserID, &message.SenderRole, &message.Kind, &message.Body, &message.CreatedAt,
	); err != nil {
		return Thread{}, Message{}, mapPostgresError(err)
	}
	thread.CompletedAt = utcPointer(completedAt)
	normalizeThreadTimes(&thread)
	message.CreatedAt = message.CreatedAt.UTC()
	attachments, err := s.listStudentMessageAttachments(ctx, thread.StudentID, message.ID)
	if err != nil {
		return Thread{}, Message{}, err
	}
	message.Attachments = attachments
	return thread, message, nil
}

func (s *PostgresStore) CreateThreadWithFirstMessage(ctx context.Context, studentID uuid.UUID, in CreateThreadInput, now time.Time) (Thread, Message, bool, error) {
	if _, err := s.q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, studentID.String()+":"+in.IdempotencyKey); err != nil {
		return Thread{}, Message{}, false, mapPostgresError(err)
	}
	if thread, message, err := s.FindMessageByIdempotency(ctx, studentID, in.IdempotencyKey); err == nil {
		if message.Kind != MessageKindInitial {
			return Thread{}, Message{}, false, ErrIdempotencyConflict
		}
		return thread, message, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Thread{}, Message{}, false, err
	}
	threadID, messageID := uuid.New(), uuid.New()
	thread, err := scanThread(s.q.QueryRow(ctx, `
		INSERT INTO qa_threads(id,student_id,title,status,version,last_message_at,created_at,updated_at)
		SELECT $1,u.id,$3,'pending',1,$4,$4,$4 FROM users u
		WHERE u.id=$2 AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
		RETURNING id,student_id,title,status,version,last_message_at,created_at,updated_at,completed_at`, threadID, studentID, in.Title, now))
	if err != nil {
		return Thread{}, Message{}, false, err
	}
	message, err := scanMessage(s.q.QueryRow(ctx, `
		INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at)
		VALUES($1,$2,$3,'student','initial',$4,$5,$6)
		RETURNING id,thread_id,sender_user_id,sender_role,message_kind,body_text,created_at`, messageID, thread.ID, studentID, in.Body, in.IdempotencyKey, now))
	if err != nil {
		return Thread{}, Message{}, false, err
	}
	return thread, message, true, nil
}

func (s *PostgresStore) LockStudentThread(ctx context.Context, studentID, threadID uuid.UUID) (Thread, error) {
	return scanThread(s.q.QueryRow(ctx, `
		SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at
		FROM qa_threads q
		JOIN users u ON u.id=q.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
		WHERE q.id=$1 AND q.student_id=$2 FOR UPDATE OF q`, threadID, studentID))
}

func (s *PostgresStore) AppendStudentMessage(ctx context.Context, thread Thread, studentID uuid.UUID, in AddMessageInput, next Status, now time.Time) (Thread, Message, error) {
	message, err := scanMessage(s.q.QueryRow(ctx, `
		INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at)
		VALUES($1,$2,$3,'student','student_follow_up',$4,$5,$6)
		RETURNING id,thread_id,sender_user_id,sender_role,message_kind,body_text,created_at`, uuid.New(), thread.ID, studentID, in.Body, in.IdempotencyKey, now))
	if err != nil {
		return Thread{}, Message{}, err
	}
	updated, err := scanThread(s.q.QueryRow(ctx, `
		UPDATE qa_threads SET status=$4,version=version+1,last_message_at=$5,updated_at=$5,
		completed_at=CASE WHEN $4='completed' THEN $5::timestamptz ELSE NULL END
		WHERE id=$1 AND student_id=$2 AND version=$3
		RETURNING id,student_id,title,status,version,last_message_at,created_at,updated_at,completed_at`, thread.ID, studentID, thread.Version, next, now))
	if err != nil {
		return Thread{}, Message{}, err
	}
	return updated, message, nil
}

func (s *PostgresStore) ActiveAdminID(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.q.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL LIMIT 1`).Scan(&id)
	return id, mapPostgresError(err)
}

func (s *PostgresStore) FindAdminMessageByIdempotency(ctx context.Context, adminID uuid.UUID, key string) (Thread, Message, error) {
	row := s.q.QueryRow(ctx, `SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at,m.id,m.thread_id,m.sender_user_id,m.sender_role,m.message_kind,m.body_text,m.created_at FROM qa_messages m JOIN qa_threads q ON q.id=m.thread_id JOIN users u ON u.id=m.sender_user_id AND u.role='admin' AND u.status='active' AND u.deleted_at IS NULL WHERE m.sender_user_id=$1 AND m.sender_role='admin' AND m.message_kind='admin_reply' AND m.idempotency_key=$2`, adminID, key)
	var thread Thread
	var message Message
	var completed *time.Time
	if err := row.Scan(&thread.ID, &thread.StudentID, &thread.Title, &thread.Status, &thread.Version, &thread.LastMessageAt, &thread.CreatedAt, &thread.UpdatedAt, &completed, &message.ID, &message.ThreadID, &message.SenderUserID, &message.SenderRole, &message.Kind, &message.Body, &message.CreatedAt); err != nil {
		return Thread{}, Message{}, mapPostgresError(err)
	}
	thread.CompletedAt = utcPointer(completed)
	normalizeThreadTimes(&thread)
	message.CreatedAt = message.CreatedAt.UTC()
	attachments, err := s.listAdminMessageAttachments(ctx, message.ID)
	if err != nil {
		return Thread{}, Message{}, err
	}
	message.Attachments = attachments
	return thread, message, nil
}

func (s *PostgresStore) LockAdminThread(ctx context.Context, threadID uuid.UUID) (Thread, error) {
	return scanThread(s.q.QueryRow(ctx, `SELECT q.id,q.student_id,q.title,q.status,q.version,q.last_message_at,q.created_at,q.updated_at,q.completed_at FROM qa_threads q JOIN users u ON u.id=q.student_id AND u.role='student' AND u.deleted_at IS NULL WHERE q.id=$1 FOR UPDATE OF q`, threadID))
}

func (s *PostgresStore) AppendAdminMessage(ctx context.Context, thread Thread, adminID uuid.UUID, in AddAdminMessageInput, next Status, now time.Time) (Thread, Message, error) {
	message, err := scanMessage(s.q.QueryRow(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at) SELECT $1,$2,u.id,'admin','admin_reply',$4,$5,$6 FROM users u WHERE u.id=$3 AND u.role='admin' AND u.status='active' AND u.deleted_at IS NULL RETURNING id,thread_id,sender_user_id,sender_role,message_kind,body_text,created_at`, uuid.New(), thread.ID, adminID, in.Body, in.IdempotencyKey, now))
	if err != nil {
		return Thread{}, Message{}, err
	}
	updated, err := scanThread(s.q.QueryRow(ctx, `UPDATE qa_threads SET status=$3,version=version+1,last_message_at=$4,updated_at=$4,completed_at=NULL WHERE id=$1 AND version=$2 RETURNING id,student_id,title,status,version,last_message_at,created_at,updated_at,completed_at`, thread.ID, thread.Version, next, now))
	if errors.Is(err, ErrNotFound) {
		return Thread{}, Message{}, ErrThreadConflict
	}
	if err != nil {
		return Thread{}, Message{}, err
	}
	return updated, message, nil
}

func (s *PostgresStore) UpdateAdminThreadStatus(ctx context.Context, thread Thread, next Status, now time.Time) (Thread, error) {
	updated, err := scanThread(s.q.QueryRow(ctx, `UPDATE qa_threads SET status=$3,version=version+1,updated_at=$4,completed_at=CASE WHEN $3='completed' THEN $4::timestamptz ELSE NULL END WHERE id=$1 AND version=$2 RETURNING id,student_id,title,status,version,last_message_at,created_at,updated_at,completed_at`, thread.ID, thread.Version, next, now))
	if errors.Is(err, ErrNotFound) {
		return Thread{}, ErrThreadConflict
	}
	return updated, err
}

func (s *PostgresStore) InsertTeacherNote(ctx context.Context, threadID, adminID uuid.UUID, body string, now time.Time) (TeacherNote, error) {
	var n TeacherNote
	err := s.q.QueryRow(ctx, `INSERT INTO teacher_notes(id,thread_id,author_user_id,body_text,created_at) SELECT $1,q.id,a.id,$4,$5 FROM qa_threads q JOIN users a ON a.id=$3 AND a.role='admin' AND a.status='active' AND a.deleted_at IS NULL WHERE q.id=$2 RETURNING id,thread_id,author_user_id,body_text,created_at`, uuid.New(), threadID, adminID, body, now).Scan(&n.ID, &n.ThreadID, &n.AuthorUserID, &n.Body, &n.CreatedAt)
	if err != nil {
		return TeacherNote{}, mapPostgresError(err)
	}
	n.CreatedAt = n.CreatedAt.UTC()
	return n, nil
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

type lockedQAAttachment struct {
	id, owner                  uuid.UUID
	purpose, state, mime, name string
	size                       int64
}

var qaAttachmentLimits = map[string]int64{
	"image/jpeg": 20 << 20, "image/png": 20 << 20, "image/webp": 20 << 20, "image/gif": 20 << 20,
	"application/pdf": 50 << 20,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   30 << 20,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         30 << 20,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": 30 << 20,
	"text/plain": 10 << 20, "text/markdown": 10 << 20,
}

// ValidateForMessage runs on the caller's pgx transaction. The one locked
// query obtains every security-relevant fact from PostgreSQL and holds all
// matching versions until the message/binding transaction commits.
func (s *PostgresStore) ValidateForMessage(ctx context.Context, actor Principal, inputs []AttachmentInput) ([]Attachment, error) {
	if len(inputs) == 0 {
		return []Attachment{}, nil
	}
	if len(inputs) > 20 || actor.User.ID == uuid.Nil || actor.User.Status != "active" || (actor.User.Role != "student" && actor.User.Role != "admin") {
		return nil, ErrAttachmentLimit
	}
	ids := make([]uuid.UUID, len(inputs))
	requested := make(map[uuid.UUID]AttachmentInput, len(inputs))
	positions := make(map[int]struct{}, len(inputs))
	for i, in := range inputs {
		if in.FileVersionID == uuid.Nil || in.SortPosition < 0 || in.SortPosition > 19 {
			return nil, ErrInvalidInput
		}
		if _, ok := requested[in.FileVersionID]; ok {
			return nil, ErrInvalidInput
		}
		if _, ok := positions[in.SortPosition]; ok {
			return nil, ErrInvalidInput
		}
		ids[i] = in.FileVersionID
		requested[in.FileVersionID] = in
		positions[in.SortPosition] = struct{}{}
	}
	rows, err := s.q.Query(ctx, `SELECT fv.id,f.created_by,fv.purpose,fv.processing_state,COALESCE(fv.detected_mime,''),fv.size_bytes,fv.display_name
	 FROM file_versions fv JOIN files f ON f.id=fv.file_id AND f.deleted_at IS NULL
	 WHERE fv.id=ANY($1) ORDER BY fv.id FOR UPDATE OF fv,f`, ids)
	if err != nil {
		return nil, mapPostgresError(err)
	}
	defer rows.Close()
	locked := make(map[uuid.UUID]lockedQAAttachment, len(ids))
	for rows.Next() {
		var a lockedQAAttachment
		if err = rows.Scan(&a.id, &a.owner, &a.purpose, &a.state, &a.mime, &a.size, &a.name); err != nil {
			return nil, mapPostgresError(err)
		}
		locked[a.id] = a
	}
	if err = rows.Err(); err != nil {
		return nil, mapPostgresError(err)
	}
	if len(locked) != len(requested) {
		return nil, ErrForbidden
	}
	out := make([]Attachment, 0, len(inputs))
	images := 0
	var total int64
	for id, in := range requested {
		a := locked[id]
		if a.owner != actor.User.ID || a.purpose != "qa_attachment" {
			return nil, ErrForbidden
		}
		if a.state != "ready" {
			return nil, ErrAttachmentNotReady
		}
		mime := strings.ToLower(a.mime)
		limit, ok := qaAttachmentLimits[mime]
		if !ok {
			return nil, ErrInvalidInput
		}
		if a.size < 1 || a.size > limit {
			return nil, ErrAttachmentLimit
		}
		total += a.size
		if total > 100<<20 {
			return nil, ErrAttachmentLimit
		}
		if strings.HasPrefix(mime, "image/") {
			images++
			if images > 10 {
				return nil, ErrAttachmentLimit
			}
		}
		name := strings.TrimSpace(a.name)
		if !validQAAttachmentName(name) {
			return nil, ErrInvalidInput
		}
		out = append(out, Attachment{FileVersionID: id, SortPosition: in.SortPosition, DisplayName: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SortPosition < out[j].SortPosition })
	return out, nil
}

func validQAAttachmentName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 || name == "." || name == ".." || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\\x00\r\n") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return filepath.Ext(name) != ""
}

func (s *PostgresStore) BindMessageAttachments(ctx context.Context, messageID uuid.UUID, attachments []Attachment) error {
	if len(attachments) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(attachments))
	positions := make([]int16, len(attachments))
	names := make([]string, len(attachments))
	for i, a := range attachments {
		ids[i] = a.FileVersionID
		positions[i] = int16(a.SortPosition)
		names[i] = a.DisplayName
	}
	tag, err := s.q.Exec(ctx, `INSERT INTO qa_message_files(message_id,file_version_id,sort_position,display_name)
	 SELECT $1,x.id,x.pos,x.name FROM unnest($2::uuid[],$3::smallint[],$4::text[]) AS x(id,pos,name)`, messageID, ids, positions, names)
	if err != nil {
		return mapPostgresError(err)
	}
	if tag.RowsAffected() != int64(len(attachments)) {
		return ErrInvalidInput
	}
	return nil
}

func (s *PostgresStore) listAdminMessageAttachments(ctx context.Context, messageID uuid.UUID) ([]Attachment, error) {
	rows, err := s.q.Query(ctx, `SELECT mf.file_version_id,mf.sort_position,mf.display_name FROM qa_message_files mf JOIN qa_messages m ON m.id=mf.message_id JOIN file_versions fv ON fv.id=mf.file_version_id AND fv.purpose='qa_attachment' AND fv.processing_state='ready' JOIN files f ON f.id=fv.file_id AND f.created_by=m.sender_user_id AND f.deleted_at IS NULL WHERE mf.message_id=$1 ORDER BY mf.sort_position,mf.file_version_id`, messageID)
	if err != nil {
		return nil, mapPostgresError(err)
	}
	defer rows.Close()
	out := make([]Attachment, 0)
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.FileVersionID, &a.SortPosition, &a.DisplayName); err != nil {
			return nil, mapPostgresError(err)
		}
		out = append(out, a)
	}
	return out, mapPostgresError(rows.Err())
}

func (s *PostgresStore) requireActiveStudent(ctx context.Context, studentID uuid.UUID) error {
	var ok bool
	err := s.q.QueryRow(ctx, `SELECT true FROM users WHERE id=$1 AND role='student' AND status='active' AND deleted_at IS NULL`, studentID).Scan(&ok)
	return mapPostgresError(err)
}

func (s *PostgresStore) listStudentMessageAttachments(ctx context.Context, studentID, messageID uuid.UUID) ([]Attachment, error) {
	rows, err := s.q.Query(ctx, `
		SELECT mf.file_version_id,mf.sort_position,mf.display_name
		FROM qa_message_files mf
		JOIN qa_messages m ON m.id=mf.message_id
		JOIN qa_threads q ON q.id=m.thread_id
		JOIN users u ON u.id=q.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
		JOIN file_versions fv ON fv.id=mf.file_version_id
		  AND fv.purpose='qa_attachment' AND fv.processing_state='ready'
		JOIN files f ON f.id=fv.file_id AND f.deleted_at IS NULL
		LEFT JOIN users sender_admin ON sender_admin.id=m.sender_user_id AND sender_admin.role='admin' AND sender_admin.status='active' AND sender_admin.deleted_at IS NULL
		WHERE mf.message_id=$1 AND q.student_id=$2
		  AND ((m.sender_role='student' AND m.sender_user_id=q.student_id AND m.message_kind IN ('initial','student_follow_up') AND f.created_by=q.student_id)
		    OR (m.sender_role='admin' AND sender_admin.id IS NOT NULL AND m.message_kind='admin_reply' AND f.created_by=m.sender_user_id))
		ORDER BY mf.sort_position,mf.file_version_id`, messageID, studentID)
	if err != nil {
		return nil, mapPostgresError(err)
	}
	defer rows.Close()
	attachments := make([]Attachment, 0)
	for rows.Next() {
		var attachment Attachment
		if err := rows.Scan(&attachment.FileVersionID, &attachment.SortPosition, &attachment.DisplayName); err != nil {
			return nil, mapPostgresError(err)
		}
		attachments = append(attachments, attachment)
	}
	return attachments, mapPostgresError(rows.Err())
}

type rowScanner interface{ Scan(...any) error }

func scanThread(row rowScanner) (Thread, error) {
	var thread Thread
	var completedAt *time.Time
	if err := row.Scan(&thread.ID, &thread.StudentID, &thread.Title, &thread.Status, &thread.Version, &thread.LastMessageAt, &thread.CreatedAt, &thread.UpdatedAt, &completedAt); err != nil {
		return Thread{}, mapPostgresError(err)
	}
	thread.CompletedAt = utcPointer(completedAt)
	normalizeThreadTimes(&thread)
	return thread, nil
}

func scanMessage(row rowScanner) (Message, error) {
	var message Message
	if err := row.Scan(&message.ID, &message.ThreadID, &message.SenderUserID, &message.SenderRole, &message.Kind, &message.Body, &message.CreatedAt); err != nil {
		return Message{}, mapPostgresError(err)
	}
	message.CreatedAt = message.CreatedAt.UTC()
	return message, nil
}

func normalizeThreadTimes(thread *Thread) {
	thread.LastMessageAt = thread.LastMessageAt.UTC()
	thread.CreatedAt = thread.CreatedAt.UTC()
	thread.UpdatedAt = thread.UpdatedAt.UTC()
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func mapPostgresError(err error) error {
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
			return ErrIdempotencyConflict
		case "23503", "23514", "22001", "22P02":
			return ErrInvalidInput
		}
	}
	return fmt.Errorf("qanda postgres: %w", err)
}

var (
	_ Store      = (*PostgresStore)(nil)
	_ TxStore    = (*PostgresStore)(nil)
	_ UnitOfWork = (*PostgresUnitOfWork)(nil)
)
