package students

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

type PostgresUnitOfWork struct{ pool *pgxpool.Pool }

func NewPostgresUnitOfWork(pool *pgxpool.Pool) *PostgresUnitOfWork {
	return &PostgresUnitOfWork{pool: pool}
}
func (u *PostgresUnitOfWork) WithinTx(ctx context.Context, fn func(UserStore, SessionStore, audit.Writer) error) error {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := fn(&txUsers{tx}, &txSessions{tx}, audit.NewPostgresWriter(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type txUsers struct{ tx pgx.Tx }

func (s *txUsers) FindByID(ctx context.Context, id uuid.UUID) (auth.User, error) {
	return scanUser(s.tx.QueryRow(ctx, `SELECT id, username, display_name, role, status, password_hash, must_change_password, created_at FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id))
}
func (s *txUsers) Create(ctx context.Context, p auth.CreateUserParams) (auth.User, error) {
	return scanUser(s.tx.QueryRow(ctx, `INSERT INTO users (username, display_name, role, status, password_hash, must_change_password) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, username, display_name, role, status, password_hash, must_change_password, created_at`, normalizeUsername(p.Username), p.DisplayName, p.Role, p.Status, p.PasswordHash, p.MustChangePassword))
}
func (s *txUsers) UpdatePassword(ctx context.Context, id uuid.UUID, hash string, must bool) error {
	tag, err := s.tx.Exec(ctx, `UPDATE users SET password_hash=$2, must_change_password=$3, updated_at=now() WHERE id=$1 AND deleted_at IS NULL`, id, hash, must)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() != 1 {
		return auth.ErrNotFound
	}
	return nil
}
func (s *txUsers) SetStatus(ctx context.Context, id uuid.UUID, status auth.Status) error {
	tag, err := s.tx.Exec(ctx, `UPDATE users SET status=$2, updated_at=now() WHERE id=$1 AND deleted_at IS NULL`, id, status)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() != 1 {
		return auth.ErrNotFound
	}
	return nil
}

type txSessions struct{ tx pgx.Tx }

func (s *txSessions) RevokeAllForUser(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := s.tx.Exec(ctx, `UPDATE sessions SET revoked_at=now(), revoke_reason=$2 WHERE user_id=$1 AND revoked_at IS NULL`, id, reason)
	return mapError(err)
}
func scanUser(row pgx.Row) (auth.User, error) {
	var u auth.User
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Status, &u.PasswordHash, &u.MustChangePassword, &u.CreatedAt); err != nil {
		return auth.User{}, mapError(err)
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return u, nil
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && (pgErr.ConstraintName == "users_username_active_key" || pgErr.ConstraintName == "users_single_admin_key") {
		return auth.ErrConflict
	}
	return err
}
