package operations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const admissionLockTimeoutSQL = `SET LOCAL lock_timeout = '250ms'`
const clearAdmissionLockTimeoutSQL = `SET LOCAL lock_timeout = 0`

// AdmitClaim closes the normal-mode check/claim race inside a caller-owned
// PostgreSQL transaction. The transaction-level shared lock is released by
// commit or rollback, immediately after the durable claim.
func AdmitClaim(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, admissionLockTimeoutSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock_shared($1)`,
		operationsAdvisoryKey,
	); err != nil {
		if admissionLockTimedOut(err) {
			return ErrLeaseHeld
		}
		return err
	}
	if _, err := tx.Exec(ctx, clearAdmissionLockTimeoutSQL); err != nil {
		return err
	}
	var mode string
	if err := tx.QueryRow(
		ctx,
		`SELECT mode FROM operational_modes WHERE singleton_id=true`,
	).Scan(&mode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if mode != "normal" {
		return ErrLeaseHeld
	}
	return nil
}

func admissionLockTimedOut(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}
