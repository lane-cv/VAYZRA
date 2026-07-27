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

type leaseSession struct {
	mu       sync.Mutex
	conn     *pgxpool.Conn
	released bool
}

type PostgresStore struct {
	pool *pgxpool.Pool

	mu       sync.Mutex
	sessions map[[32]byte]*leaseSession
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
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, operationsAdvisoryKey); err != nil {
		releaseSharedConnection(conn)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseSharedConnection(conn)
		})
	}, nil
}

func (s *PostgresStore) AcquireLease(ctx context.Context, request LeaseRequest) (lease Lease, err error) {
	if !maintenanceMode(request.Mode) || request.OwnerID == uuid.Nil ||
		!request.ExpiresAt.After(time.Now().UTC()) {
		return Lease{}, ErrInvalid
	}
	snapshot, err := s.GetMode(ctx)
	if err != nil {
		return Lease{}, err
	}
	if snapshot.Mode != "normal" && snapshot.ExpiresAt.After(time.Now().UTC()) {
		return Lease{}, ErrLeaseHeld
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
				releaseExclusiveConnection(conn)
			} else {
				conn.Release()
			}
		}
	}()
	lockAttempted = true
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, operationsAdvisoryKey); err != nil {
		return Lease{}, err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	var currentMode string
	var currentOwner *uuid.UUID
	var currentHash []byte
	var currentExpiry *time.Time
	var currentVersion int64
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `
SELECT mode,owner_id,lease_token_hash,lease_expires_at,version,now()
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&currentMode, &currentOwner, &currentHash, &currentExpiry, &currentVersion, &databaseNow); err != nil {
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
	if err := tx.QueryRow(ctx, `
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
		if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
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
	if err := tx.Commit(ctx); err != nil {
		return Lease{}, mapLeaseError(err)
	}
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	session := &leaseSession{conn: conn}
	s.mu.Lock()
	s.sessions[tokenHash] = session
	s.mu.Unlock()
	keepConnection = true
	return lease, nil
}

func (s *PostgresStore) RenewLease(ctx context.Context, lease Lease, expiresAt time.Time) (Lease, error) {
	if !expiresAt.After(time.Now().UTC()) {
		return Lease{}, ErrInvalid
	}
	return s.mutateLease(ctx, lease, lease.Mode, expiresAt)
}

func (s *PostgresStore) TransitionLease(ctx context.Context, lease Lease, mode string, expiresAt time.Time) (Lease, error) {
	if !maintenanceMode(mode) || !expiresAt.After(time.Now().UTC()) {
		return Lease{}, ErrInvalid
	}
	return s.mutateLease(ctx, lease, mode, expiresAt)
}

func (s *PostgresStore) mutateLease(ctx context.Context, lease Lease, mode string, expiresAt time.Time) (Lease, error) {
	tokenHash, session, ok := s.sessionFor(lease)
	if !ok {
		return Lease{}, ErrStaleLease
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.released {
		return Lease{}, ErrStaleLease
	}
	keepSession := false
	defer func() {
		if !keepSession {
			s.releaseSessionLocked(tokenHash, session)
		}
	}()
	tx, err := session.conn.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	var ownerID *uuid.UUID
	var storedHash []byte
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `
SELECT owner_id,lease_token_hash,now()
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&ownerID, &storedHash, &databaseNow); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Lease{}, ErrStaleLease
		}
		return Lease{}, mapLeaseError(err)
	}
	if ownerID == nil || *ownerID != lease.OwnerID ||
		!bytes.Equal(storedHash, tokenHash[:]) {
		return Lease{}, ErrStaleLease
	}
	if !expiresAt.After(databaseNow) {
		return Lease{}, ErrInvalid
	}
	updated := Lease{
		Mode: mode, OwnerID: lease.OwnerID,
		Token: append([]byte(nil), lease.Token...),
	}
	if err := tx.QueryRow(ctx, `
UPDATE operational_modes
SET mode=$1,lease_expires_at=$2,updated_at=now(),version=version+1
WHERE singleton_id=true AND owner_id=$3 AND lease_token_hash=$4
RETURNING lease_expires_at,version`,
		mode, expiresAt, lease.OwnerID, tokenHash[:],
	).Scan(&updated.ExpiresAt, &updated.Version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Lease{}, ErrStaleLease
		}
		return Lease{}, mapLeaseError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Lease{}, mapLeaseError(err)
	}
	keepSession = true
	updated.ExpiresAt = updated.ExpiresAt.UTC()
	return updated, nil
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
	defer s.releaseSessionLocked(tokenHash, session)

	tx, err := session.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ownerID *uuid.UUID
	var storedHash []byte
	if err := tx.QueryRow(ctx, `
SELECT owner_id,lease_token_hash
FROM operational_modes WHERE singleton_id=true FOR UPDATE`).
		Scan(&ownerID, &storedHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleLease
		}
		return mapLeaseError(err)
	}
	if ownerID == nil || *ownerID != lease.OwnerID ||
		!bytes.Equal(storedHash, tokenHash[:]) {
		return ErrStaleLease
	}
	tag, err := tx.Exec(ctx, `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=now(),version=version+1
WHERE singleton_id=true AND owner_id=$1 AND lease_token_hash=$2`,
		lease.OwnerID, tokenHash[:])
	if err != nil {
		return mapLeaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleLease
	}
	if err := tx.Commit(ctx); err != nil {
		return mapLeaseError(err)
	}
	return nil
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
	s.mu.Lock()
	delete(s.sessions, tokenHash)
	s.mu.Unlock()
	releaseExclusiveConnection(session.conn)
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
var _ WriteGate = (*PostgresStore)(nil)
