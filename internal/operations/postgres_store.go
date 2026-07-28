package operations

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
)

const operationsAdvisoryKey int64 = 845103120

var errStoreClosed = errors.New("operations store closed")

type leaseSession struct {
	mu               sync.Mutex
	conn             *pgxpool.Conn
	ownerID          uuid.UUID
	timer            *time.Timer
	expiryGeneration uint64
	released         bool
}

type PostgresStore struct {
	pool *pgxpool.Pool

	mu       sync.Mutex
	sessions map[[32]byte]*leaseSession
	closed   bool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{
		pool:     pool,
		sessions: make(map[[32]byte]*leaseSession),
	}
}

func (s *PostgresStore) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	var updatedBy *uuid.UUID
	err := s.pool.QueryRow(ctx, `
SELECT version,site_name,site_announcement,soft_delete_retention_days,
       audit_retention_days,operational_sample_retention_days,
       backup_hour,backup_minute,backup_timezone,
       disk_warning_percent,disk_critical_percent,
       ai_error_warning_percent,ai_error_critical_percent,
       processing_queue_warning,processing_queue_critical,
       updated_by,updated_at
FROM system_settings
WHERE singleton_id=true`).Scan(
		&settings.Version, &settings.SiteName, &settings.SiteAnnouncement,
		&settings.SoftDeleteRetentionDays, &settings.AuditRetentionDays,
		&settings.OperationalSampleRetentionDays, &settings.BackupHour,
		&settings.BackupMinute, &settings.BackupTimezone,
		&settings.DiskWarningPercent, &settings.DiskCriticalPercent,
		&settings.AIErrorWarningPercent, &settings.AIErrorCriticalPercent,
		&settings.ProcessingQueueWarning, &settings.ProcessingQueueCritical,
		&updatedBy, &settings.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrConflict
	}
	if err != nil {
		return Settings{}, err
	}
	if updatedBy != nil {
		settings.UpdatedBy = *updatedBy
	}
	settings.UpdatedAt = settings.UpdatedAt.UTC()
	return settings, nil
}

func (s *PostgresStore) UpdateSettings(ctx context.Context, principal Principal, settings Settings) (Settings, error) {
	if err := authorizeSettings(principal); err != nil {
		return Settings{}, err
	}
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback(ctx)

	var currentVersion int64
	if err := tx.QueryRow(ctx, `
SELECT version FROM system_settings WHERE singleton_id=true FOR UPDATE`).
		Scan(&currentVersion); err != nil {
		return Settings{}, mapSettingsError(err)
	}
	if currentVersion != settings.Version {
		return Settings{}, ErrConflict
	}

	var updated Settings
	err = tx.QueryRow(ctx, `
UPDATE system_settings
SET site_name=$1,site_announcement=$2,soft_delete_retention_days=$3,
    audit_retention_days=$4,operational_sample_retention_days=$5,
    backup_hour=$6,backup_minute=$7,backup_timezone=$8,
    disk_warning_percent=$9,disk_critical_percent=$10,
    ai_error_warning_percent=$11,ai_error_critical_percent=$12,
    processing_queue_warning=$13,processing_queue_critical=$14,
    updated_by=$15,updated_at=now(),version=version+1
WHERE singleton_id=true AND version=$16
RETURNING version,site_name,site_announcement,soft_delete_retention_days,
          audit_retention_days,operational_sample_retention_days,
          backup_hour,backup_minute,backup_timezone,
          disk_warning_percent,disk_critical_percent,
          ai_error_warning_percent,ai_error_critical_percent,
          processing_queue_warning,processing_queue_critical,
          updated_by,updated_at`,
		settings.SiteName, settings.SiteAnnouncement,
		settings.SoftDeleteRetentionDays, settings.AuditRetentionDays,
		settings.OperationalSampleRetentionDays, settings.BackupHour,
		settings.BackupMinute, settings.BackupTimezone,
		settings.DiskWarningPercent, settings.DiskCriticalPercent,
		settings.AIErrorWarningPercent, settings.AIErrorCriticalPercent,
		settings.ProcessingQueueWarning, settings.ProcessingQueueCritical,
		principal.User.ID, settings.Version,
	).Scan(
		&updated.Version, &updated.SiteName, &updated.SiteAnnouncement,
		&updated.SoftDeleteRetentionDays, &updated.AuditRetentionDays,
		&updated.OperationalSampleRetentionDays, &updated.BackupHour,
		&updated.BackupMinute, &updated.BackupTimezone,
		&updated.DiskWarningPercent, &updated.DiskCriticalPercent,
		&updated.AIErrorWarningPercent, &updated.AIErrorCriticalPercent,
		&updated.ProcessingQueueWarning, &updated.ProcessingQueueCritical,
		&updated.UpdatedBy, &updated.UpdatedAt,
	)
	if err != nil {
		return Settings{}, mapSettingsError(err)
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
		ActorUserID: principal.User.ID,
		Action:      "operations.settings_updated",
		TargetType:  "system_settings",
		TargetID:    "global",
		Metadata:    map[string]any{},
		RequestID:   principal.RequestID,
		IP:          append(net.IP(nil), principal.IP...),
	}); err != nil {
		return Settings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Settings{}, mapSettingsError(err)
	}
	updated.UpdatedAt = updated.UpdatedAt.UTC()
	return updated, nil
}

func (s *PostgresStore) AuditSettingsRejection(ctx context.Context, principal Principal, reason string) error {
	if err := authorizeSettings(principal); err != nil {
		return err
	}
	if reason != "retention" && reason != "backup_schedule" && reason != "threshold" {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
		ActorUserID: principal.User.ID,
		Action:      "operations.settings_rejected",
		TargetType:  "system_settings",
		TargetID:    "global",
		Metadata: map[string]any{
			"category": "high_risk",
			"reason":   reason,
		},
		RequestID: principal.RequestID,
		IP:        append(net.IP(nil), principal.IP...),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) GetMode(ctx context.Context) (ModeSnapshot, error) {
	var mode ModeSnapshot
	var ownerID *uuid.UUID
	var expiresAt *time.Time
	err := s.pool.QueryRow(ctx, `
SELECT mode,owner_id,lease_expires_at,version
FROM operational_modes WHERE singleton_id=true`).
		Scan(&mode.Mode, &ownerID, &expiresAt, &mode.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModeSnapshot{}, ErrConflict
	}
	if err != nil {
		return ModeSnapshot{}, err
	}
	if ownerID != nil {
		mode.OwnerID = *ownerID
	}
	if expiresAt != nil {
		mode.ExpiresAt = expiresAt.UTC()
	}
	return mode, nil
}

func (s *PostgresStore) AcquireShared(ctx context.Context) (func(), error) {
	if s.leaseStoreClosed() {
		return nil, errStoreClosed
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, operationsAdvisoryKey); err != nil {
		releaseSharedConnection(conn)
		return nil, err
	}
	var mode string
	if err := conn.QueryRow(ctx, `
SELECT mode FROM operational_modes WHERE singleton_id=true`).Scan(&mode); err != nil {
		releaseSharedConnection(conn)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConflict
		}
		return nil, err
	}
	if mode != "normal" {
		releaseSharedConnection(conn)
		return nil, ErrLeaseHeld
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseSharedConnection(conn)
		})
	}, nil
}

func (s *PostgresStore) AcquireLease(ctx context.Context, request LeaseRequest) (lease Lease, err error) {
	if !maintenanceMode(request.Mode) || request.OwnerID == uuid.Nil {
		return Lease{}, ErrInvalid
	}
	if s.leaseStoreClosed() {
		return Lease{}, errStoreClosed
	}
	if err := s.preflightLeaseAvailability(ctx); err != nil {
		return Lease{}, err
	}

	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return Lease{}, err
	}
	tokenHash := sha256.Sum256(token)
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return Lease{}, err
	}
	lockAttempted := false
	keepConnection := false
	defer func() {
		if !keepConnection {
			if lockAttempted {
				closeHijackedConnection(conn)
			} else {
				conn.Release()
			}
		}
	}()
	lockAttempted = true
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, operationsAdvisoryKey); err != nil {
		return Lease{}, err
	}
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}

	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	tx, err := conn.Begin(dbCtx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(dbCtx)
	var currentMode string
	var currentOwner *uuid.UUID
	var currentHash []byte
	var currentExpiry *time.Time
	var currentVersion int64
	if err := tx.QueryRow(dbCtx, `
SELECT mode,owner_id,lease_token_hash,lease_expires_at,version
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&currentMode, &currentOwner, &currentHash, &currentExpiry, &currentVersion); err != nil {
		return Lease{}, mapLeaseError(err)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(dbCtx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return Lease{}, mapLeaseError(err)
	}
	if !request.ExpiresAt.After(databaseNow) {
		return Lease{}, ErrInvalid
	}
	takeover := currentMode != "normal"
	if takeover && currentExpiry != nil && currentExpiry.After(databaseNow) {
		return Lease{}, ErrLeaseHeld
	}
	lease = Lease{
		Mode: request.Mode, OwnerID: request.OwnerID,
		Token: append([]byte(nil), token...),
	}
	if err := tx.QueryRow(dbCtx, `
UPDATE operational_modes
SET mode=$1,owner_id=$2,lease_token_hash=$3,lease_expires_at=$4,
    entered_at=now(),updated_at=now(),version=version+1
WHERE singleton_id=true AND version=$5
RETURNING lease_expires_at,version`,
		request.Mode, request.OwnerID, tokenHash[:], request.ExpiresAt, currentVersion,
	).Scan(&lease.ExpiresAt, &lease.Version); err != nil {
		return Lease{}, mapLeaseError(err)
	}
	if takeover {
		if err := audit.NewPostgresWriter(tx).Write(dbCtx, audit.Event{
			Action:     "operations.lease_taken_over",
			TargetType: "operational_mode",
			TargetID:   "global",
			Metadata:   map[string]any{},
			RequestID:  "operations-lease-takeover",
			IP:         net.IPv4(127, 0, 0, 1).To4(),
		}); err != nil {
			return Lease{}, err
		}
	}
	remaining := lease.ExpiresAt.Sub(databaseNow)
	if commitErr := tx.Commit(dbCtx); commitErr != nil {
		reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer reconcileCancel()
		reconciledConn, reconciledRemaining, matched, reconcileErr :=
			s.reconcileAcquiredLease(reconcileCtx, conn, lease, tokenHash)
		conn = reconciledConn
		if conn == nil {
			keepConnection = true
		}
		if reconcileErr != nil || !matched {
			return Lease{}, mapLeaseError(commitErr)
		}
		remaining = reconciledRemaining
	}
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	keepConnection = true
	if err := s.registerLeaseSession(tokenHash, lease.OwnerID, conn, remaining); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		_ = normalizeOwnedLease(cleanupCtx, conn, lease.OwnerID, tokenHash)
		closeHijackedConnection(conn)
		return Lease{}, err
	}
	return lease, nil
}

func (s *PostgresStore) reconcileAcquiredLease(
	ctx context.Context,
	conn *pgxpool.Conn,
	lease Lease,
	tokenHash [32]byte,
) (*pgxpool.Conn, time.Duration, bool, error) {
	remaining, matched, err := acquiredLeaseState(ctx, conn, lease, tokenHash)
	if err == nil {
		return conn, remaining, matched, nil
	}

	closeHijackedConnection(conn)
	fresh, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	if _, err := fresh.Exec(ctx, `SELECT pg_advisory_lock($1)`, operationsAdvisoryKey); err != nil {
		closeHijackedConnection(fresh)
		return nil, 0, false, err
	}
	remaining, matched, err = acquiredLeaseState(ctx, fresh, lease, tokenHash)
	if err != nil {
		releaseExclusiveConnection(fresh)
		return nil, 0, false, err
	}
	return fresh, remaining, matched, nil
}

func acquiredLeaseState(
	ctx context.Context,
	conn *pgxpool.Conn,
	lease Lease,
	tokenHash [32]byte,
) (time.Duration, bool, error) {
	var ownerID *uuid.UUID
	var storedHash []byte
	var expiresAt *time.Time
	var databaseNow time.Time
	if err := conn.QueryRow(ctx, `
SELECT owner_id,lease_token_hash,lease_expires_at,clock_timestamp()
FROM operational_modes WHERE singleton_id=true`).
		Scan(&ownerID, &storedHash, &expiresAt, &databaseNow); err != nil {
		return 0, false, err
	}
	matched := ownerID != nil && *ownerID == lease.OwnerID &&
		bytes.Equal(storedHash, tokenHash[:])
	if !matched || expiresAt == nil {
		return 0, matched, nil
	}
	return expiresAt.Sub(databaseNow), true, nil
}

func (s *PostgresStore) preflightLeaseAvailability(ctx context.Context) error {
	var mode string
	var expiresAt *time.Time
	var databaseNow time.Time
	if err := s.pool.QueryRow(ctx, `
SELECT mode,lease_expires_at,clock_timestamp()
FROM operational_modes WHERE singleton_id=true`).
		Scan(&mode, &expiresAt, &databaseNow); err != nil {
		return mapLeaseError(err)
	}
	if mode != "normal" && expiresAt != nil && expiresAt.After(databaseNow) {
		return ErrLeaseHeld
	}
	return nil
}

func (s *PostgresStore) RenewLease(ctx context.Context, lease Lease, expiresAt time.Time) (Lease, error) {
	return s.mutateLease(ctx, lease, "", expiresAt, false)
}

func (s *PostgresStore) TransitionLease(ctx context.Context, lease Lease, mode string, expiresAt time.Time) (Lease, error) {
	if !maintenanceMode(mode) {
		return Lease{}, ErrInvalid
	}
	return s.mutateLease(ctx, lease, mode, expiresAt, true)
}

func (s *PostgresStore) mutateLease(ctx context.Context, lease Lease, mode string, expiresAt time.Time, transition bool) (Lease, error) {
	tokenHash, session, ok := s.sessionFor(lease)
	if !ok {
		return Lease{}, ErrStaleLease
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.released {
		return Lease{}, ErrStaleLease
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	tx, err := session.conn.Begin(dbCtx)
	if err != nil {
		return Lease{}, err
	}
	rollback := func() {
		_ = tx.Rollback(dbCtx)
	}
	defer rollback()
	var currentMode string
	var ownerID *uuid.UUID
	var storedHash []byte
	var storedExpiry *time.Time
	if err := tx.QueryRow(dbCtx, `
SELECT mode,owner_id,lease_token_hash,lease_expires_at
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&currentMode, &ownerID, &storedHash, &storedExpiry); err != nil {
		rollback()
		if errors.Is(err, pgx.ErrNoRows) {
			s.releaseSessionLocked(tokenHash, session)
			return Lease{}, ErrStaleLease
		}
		return Lease{}, mapLeaseError(err)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(dbCtx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return Lease{}, mapLeaseError(err)
	}
	if ownerID == nil || *ownerID != lease.OwnerID ||
		!bytes.Equal(storedHash, tokenHash[:]) {
		rollback()
		s.releaseSessionLocked(tokenHash, session)
		return Lease{}, ErrStaleLease
	}
	if storedExpiry == nil || !storedExpiry.After(databaseNow) {
		rollback()
		s.releaseSessionLocked(tokenHash, session)
		return Lease{}, ErrStaleLease
	}
	if !expiresAt.After(databaseNow) {
		return Lease{}, ErrInvalid
	}
	updated := Lease{
		Mode: currentMode, OwnerID: lease.OwnerID,
		Token: append([]byte(nil), lease.Token...),
	}
	var updateErr error
	if transition {
		updated.Mode = mode
		updateErr = tx.QueryRow(dbCtx, `
UPDATE operational_modes
SET mode=$1,lease_expires_at=$2,updated_at=now(),version=version+1
WHERE singleton_id=true AND owner_id=$3 AND lease_token_hash=$4
RETURNING lease_expires_at,version`,
			mode, expiresAt, lease.OwnerID, tokenHash[:],
		).Scan(&updated.ExpiresAt, &updated.Version)
	} else {
		updateErr = tx.QueryRow(dbCtx, `
UPDATE operational_modes
SET lease_expires_at=$1,updated_at=now(),version=version+1
WHERE singleton_id=true AND owner_id=$2 AND lease_token_hash=$3
RETURNING mode,lease_expires_at,version`,
			expiresAt, lease.OwnerID, tokenHash[:],
		).Scan(&updated.Mode, &updated.ExpiresAt, &updated.Version)
	}
	if updateErr != nil {
		rollback()
		if errors.Is(updateErr, pgx.ErrNoRows) {
			s.releaseSessionLocked(tokenHash, session)
			return Lease{}, ErrStaleLease
		}
		return Lease{}, mapLeaseError(updateErr)
	}
	if err := tx.Commit(dbCtx); err != nil {
		return Lease{}, mapLeaseError(err)
	}
	remaining := updated.ExpiresAt.Sub(databaseNow)
	updated.ExpiresAt = updated.ExpiresAt.UTC()
	s.scheduleLeaseExpiryLocked(tokenHash, session, remaining)
	return updated, nil
}

func (s *PostgresStore) clearLeaseAndReleaseLocked(ctx context.Context, tokenHash [32]byte, session *leaseSession, lease Lease) (bool, error) {
	tx, err := session.conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return false, err
	}
	var ownerID *uuid.UUID
	var storedHash []byte
	if err := tx.QueryRow(ctx, `
SELECT owner_id,lease_token_hash
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&ownerID, &storedHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			s.releaseSessionLocked(tokenHash, session)
			return false, nil
		}
		return false, err
	}
	if ownerID == nil || *ownerID != lease.OwnerID ||
		!bytes.Equal(storedHash, tokenHash[:]) {
		_ = tx.Rollback(ctx)
		s.releaseSessionLocked(tokenHash, session)
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=now(),version=version+1
WHERE singleton_id=true AND owner_id=$1 AND lease_token_hash=$2`,
		lease.OwnerID, tokenHash[:])
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, ErrStaleLease
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	s.releaseSessionLocked(tokenHash, session)
	return true, nil
}

func (s *PostgresStore) ReleaseLease(ctx context.Context, lease Lease) error {
	tokenHash, session, ok := s.sessionFor(lease)
	if !ok {
		return ErrStaleLease
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.released {
		return ErrStaleLease
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	callerErr := ctx.Err()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	matched, err := s.clearLeaseAndReleaseLocked(cleanupCtx, tokenHash, session, lease)
	if err != nil {
		return mapLeaseError(err)
	}
	if !matched {
		return ErrStaleLease
	}
	return callerErr
}

func (s *PostgresStore) Close(ctx context.Context) error {
	if err := lockMutexContext(ctx, &s.mu); err != nil {
		return err
	}
	s.closed = true
	type sessionEntry struct {
		tokenHash [32]byte
		session   *leaseSession
	}
	entries := make([]sessionEntry, 0, len(s.sessions))
	for tokenHash, session := range s.sessions {
		entries = append(entries, sessionEntry{tokenHash: tokenHash, session: session})
	}
	s.mu.Unlock()

	var firstErr error
	for _, entry := range entries {
		if err := lockMutexContext(ctx, &entry.session.mu); err != nil {
			if firstErr != nil {
				return errors.Join(firstErr, err)
			}
			return err
		}
		session := entry.session
		if session.released {
			session.mu.Unlock()
			continue
		}
		if err := normalizeOwnedLease(ctx, session.conn, session.ownerID, entry.tokenHash); err != nil &&
			firstErr == nil {
			firstErr = err
		}
		session.released = true
		if session.timer != nil {
			session.timer.Stop()
			session.timer = nil
		}
		if err := lockMutexContext(ctx, &s.mu); err != nil {
			closeHijackedConnectionWithContext(ctx, session.conn)
			session.mu.Unlock()
			if firstErr != nil {
				return errors.Join(firstErr, err)
			}
			return err
		}
		if s.sessions[entry.tokenHash] == session {
			delete(s.sessions, entry.tokenHash)
		}
		s.mu.Unlock()
		closeHijackedConnectionWithContext(ctx, session.conn)
		session.mu.Unlock()
	}
	return firstErr
}

func (s *PostgresStore) registerLeaseSession(
	tokenHash [32]byte,
	ownerID uuid.UUID,
	conn *pgxpool.Conn,
	remaining time.Duration,
) error {
	session := &leaseSession{conn: conn, ownerID: ownerID}
	session.mu.Lock()
	defer session.mu.Unlock()
	s.scheduleLeaseExpiryLocked(tokenHash, session, remaining)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		session.released = true
		if session.timer != nil {
			session.timer.Stop()
			session.timer = nil
		}
		return errStoreClosed
	}
	s.sessions[tokenHash] = session
	return nil
}

func (s *PostgresStore) leaseStoreClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func normalizeOwnedLease(
	ctx context.Context,
	conn *pgxpool.Conn,
	ownerID uuid.UUID,
	tokenHash [32]byte,
) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var storedOwner *uuid.UUID
	var storedHash []byte
	if err := tx.QueryRow(ctx, `
SELECT owner_id,lease_token_hash
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&storedOwner, &storedHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if storedOwner == nil || *storedOwner != ownerID ||
		!bytes.Equal(storedHash, tokenHash[:]) {
		return nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=clock_timestamp(),version=version+1
WHERE singleton_id=true AND owner_id=$1 AND lease_token_hash=$2`,
		ownerID, tokenHash[:]); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockMutexContext(ctx context.Context, mu *sync.Mutex) error {
	if mu.TryLock() {
		return nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if mu.TryLock() {
				return nil
			}
		}
	}
}

func (s *PostgresStore) sessionFor(lease Lease) ([32]byte, *leaseSession, bool) {
	if lease.OwnerID == uuid.Nil || len(lease.Token) != sha256.Size {
		return [32]byte{}, nil, false
	}
	tokenHash := sha256.Sum256(lease.Token)
	s.mu.Lock()
	session, ok := s.sessions[tokenHash]
	s.mu.Unlock()
	return tokenHash, session, ok
}

func (s *PostgresStore) releaseSessionLocked(tokenHash [32]byte, session *leaseSession) {
	if session.released {
		return
	}
	session.released = true
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
	}
	s.mu.Lock()
	delete(s.sessions, tokenHash)
	s.mu.Unlock()
	releaseExclusiveConnection(session.conn)
}

func (s *PostgresStore) scheduleLeaseExpiryLocked(tokenHash [32]byte, session *leaseSession, remaining time.Duration) {
	if session.timer != nil {
		session.timer.Stop()
	}
	session.expiryGeneration++
	generation := session.expiryGeneration
	if remaining < 0 {
		remaining = 0
	}
	session.timer = time.AfterFunc(remaining, func() {
		s.expireLeaseSession(tokenHash, session, generation)
	})
}

func (s *PostgresStore) expireLeaseSession(
	tokenHash [32]byte,
	session *leaseSession,
	generation uint64,
) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.released || session.expiryGeneration != generation {
		return
	}
	s.releaseSessionLocked(tokenHash, session)
}

func maintenanceMode(mode string) bool {
	switch mode {
	case "draining", "backup", "release":
		return true
	default:
		return false
	}
}

func releaseSharedConnection(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock_shared($1)`, operationsAdvisoryKey).
		Scan(&unlocked); err != nil || !unlocked {
		closeHijackedConnection(conn)
		return
	}
	conn.Release()
}

func releaseExclusiveConnection(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, operationsAdvisoryKey).
		Scan(&unlocked); err != nil || !unlocked {
		closeHijackedConnection(conn)
		return
	}
	conn.Release()
}

func closeHijackedConnection(conn *pgxpool.Conn) {
	raw := conn.Hijack()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = raw.Close(ctx)
}

func closeHijackedConnectionWithContext(ctx context.Context, conn *pgxpool.Conn) {
	raw := conn.Hijack()
	_ = raw.Close(ctx)
}

func mapSettingsError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return ErrForbidden
		case "23505":
			return ErrConflict
		case "22021", "23514":
			return ErrInvalid
		}
	}
	return err
}

func mapLeaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStaleLease
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "22021", "23502", "23503", "23514":
			return ErrInvalid
		case "23505":
			return ErrConflict
		}
	}
	return err
}

var _ Store = (*PostgresStore)(nil)
var _ SettingsRejectionAuditor = (*PostgresStore)(nil)
var _ WriteGate = (*PostgresStore)(nil)
var _ LeaseSessionCloser = (*PostgresStore)(nil)
