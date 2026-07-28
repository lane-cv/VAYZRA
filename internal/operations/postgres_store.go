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

type sharedSession struct {
	once sync.Once
	conn *pgxpool.Conn
}

type PostgresStore struct {
	pool *pgxpool.Pool

	mu             sync.Mutex
	sessions       map[[32]byte]*leaseSession
	sharedSessions map[uint64]*sharedSession
	nextSharedID   uint64
	closed         bool
	lifecycleCtx   context.Context
	lifecycleStop  context.CancelFunc
	admitted       sync.WaitGroup

	afterCommit func(string, *pgxpool.Conn) error
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	lifecycleCtx, lifecycleStop := context.WithCancel(context.Background())
	return &PostgresStore{
		pool:           pool,
		sessions:       make(map[[32]byte]*leaseSession),
		sharedSessions: make(map[uint64]*sharedSession),
		lifecycleCtx:   lifecycleCtx,
		lifecycleStop:  lifecycleStop,
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

func (s *PostgresStore) ClaimsAllowed(ctx context.Context) (bool, error) {
	mode, err := s.GetMode(ctx)
	if err != nil {
		return false, err
	}
	return mode.Mode == "normal", nil
}

func (s *PostgresStore) AcquireShared(ctx context.Context) (func(), error) {
	waitCtx, _, done, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	conn, err := s.pool.Acquire(waitCtx)
	if err != nil {
		return nil, s.mapLifecycleError(err)
	}
	tx, err := conn.Begin(waitCtx)
	if err != nil {
		conn.Release()
		return nil, s.mapLifecycleError(err)
	}
	rollback := func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}
	if _, err := tx.Exec(waitCtx, admissionLockTimeoutSQL); err != nil {
		rollback()
		conn.Release()
		return nil, s.mapLifecycleError(err)
	}
	if _, err := tx.Exec(
		waitCtx,
		`SELECT pg_advisory_lock_shared($1)`,
		operationsAdvisoryKey,
	); err != nil {
		rollback()
		conn.Release()
		if admissionLockTimedOut(err) {
			return nil, ErrLeaseHeld
		}
		return nil, s.mapLifecycleError(err)
	}
	var mode string
	if err := tx.QueryRow(waitCtx, `
SELECT mode FROM operational_modes WHERE singleton_id=true`).Scan(&mode); err != nil {
		rollback()
		releaseSharedConnection(conn)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConflict
		}
		return nil, s.mapLifecycleError(err)
	}
	if mode != "normal" {
		rollback()
		releaseSharedConnection(conn)
		return nil, ErrLeaseHeld
	}
	if err := tx.Commit(waitCtx); err != nil {
		closeHijackedConnection(conn)
		return nil, s.mapLifecycleError(err)
	}
	shared := &sharedSession{conn: conn}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		releaseSharedConnection(conn)
		return nil, errStoreClosed
	}
	s.nextSharedID++
	id := s.nextSharedID
	s.sharedSessions[id] = shared
	s.mu.Unlock()
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.releaseSharedSession(releaseCtx, id, shared)
	}, nil
}

func (s *PostgresStore) AcquireLease(ctx context.Context, request LeaseRequest) (lease Lease, err error) {
	if !maintenanceMode(request.Mode) || request.OwnerID == uuid.Nil {
		return Lease{}, ErrInvalid
	}
	waitCtx, lifecycleCtx, done, err := s.admit(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer done()
	if err := s.preflightLeaseAvailability(waitCtx); err != nil {
		return Lease{}, s.mapLifecycleError(err)
	}

	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return Lease{}, err
	}
	tokenHash := sha256.Sum256(token)
	conn, err := s.pool.Acquire(waitCtx)
	if err != nil {
		return Lease{}, s.mapLifecycleError(err)
	}
	lockAttempted := false
	keepConnection := false
	defer func() {
		if !keepConnection && conn != nil {
			if lockAttempted {
				closeHijackedConnection(conn)
			} else {
				conn.Release()
			}
		}
	}()
	lockAttempted = true
	if _, err := conn.Exec(waitCtx, `SELECT pg_advisory_lock($1)`, operationsAdvisoryKey); err != nil {
		return Lease{}, s.mapLifecycleError(err)
	}
	if err := waitCtx.Err(); err != nil {
		return Lease{}, s.mapLifecycleError(err)
	}

	dbCtx, cancel := context.WithTimeout(lifecycleCtx, 3*time.Second)
	defer cancel()
	tx, err := conn.Begin(dbCtx)
	if err != nil {
		return Lease{}, s.mapLifecycleError(err)
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
		return Lease{}, s.mapLifecycleError(mapLeaseError(err))
	}
	var databaseNow time.Time
	if err := tx.QueryRow(dbCtx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return Lease{}, s.mapLifecycleError(mapLeaseError(err))
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
		return Lease{}, s.mapLifecycleError(mapLeaseError(err))
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
			return Lease{}, s.mapLifecycleError(err)
		}
	}
	commitErr := s.commitLeaseTx(dbCtx, "acquire", tx, conn)
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer reconcileCancel()
	ownedConn := conn
	conn = nil
	reconciledConn, state, reconcileErr := s.authoritativeLeaseState(reconcileCtx, ownedConn)
	conn = reconciledConn
	if reconcileErr != nil {
		if commitErr != nil {
			return Lease{}, s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return Lease{}, s.mapLifecycleError(mapLeaseError(reconcileErr))
	}
	if !state.ownedBy(lease.OwnerID, tokenHash) ||
		state.mode != lease.Mode ||
		state.version != lease.Version ||
		state.expiresAt == nil ||
		!state.expiresAt.Equal(lease.ExpiresAt) {
		if commitErr != nil {
			return Lease{}, s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return Lease{}, ErrStaleLease
	}
	lease.Mode = state.mode
	lease.Version = state.version
	lease.ExpiresAt = state.expiresAt.UTC()
	keepConnection = true
	if err := s.registerLeaseSession(tokenHash, lease.OwnerID, conn, state.remaining); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		_ = normalizeOwnedLease(cleanupCtx, conn, lease.OwnerID, tokenHash)
		closeHijackedConnection(conn)
		return Lease{}, err
	}
	return lease, nil
}

func (s *PostgresStore) commitLeaseTx(
	ctx context.Context,
	operation string,
	tx pgx.Tx,
	conn *pgxpool.Conn,
) error {
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if s.afterCommit != nil {
		return s.afterCommit(operation, conn)
	}
	return nil
}

type leaseDBState struct {
	mode      string
	ownerID   *uuid.UUID
	tokenHash []byte
	expiresAt *time.Time
	version   int64
	remaining time.Duration
}

func (state leaseDBState) ownedBy(ownerID uuid.UUID, tokenHash [32]byte) bool {
	return state.ownerID != nil &&
		*state.ownerID == ownerID &&
		bytes.Equal(state.tokenHash, tokenHash[:])
}

func (s *PostgresStore) authoritativeLeaseState(
	ctx context.Context,
	conn *pgxpool.Conn,
) (*pgxpool.Conn, leaseDBState, error) {
	state, err := queryLeaseDBState(ctx, conn)
	if err == nil {
		return conn, state, nil
	}
	closeHijackedConnection(conn)
	fresh, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, leaseDBState{}, err
	}
	if _, err := fresh.Exec(ctx, `SELECT pg_advisory_lock($1)`, operationsAdvisoryKey); err != nil {
		closeHijackedConnection(fresh)
		return nil, leaseDBState{}, err
	}
	state, err = queryLeaseDBState(ctx, fresh)
	if err != nil {
		releaseExclusiveConnection(fresh)
		return nil, leaseDBState{}, err
	}
	return fresh, state, nil
}

func leaseConnectionInterrupted(conn *pgxpool.Conn, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if conn.Conn().IsClosed() {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Severity == "FATAL"
}

func (s *PostgresStore) recoverLeaseSessionLocked(
	ctx context.Context,
	tokenHash [32]byte,
	session *leaseSession,
	lease Lease,
	originalErr error,
) error {
	state, recoverErr := s.authoritativeLeaseSessionStateLocked(ctx, session)
	if session.conn == nil {
		s.removeSessionLocked(tokenHash, session)
		return originalErr
	}
	if recoverErr != nil {
		s.releaseSessionLocked(tokenHash, session)
		return originalErr
	}
	if !state.ownedBy(lease.OwnerID, tokenHash) ||
		state.expiresAt == nil ||
		state.remaining <= 0 {
		s.releaseSessionLocked(tokenHash, session)
		return ErrStaleLease
	}
	s.scheduleLeaseExpiryLocked(tokenHash, session, state.remaining)
	return nil
}

func (s *PostgresStore) authoritativeLeaseSessionStateLocked(
	ctx context.Context,
	session *leaseSession,
) (leaseDBState, error) {
	ownedConn := session.conn
	session.conn = nil
	recoveredConn, state, err := s.authoritativeLeaseState(ctx, ownedConn)
	session.conn = recoveredConn
	return state, err
}

func queryLeaseDBState(
	ctx context.Context,
	conn *pgxpool.Conn,
) (leaseDBState, error) {
	var state leaseDBState
	var ownerID *uuid.UUID
	var databaseNow time.Time
	if err := conn.QueryRow(ctx, `
SELECT mode,owner_id,lease_token_hash,lease_expires_at,version,clock_timestamp()
FROM operational_modes WHERE singleton_id=true`).
		Scan(
			&state.mode,
			&ownerID,
			&state.tokenHash,
			&state.expiresAt,
			&state.version,
			&databaseNow,
		); err != nil {
		return leaseDBState{}, err
	}
	state.ownerID = ownerID
	if state.expiresAt != nil {
		state.remaining = state.expiresAt.Sub(databaseNow)
	}
	return state, nil
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
	waitCtx, lifecycleCtx, done, err := s.admit(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer done()
	tokenHash, session, ok := s.sessionFor(lease)
	if !ok {
		return Lease{}, ErrStaleLease
	}
	if err := lockMutexContext(waitCtx, &session.mu); err != nil {
		return Lease{}, s.mapLifecycleError(err)
	}
	defer session.mu.Unlock()
	if session.released {
		return Lease{}, ErrStaleLease
	}
	if err := waitCtx.Err(); err != nil {
		return Lease{}, s.mapLifecycleError(err)
	}
	dbCtx, cancel := context.WithTimeout(lifecycleCtx, 3*time.Second)
	defer cancel()
	var updated Lease
	var commitErr error
	for attempt := 0; ; attempt++ {
		var attemptErr error
		var interrupted bool
		updated, commitErr, interrupted, attemptErr = s.mutateLeaseTransaction(
			dbCtx,
			tokenHash,
			session,
			lease,
			mode,
			expiresAt,
			transition,
		)
		if attemptErr == nil {
			break
		}
		if lifecycleCtx.Err() != nil {
			return Lease{}, s.mapLifecycleError(attemptErr)
		}
		if attempt > 0 || !interrupted {
			if interrupted {
				s.releaseSessionLocked(tokenHash, session)
			}
			return Lease{}, s.mapLifecycleError(attemptErr)
		}
		if err := s.recoverLeaseSessionLocked(
			dbCtx,
			tokenHash,
			session,
			lease,
			attemptErr,
		); err != nil {
			return Lease{}, s.mapLifecycleError(err)
		}
	}
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer reconcileCancel()
	state, reconcileErr := s.authoritativeLeaseSessionStateLocked(reconcileCtx, session)
	if session.conn == nil {
		s.removeSessionLocked(tokenHash, session)
		if commitErr != nil {
			return Lease{}, s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return Lease{}, mapLeaseError(reconcileErr)
	}
	if reconcileErr != nil {
		s.releaseSessionLocked(tokenHash, session)
		if commitErr != nil {
			return Lease{}, s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return Lease{}, mapLeaseError(reconcileErr)
	}
	if !state.ownedBy(lease.OwnerID, tokenHash) {
		s.releaseSessionLocked(tokenHash, session)
		return Lease{}, ErrStaleLease
	}
	intendedCommitted := state.mode == updated.Mode &&
		state.version == updated.Version &&
		state.expiresAt != nil &&
		state.expiresAt.Equal(updated.ExpiresAt)
	s.scheduleLeaseExpiryLocked(tokenHash, session, state.remaining)
	if !intendedCommitted {
		if commitErr != nil {
			return Lease{}, s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return Lease{}, ErrConflict
	}
	updated.Mode = state.mode
	updated.Version = state.version
	updated.ExpiresAt = state.expiresAt.UTC()
	return updated, nil
}

func (s *PostgresStore) mutateLeaseTransaction(
	ctx context.Context,
	tokenHash [32]byte,
	session *leaseSession,
	lease Lease,
	mode string,
	expiresAt time.Time,
	transition bool,
) (Lease, error, bool, error) {
	tx, err := session.conn.Begin(ctx)
	if err != nil {
		return Lease{}, nil, leaseConnectionInterrupted(session.conn, err), mapLeaseError(err)
	}
	rollback := func() {
		_ = tx.Rollback(ctx)
	}
	defer rollback()
	var currentMode string
	var ownerID *uuid.UUID
	var storedHash []byte
	var storedExpiry *time.Time
	if err := tx.QueryRow(ctx, `
SELECT mode,owner_id,lease_token_hash,lease_expires_at
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&currentMode, &ownerID, &storedHash, &storedExpiry); err != nil {
		rollback()
		if errors.Is(err, pgx.ErrNoRows) {
			s.releaseSessionLocked(tokenHash, session)
			return Lease{}, nil, false, ErrStaleLease
		}
		return Lease{}, nil, leaseConnectionInterrupted(session.conn, err), mapLeaseError(err)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return Lease{}, nil, leaseConnectionInterrupted(session.conn, err), mapLeaseError(err)
	}
	if ownerID == nil || *ownerID != lease.OwnerID ||
		!bytes.Equal(storedHash, tokenHash[:]) {
		rollback()
		s.releaseSessionLocked(tokenHash, session)
		return Lease{}, nil, false, ErrStaleLease
	}
	if storedExpiry == nil || !storedExpiry.After(databaseNow) {
		rollback()
		s.releaseSessionLocked(tokenHash, session)
		return Lease{}, nil, false, ErrStaleLease
	}
	if !expiresAt.After(databaseNow) {
		return Lease{}, nil, false, ErrInvalid
	}
	updated := Lease{
		Mode: currentMode, OwnerID: lease.OwnerID,
		Token: append([]byte(nil), lease.Token...),
	}
	var updateErr error
	if transition {
		updated.Mode = mode
		updateErr = tx.QueryRow(ctx, `
UPDATE operational_modes
SET mode=$1,lease_expires_at=$2,updated_at=now(),version=version+1
WHERE singleton_id=true AND owner_id=$3 AND lease_token_hash=$4
RETURNING lease_expires_at,version`,
			mode, expiresAt, lease.OwnerID, tokenHash[:],
		).Scan(&updated.ExpiresAt, &updated.Version)
	} else {
		updateErr = tx.QueryRow(ctx, `
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
			return Lease{}, nil, false, ErrStaleLease
		}
		return Lease{}, nil, leaseConnectionInterrupted(session.conn, updateErr), mapLeaseError(updateErr)
	}
	operation := "renew"
	if transition {
		operation = "transition"
	}
	commitErr := s.commitLeaseTx(ctx, operation, tx, session.conn)
	return updated, commitErr, false, nil
}

func (s *PostgresStore) clearLeaseAndReleaseLocked(
	ctx context.Context,
	tokenHash [32]byte,
	session *leaseSession,
	lease Lease,
) (bool, bool, error) {
	tx, err := session.conn.Begin(ctx)
	if err != nil {
		return false, leaseConnectionInterrupted(session.conn, err), err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return false, leaseConnectionInterrupted(session.conn, err), err
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
			return false, false, nil
		}
		return false, leaseConnectionInterrupted(session.conn, err), err
	}
	if ownerID == nil || *ownerID != lease.OwnerID ||
		!bytes.Equal(storedHash, tokenHash[:]) {
		_ = tx.Rollback(ctx)
		s.releaseSessionLocked(tokenHash, session)
		return false, false, nil
	}
	tag, err := tx.Exec(ctx, `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=now(),version=version+1
WHERE singleton_id=true AND owner_id=$1 AND lease_token_hash=$2`,
		lease.OwnerID, tokenHash[:])
	if err != nil {
		return false, leaseConnectionInterrupted(session.conn, err), err
	}
	if tag.RowsAffected() != 1 {
		return false, false, ErrStaleLease
	}
	return true, false, s.commitLeaseTx(ctx, "release", tx, session.conn)
}

func (s *PostgresStore) ReleaseLease(ctx context.Context, lease Lease) error {
	waitCtx, lifecycleCtx, done, err := s.admit(ctx)
	if err != nil {
		return err
	}
	defer done()
	tokenHash, session, ok := s.sessionFor(lease)
	if !ok {
		return ErrStaleLease
	}
	if err := lockMutexContext(waitCtx, &session.mu); err != nil {
		return s.mapLifecycleError(err)
	}
	defer session.mu.Unlock()
	if session.released {
		return ErrStaleLease
	}
	if err := waitCtx.Err(); err != nil {
		return s.mapLifecycleError(err)
	}
	cleanupCtx, cancel := context.WithTimeout(lifecycleCtx, 3*time.Second)
	defer cancel()
	var matched bool
	var commitErr error
	for attempt := 0; ; attempt++ {
		var interrupted bool
		matched, interrupted, commitErr = s.clearLeaseAndReleaseLocked(
			cleanupCtx,
			tokenHash,
			session,
			lease,
		)
		if matched || commitErr == nil {
			break
		}
		if lifecycleCtx.Err() != nil {
			return s.mapLifecycleError(mapLeaseError(commitErr))
		}
		if attempt > 0 || !interrupted {
			if interrupted {
				s.releaseSessionLocked(tokenHash, session)
			}
			return s.mapLifecycleError(mapLeaseError(commitErr))
		}
		if err := s.recoverLeaseSessionLocked(
			cleanupCtx,
			tokenHash,
			session,
			lease,
			mapLeaseError(commitErr),
		); err != nil {
			return s.mapLifecycleError(err)
		}
	}
	if !matched {
		if commitErr != nil {
			return s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return ErrStaleLease
	}
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer reconcileCancel()
	state, reconcileErr := s.authoritativeLeaseSessionStateLocked(reconcileCtx, session)
	if session.conn == nil {
		s.removeSessionLocked(tokenHash, session)
		if commitErr != nil {
			return s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return mapLeaseError(reconcileErr)
	}
	if reconcileErr != nil {
		s.releaseSessionLocked(tokenHash, session)
		if commitErr != nil {
			return s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return mapLeaseError(reconcileErr)
	}
	if state.mode == "normal" && state.ownerID == nil && state.expiresAt == nil {
		s.releaseSessionLocked(tokenHash, session)
		return nil
	}
	if state.ownedBy(lease.OwnerID, tokenHash) {
		s.scheduleLeaseExpiryLocked(tokenHash, session, state.remaining)
		if commitErr != nil {
			return s.mapLifecycleError(mapLeaseError(commitErr))
		}
		return ErrConflict
	}
	s.releaseSessionLocked(tokenHash, session)
	return ErrStaleLease
}

func (s *PostgresStore) admit(
	callerCtx context.Context,
) (context.Context, context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, nil, errStoreClosed
	}
	s.admitted.Add(1)
	lifecycleCtx := s.lifecycleCtx
	s.mu.Unlock()

	waitCtx, cancelWait := context.WithCancel(callerCtx)
	stopLifecycle := context.AfterFunc(lifecycleCtx, cancelWait)
	var once sync.Once
	done := func() {
		once.Do(func() {
			stopLifecycle()
			cancelWait()
			s.admitted.Done()
		})
	}
	return waitCtx, lifecycleCtx, done, nil
}

func (s *PostgresStore) mapLifecycleError(err error) error {
	if s.lifecycleCtx.Err() != nil {
		return errStoreClosed
	}
	return err
}

func (s *PostgresStore) releaseSharedSession(
	ctx context.Context,
	id uint64,
	session *sharedSession,
) {
	session.once.Do(func() {
		s.mu.Lock()
		if s.sharedSessions[id] == session {
			delete(s.sharedSessions, id)
		}
		s.mu.Unlock()
		releaseSharedConnectionWithContext(ctx, session.conn)
	})
}

func (s *PostgresStore) waitForAdmissions(ctx context.Context) error {
	drained := make(chan struct{})
	go func() {
		s.admitted.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *PostgresStore) Close(ctx context.Context) error {
	if err := lockMutexContext(ctx, &s.mu); err != nil {
		return err
	}
	s.closed = true
	s.lifecycleStop()
	type sharedEntry struct {
		id      uint64
		session *sharedSession
	}
	sharedEntries := make([]sharedEntry, 0, len(s.sharedSessions))
	for id, session := range s.sharedSessions {
		sharedEntries = append(sharedEntries, sharedEntry{id: id, session: session})
	}
	s.mu.Unlock()
	for _, entry := range sharedEntries {
		s.releaseSharedSession(ctx, entry.id, entry.session)
	}
	if err := s.waitForAdmissions(ctx); err != nil {
		return err
	}

	if err := lockMutexContext(ctx, &s.mu); err != nil {
		return err
	}
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
		if err := s.normalizeLeaseSessionLocked(
			ctx,
			entry.tokenHash,
			session,
		); err != nil && firstErr == nil {
			firstErr = err
		}
		session.released = true
		if session.timer != nil {
			session.timer.Stop()
			session.timer = nil
		}
		if err := lockMutexContext(ctx, &s.mu); err != nil {
			ownedConn := session.conn
			session.conn = nil
			if ownedConn != nil {
				closeHijackedConnectionWithContext(ctx, ownedConn)
			}
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
		ownedConn := session.conn
		session.conn = nil
		if ownedConn != nil {
			closeHijackedConnectionWithContext(ctx, ownedConn)
		}
		session.mu.Unlock()
	}
	return firstErr
}

func (s *PostgresStore) normalizeLeaseSessionLocked(
	ctx context.Context,
	tokenHash [32]byte,
	session *leaseSession,
) error {
	err := normalizeOwnedLease(ctx, session.conn, session.ownerID, tokenHash)
	if err == nil || !leaseConnectionInterrupted(session.conn, err) {
		return err
	}
	state, recoverErr := s.authoritativeLeaseSessionStateLocked(ctx, session)
	if session.conn == nil {
		return recoverErr
	}
	if recoverErr != nil {
		return recoverErr
	}
	if !state.ownedBy(session.ownerID, tokenHash) ||
		state.expiresAt == nil ||
		state.remaining <= 0 {
		return nil
	}
	return normalizeOwnedLease(ctx, session.conn, session.ownerID, tokenHash)
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
	ownedConn := session.conn
	session.conn = nil
	s.removeSessionLocked(tokenHash, session)
	if ownedConn != nil {
		releaseExclusiveConnection(ownedConn)
	}
}

func (s *PostgresStore) removeSessionLocked(tokenHash [32]byte, session *leaseSession) {
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
	releaseSharedConnectionWithContext(ctx, conn)
}

func releaseSharedConnectionWithContext(ctx context.Context, conn *pgxpool.Conn) {
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock_shared($1)`, operationsAdvisoryKey).
		Scan(&unlocked); err != nil || !unlocked {
		closeHijackedConnectionWithContext(ctx, conn)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	closeHijackedConnectionWithContext(ctx, conn)
}

func closeHijackedConnectionWithContext(ctx context.Context, conn *pgxpool.Conn) {
	raw := conn.Hijack()
	_ = raw.Close(ctx)
	select {
	case <-raw.PgConn().CleanupDone():
	case <-ctx.Done():
	}
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
var _ ClaimGate = (*PostgresStore)(nil)
var _ LeaseSessionCloser = (*PostgresStore)(nil)
