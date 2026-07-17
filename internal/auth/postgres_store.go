package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresUserStore struct {
	pool *pgxpool.Pool
}

func NewPostgresUserStore(pool *pgxpool.Pool) *PostgresUserStore {
	return &PostgresUserStore{pool: pool}
}

func (s *PostgresUserStore) FindByUsername(ctx context.Context, username string) (User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, display_name, role, status, password_hash, must_change_password, created_at
		FROM users
		WHERE lower(username) = $1 AND deleted_at IS NULL`, normalizeUsername(username))
	return scanUser(row)
}

func (s *PostgresUserStore) FindByID(ctx context.Context, id uuid.UUID) (User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, display_name, role, status, password_hash, must_change_password, created_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanUser(row)
}

func (s *PostgresUserStore) Create(ctx context.Context, params CreateUserParams) (User, error) {
	status := params.Status
	if status == "" {
		status = StatusActive
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO users (username, display_name, role, status, password_hash, must_change_password)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, username, display_name, role, status, password_hash, must_change_password, created_at`,
		normalizeUsername(params.Username), params.DisplayName, params.Role, status, params.PasswordHash, params.MustChangePassword)
	return scanUser(row)
}

func (s *PostgresUserStore) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string, mustChangePassword bool) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE users
		SET password_hash = $2, must_change_password = $3, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id, passwordHash, mustChangePassword)
	if err != nil {
		return mapStoreError(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresUserStore) SetStatus(ctx context.Context, id uuid.UUID, status Status) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE users
		SET status = $2, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id, status)
	if err != nil {
		return mapStoreError(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresUserStore) ListStudents(ctx context.Context, limit int, afterID uuid.UUID) ([]User, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	var cursor any
	if afterID != uuid.Nil {
		cursor = afterID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, username, display_name, role, status, password_hash, must_change_password, created_at
		FROM users
		WHERE role = 'student' AND deleted_at IS NULL AND ($2::uuid IS NULL OR id > $2)
		ORDER BY id
		LIMIT $1`, limit, cursor)
	if err != nil {
		return nil, mapStoreError(err)
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, mapStoreError(err)
	}
	return users, nil
}

type PostgresSessionStore struct {
	pool *pgxpool.Pool
}

func NewPostgresSessionStore(pool *pgxpool.Pool) *PostgresSessionStore {
	return &PostgresSessionStore{pool: pool}
}

func (s *PostgresSessionStore) Create(ctx context.Context, params CreateSessionParams) error {
	if params.ID == uuid.Nil {
		params.ID = uuid.New()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (
			id, user_id, token_hash, user_agent, ip, created_at, last_seen_at, idle_expires_at, absolute_expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		params.ID, params.UserID, params.TokenHash[:], params.UserAgent, params.IP, params.CreatedAt.UTC(), params.LastSeenAt.UTC(),
		params.IdleExpiresAt.UTC(), params.AbsoluteExpiresAt.UTC())
	if err != nil {
		return mapStoreError(err)
	}
	return nil
}

func (s *PostgresSessionStore) FindActiveByTokenHash(ctx context.Context, tokenHash [32]byte, now time.Time) (Session, User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT
			s.id, s.user_id, s.token_hash, s.user_agent, s.ip, s.created_at, s.last_seen_at,
			s.idle_expires_at, s.absolute_expires_at, s.revoked_at, s.revoke_reason,
			u.id, u.username, u.display_name, u.role, u.status, u.password_hash, u.must_change_password, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
			AND s.revoked_at IS NULL
			AND s.idle_expires_at > $2
			AND s.absolute_expires_at > $2
			AND u.deleted_at IS NULL`, tokenHash[:], now.UTC())

	var session Session
	var tokenHashBytes []byte
	var user User
	err := row.Scan(
		&session.ID, &session.UserID, &tokenHashBytes, &session.UserAgent, &session.IP, &session.CreatedAt, &session.LastSeenAt,
		&session.IdleExpiresAt, &session.AbsoluteExpiresAt, &session.RevokedAt, &session.RevokeReason,
		&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Status, &user.PasswordHash, &user.MustChangePassword, &user.CreatedAt,
	)
	if err != nil {
		return Session{}, User{}, mapStoreError(err)
	}
	if len(tokenHashBytes) != len(session.TokenHash) {
		return Session{}, User{}, fmt.Errorf("scan session token hash: unexpected length")
	}
	copy(session.TokenHash[:], tokenHashBytes)
	return normalizeSessionUTC(session), normalizeUserUTC(user), nil
}

func (s *PostgresSessionStore) Touch(ctx context.Context, id uuid.UUID, lastSeenAt, idleExpiresAt time.Time) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET
			last_seen_at = CASE WHEN last_seen_at <= $2 - interval '5 minutes' THEN $2 ELSE last_seen_at END,
			idle_expires_at = CASE WHEN last_seen_at <= $2 - interval '5 minutes' THEN LEAST($3, absolute_expires_at) ELSE idle_expires_at END
		WHERE id = $1 AND revoked_at IS NULL AND idle_expires_at > $2 AND absolute_expires_at > $2`,
		id, lastSeenAt.UTC(), idleExpiresAt.UTC())
	if err != nil {
		return mapStoreError(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresSessionStore) Revoke(ctx context.Context, id uuid.UUID, reason string) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = now(), revoke_reason = $2
		WHERE id = $1 AND revoked_at IS NULL`, id, reason)
	if err != nil {
		return mapStoreError(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresSessionStore) RevokeAllForUser(ctx context.Context, userID uuid.UUID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = now(), revoke_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL`, userID, reason)
	return mapStoreError(err)
}

func (s *PostgresSessionStore) RevokeAllExceptForUser(ctx context.Context, userID, exceptID uuid.UUID, now time.Time, reason string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapStoreError(err)
	}
	defer tx.Rollback(context.Background())

	var retainedID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM sessions
		WHERE id = $1
			AND user_id = $2
			AND revoked_at IS NULL
			AND idle_expires_at > $3
			AND absolute_expires_at > $3
		FOR UPDATE`, exceptID, userID, now.UTC()).Scan(&retainedID); err != nil {
		return mapStoreError(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = now(), revoke_reason = $3
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`, userID, retainedID, reason); err != nil {
		return mapStoreError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapStoreError(err)
	}
	return nil
}

func (s *PostgresSessionStore) RecordLoginEvent(ctx context.Context, event LoginEvent) error {
	var userID any
	if event.UserID != nil {
		userID = *event.UserID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO login_events (user_id, username, success, reason, ip, user_agent, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		userID, normalizeUsername(event.Username), event.Success, event.Reason, event.IP, event.UserAgent, event.OccurredAt.UTC())
	return mapStoreError(err)
}

func (s *PostgresSessionStore) RotatePassword(ctx context.Context, params PasswordRotationParams) error {
	if params.UserID == uuid.Nil || params.ReplacementSession.UserID != params.UserID || params.ReplacementSession.ID == uuid.Nil {
		return ErrConflict
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapStoreError(err)
	}
	defer tx.Rollback(context.Background())

	var currentHash string
	var status Status
	if err := tx.QueryRow(ctx, `
		SELECT password_hash, status
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`, params.UserID).Scan(&currentHash, &status); err != nil {
		return mapStoreError(err)
	}
	if status != StatusActive || subtle.ConstantTimeCompare([]byte(currentHash), []byte(params.ExpectedPasswordHash)) != 1 {
		return ErrConflict
	}
	result, err := tx.Exec(ctx, `
		UPDATE users
		SET password_hash = $2, must_change_password = $3, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, params.UserID, params.PasswordHash, params.MustChangePassword)
	if err != nil {
		return mapStoreError(err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = now(), revoke_reason = 'password changed'
		WHERE user_id = $1 AND revoked_at IS NULL`, params.UserID); err != nil {
		return mapStoreError(err)
	}
	p := params.ReplacementSession
	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (
			id, user_id, token_hash, user_agent, ip, created_at, last_seen_at, idle_expires_at, absolute_expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.ID, p.UserID, p.TokenHash[:], p.UserAgent, p.IP, p.CreatedAt.UTC(), p.LastSeenAt.UTC(), p.IdleExpiresAt.UTC(), p.AbsoluteExpiresAt.UTC()); err != nil {
		return mapStoreError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapStoreError(err)
	}
	return nil
}
func scanUser(row pgx.Row) (User, error) {
	var user User
	if err := row.Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Status, &user.PasswordHash, &user.MustChangePassword, &user.CreatedAt,
	); err != nil {
		return User{}, mapStoreError(err)
	}
	return normalizeUserUTC(user), nil
}

func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func normalizeUserUTC(user User) User {
	user.CreatedAt = user.CreatedAt.UTC()
	return user
}

func normalizeSessionUTC(session Session) Session {
	session.CreatedAt = session.CreatedAt.UTC()
	session.LastSeenAt = session.LastSeenAt.UTC()
	session.IdleExpiresAt = session.IdleExpiresAt.UTC()
	session.AbsoluteExpiresAt = session.AbsoluteExpiresAt.UTC()
	if session.RevokedAt != nil {
		utc := session.RevokedAt.UTC()
		session.RevokedAt = &utc
	}
	return session
}

func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "users_username_active_key" || pgErr.ConstraintName == "users_single_admin_key") {
		return ErrConflict
	}
	return err
}
