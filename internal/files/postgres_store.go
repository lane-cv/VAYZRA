package files

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/audit"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) CreateSession(ctx context.Context, u UploadSession) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO upload_sessions
        (id,actor_user_id,object_key,minio_upload_id,display_name,declared_mime,expected_size,expected_sha256,state,expires_at,created_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		u.ID, u.ActorUserID, u.ObjectKey, u.MinIOUploadID, u.DisplayName, u.DeclaredMIME, u.ExpectedSize, u.ExpectedSHA256, u.State, u.ExpiresAt, u.CreatedAt)
	return err
}

func (s *PostgresStore) GetSession(ctx context.Context, id, actor uuid.UUID) (UploadSession, []UploadPart, error) {
	u, err := scanSession(s.pool.QueryRow(ctx, sessionSelect+` WHERE id=$1 AND actor_user_id=$2`, id, actor))
	if err != nil {
		return UploadSession{}, nil, mapStoreError(err)
	}
	parts, err := s.listParts(ctx, s.pool, id)
	return u, parts, err
}

func (s *PostgresStore) AdmitPart(ctx context.Context, id, actor uuid.UUID, number int, size int64, hash string, now time.Time) (UploadSession, *UploadPart, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadSession{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	u, err := scanSession(tx.QueryRow(ctx, sessionSelect+` WHERE id=$1 AND actor_user_id=$2 FOR UPDATE`, id, actor))
	if err != nil {
		return UploadSession{}, nil, mapStoreError(err)
	}
	if now.After(u.ExpiresAt) {
		return UploadSession{}, nil, ErrUploadExpired
	}
	if u.State != UploadOpen {
		return UploadSession{}, nil, ErrUploadConflict
	}
	part, err := scanPart(tx.QueryRow(ctx, partSelect+` WHERE upload_session_id=$1 AND part_number=$2`, id, number))
	if err == nil {
		if part.Size != size || part.SHA256 != hash {
			return UploadSession{}, nil, ErrUploadPartConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return UploadSession{}, nil, err
		}
		return u, &part, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UploadSession{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadSession{}, nil, err
	}
	return u, nil, nil
}

func (s *PostgresStore) RecordPart(ctx context.Context, id uuid.UUID, p UploadPart) (UploadPart, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadPart{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	u, err := scanSession(tx.QueryRow(ctx, sessionSelect+` WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return UploadPart{}, mapStoreError(err)
	}
	if u.State != UploadOpen {
		return UploadPart{}, ErrUploadConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO upload_parts (upload_session_id,part_number,size_bytes,sha256,etag,created_at)
        VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (upload_session_id,part_number) DO NOTHING`, id, p.Number, p.Size, p.SHA256, p.ETag, p.CreatedAt)
	if err != nil {
		return UploadPart{}, err
	}
	stored, err := scanPart(tx.QueryRow(ctx, partSelect+` WHERE upload_session_id=$1 AND part_number=$2`, id, p.Number))
	if err != nil {
		return UploadPart{}, err
	}
	if stored.Size != p.Size || stored.SHA256 != p.SHA256 {
		return UploadPart{}, ErrUploadPartConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadPart{}, err
	}
	return stored, nil
}

func (s *PostgresStore) BeginCompletion(ctx context.Context, id, actor uuid.UUID, now time.Time) (UploadSession, []UploadPart, *CompletedUpload, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadSession{}, nil, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	u, err := scanSession(tx.QueryRow(ctx, sessionSelect+` WHERE id=$1 AND actor_user_id=$2 FOR UPDATE`, id, actor))
	if err != nil {
		return UploadSession{}, nil, nil, mapStoreError(err)
	}
	if u.State == UploadCompleted {
		completed, err := scanCompleted(tx.QueryRow(ctx, `SELECT f.id,fv.id,fv.processing_state FROM file_versions fv JOIN files f ON f.id=fv.file_id WHERE fv.object_key=$1`, u.ObjectKey))
		if err != nil {
			return UploadSession{}, nil, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return UploadSession{}, nil, nil, err
		}
		return u, nil, &completed, nil
	}
	if now.After(u.ExpiresAt) {
		return UploadSession{}, nil, nil, ErrUploadExpired
	}
	if u.State != UploadOpen && u.State != UploadCompleting {
		return UploadSession{}, nil, nil, ErrUploadConflict
	}
	if u.State == UploadOpen {
		if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET state='completing' WHERE id=$1`, id); err != nil {
			return UploadSession{}, nil, nil, err
		}
		u.State = UploadCompleting
	}
	parts, err := s.listParts(ctx, tx, id)
	if err != nil {
		return UploadSession{}, nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadSession{}, nil, nil, err
	}
	return u, parts, nil, nil
}

func (s *PostgresStore) ReopenCompletion(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE upload_sessions SET state='open' WHERE id=$1 AND state='completing'`)
	return err
}

func (s *PostgresStore) FinishCompletion(ctx context.Context, u UploadSession, actor Principal) (CompletedUpload, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CompletedUpload{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := scanSession(tx.QueryRow(ctx, sessionSelect+` WHERE id=$1 FOR UPDATE`, u.ID))
	if err != nil {
		return CompletedUpload{}, mapStoreError(err)
	}
	if locked.State == UploadCompleted {
		completed, err := scanCompleted(tx.QueryRow(ctx, `SELECT f.id,fv.id,fv.processing_state FROM file_versions fv JOIN files f ON f.id=fv.file_id WHERE fv.object_key=$1`, locked.ObjectKey))
		if err != nil {
			return CompletedUpload{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return CompletedUpload{}, err
		}
		return completed, nil
	}
	if locked.State != UploadCompleting {
		return CompletedUpload{}, ErrUploadConflict
	}
	var completed CompletedUpload
	if err := tx.QueryRow(ctx, `INSERT INTO files (created_by) VALUES ($1) RETURNING id`, locked.ActorUserID).Scan(&completed.FileID); err != nil {
		return CompletedUpload{}, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO file_versions
        (file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by)
        VALUES ($1,1,$2,$3,$4,$5,$6,'pending_scan',$7) RETURNING id,processing_state`,
		completed.FileID, locked.ObjectKey, locked.DisplayName, locked.DeclaredMIME, locked.ExpectedSize, locked.ExpectedSHA256, locked.ActorUserID).Scan(&completed.FileVersionID, &completed.ProcessingState); err != nil {
		return CompletedUpload{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET state='completed' WHERE id=$1`, locked.ID); err != nil {
		return CompletedUpload{}, err
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{ActorUserID: actor.User.ID, Action: "file.uploaded", TargetType: "file_version", TargetID: completed.FileVersionID.String(), Metadata: map[string]any{}, RequestID: actor.RequestID, IP: actor.IP}); err != nil {
		return CompletedUpload{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CompletedUpload{}, err
	}
	return completed, nil
}

func (s *PostgresStore) CancelSession(ctx context.Context, id, actor uuid.UUID, state UploadState) (UploadSession, error) {
	if state != UploadCancelled {
		return UploadSession{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	u, err := scanSession(tx.QueryRow(ctx, sessionSelect+` WHERE id=$1 AND actor_user_id=$2 FOR UPDATE`, id, actor))
	if err != nil {
		return UploadSession{}, mapStoreError(err)
	}
	if u.State == UploadCompleted || u.State == UploadExpired {
		return UploadSession{}, ErrUploadConflict
	}
	if u.State != UploadCancelled {
		if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET state='cancelled' WHERE id=$1`, id); err != nil {
			return UploadSession{}, err
		}
		u.State = UploadCancelled
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadSession{}, err
	}
	return u, nil
}

func (s *PostgresStore) ClaimCleanup(ctx context.Context, cutoff time.Time, limit int) ([]UploadSession, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, sessionSelect+` WHERE expires_at <= $1 AND state IN ('open','expired','cancelled','completing') AND NOT EXISTS (SELECT 1 FROM file_versions WHERE file_versions.object_key=upload_sessions.object_key) ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	var result []UploadSession
	for rows.Next() {
		u, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PostgresStore) ConfirmCleanup(ctx context.Context, id uuid.UUID) (UploadSession, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	u, err := scanSession(tx.QueryRow(ctx, sessionSelect+` WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return UploadSession{}, mapStoreError(err)
	}
	var unreferenced bool
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM file_versions WHERE object_key=$1)`, u.ObjectKey).Scan(&unreferenced); err != nil {
		return UploadSession{}, err
	}
	eligible := u.State == UploadOpen || u.State == UploadExpired || u.State == UploadCancelled || u.State == UploadCompleting
	if !unreferenced || !eligible {
		return UploadSession{}, ErrUploadConflict
	}
	if u.State == UploadOpen || u.State == UploadCompleting {
		if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET state='expired' WHERE id=$1`, id); err != nil {
			return UploadSession{}, err
		}
		u.State = UploadExpired
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadSession{}, err
	}
	return u, nil
}
func (s *PostgresStore) FinishCleanup(ctx context.Context, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state UploadState
	var referenced bool
	if err := tx.QueryRow(ctx, `SELECT state,EXISTS(SELECT 1 FROM file_versions WHERE file_versions.object_key=upload_sessions.object_key) FROM upload_sessions WHERE id=$1 FOR UPDATE`, id).Scan(&state, &referenced); err != nil {
		return mapStoreError(err)
	}
	if referenced || (state != UploadExpired && state != UploadCancelled) {
		return ErrUploadConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM upload_parts WHERE upload_session_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM upload_sessions WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type rowScanner interface{ Scan(...any) error }
type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const sessionSelect = `SELECT id,actor_user_id,object_key,minio_upload_id,display_name,declared_mime,expected_size,expected_sha256,state,expires_at,created_at FROM upload_sessions`
const partSelect = `SELECT upload_session_id,part_number,size_bytes,sha256,etag,created_at FROM upload_parts`

func scanSession(row rowScanner) (UploadSession, error) {
	var u UploadSession
	err := row.Scan(&u.ID, &u.ActorUserID, &u.ObjectKey, &u.MinIOUploadID, &u.DisplayName, &u.DeclaredMIME, &u.ExpectedSize, &u.ExpectedSHA256, &u.State, &u.ExpiresAt, &u.CreatedAt)
	return u, err
}

func scanPart(row rowScanner) (UploadPart, error) {
	var p UploadPart
	err := row.Scan(&p.SessionID, &p.Number, &p.Size, &p.SHA256, &p.ETag, &p.CreatedAt)
	return p, err
}

func scanCompleted(row rowScanner) (CompletedUpload, error) {
	var c CompletedUpload
	err := row.Scan(&c.FileID, &c.FileVersionID, &c.ProcessingState)
	return c, err
}

func (s *PostgresStore) listParts(ctx context.Context, q queryer, id uuid.UUID) ([]UploadPart, error) {
	rows, err := q.Query(ctx, partSelect+` WHERE upload_session_id=$1 ORDER BY part_number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UploadPart
	for rows.Next() {
		p, err := scanPart(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func mapStoreError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

var _ UploadStore = (*PostgresStore)(nil)
