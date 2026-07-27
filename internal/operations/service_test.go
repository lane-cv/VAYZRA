package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestValidateSettingsAcceptsDefaultsAndEveryBoundary(t *testing.T) {
	defaults := validSettings()
	valid := map[string]func(*Settings){
		"site name minimum":                  func(s *Settings) { s.SiteName = "学" },
		"site name maximum":                  func(s *Settings) { s.SiteName = strings.Repeat("学", 80) },
		"announcement minimum":               func(s *Settings) { s.SiteAnnouncement = "" },
		"announcement maximum":               func(s *Settings) { s.SiteAnnouncement = strings.Repeat("通", 1000) },
		"soft delete retention minimum":      func(s *Settings) { s.SoftDeleteRetentionDays = 30 },
		"soft delete retention maximum":      func(s *Settings) { s.SoftDeleteRetentionDays = 365 },
		"audit retention minimum":            func(s *Settings) { s.AuditRetentionDays = 365 },
		"audit retention maximum":            func(s *Settings) { s.AuditRetentionDays = 2555 },
		"sample retention minimum":           func(s *Settings) { s.OperationalSampleRetentionDays = 1 },
		"sample retention maximum":           func(s *Settings) { s.OperationalSampleRetentionDays = 30 },
		"backup hour minimum":                func(s *Settings) { s.BackupHour = 0 },
		"backup hour maximum":                func(s *Settings) { s.BackupHour = 23 },
		"backup minute minimum":              func(s *Settings) { s.BackupMinute = 0 },
		"backup minute maximum":              func(s *Settings) { s.BackupMinute = 59 },
		"disk warning minimum":               func(s *Settings) { s.DiskWarningPercent, s.DiskCriticalPercent = 1, 2 },
		"disk warning maximum":               func(s *Settings) { s.DiskWarningPercent, s.DiskCriticalPercent = 99, 100 },
		"disk critical minimum":              func(s *Settings) { s.DiskWarningPercent, s.DiskCriticalPercent = 1, 2 },
		"disk critical maximum":              func(s *Settings) { s.DiskWarningPercent, s.DiskCriticalPercent = 99, 100 },
		"AI warning minimum":                 func(s *Settings) { s.AIErrorWarningPercent, s.AIErrorCriticalPercent = 1, 2 },
		"AI warning maximum":                 func(s *Settings) { s.AIErrorWarningPercent, s.AIErrorCriticalPercent = 99, 100 },
		"AI critical minimum":                func(s *Settings) { s.AIErrorWarningPercent, s.AIErrorCriticalPercent = 1, 2 },
		"AI critical maximum":                func(s *Settings) { s.AIErrorWarningPercent, s.AIErrorCriticalPercent = 99, 100 },
		"processing queue warning minimum":   func(s *Settings) { s.ProcessingQueueWarning, s.ProcessingQueueCritical = 1, 2 },
		"processing queue critical minimum":  func(s *Settings) { s.ProcessingQueueWarning, s.ProcessingQueueCritical = 1, 2 },
		"processing queue unbounded maximum": func(s *Settings) { s.ProcessingQueueWarning, s.ProcessingQueueCritical = 1_000_000, 1_000_001 },
	}
	if err := ValidateSettings(defaults); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	for name, mutate := range valid {
		t.Run(name, func(t *testing.T) {
			settings := defaults
			mutate(&settings)
			if err := ValidateSettings(settings); err != nil {
				t.Fatalf("valid boundary rejected: %v", err)
			}
		})
	}
}

func TestValidateSettingsRejectsEveryOutOfRangeValue(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tests := map[string]func(*Settings){
		"version zero":                 func(s *Settings) { s.Version = 0 },
		"site name empty":              func(s *Settings) { s.SiteName = "" },
		"site name too long":           func(s *Settings) { s.SiteName = strings.Repeat("学", 81) },
		"site name invalid UTF-8":      func(s *Settings) { s.SiteName = invalidUTF8 },
		"announcement too long":        func(s *Settings) { s.SiteAnnouncement = strings.Repeat("通", 1001) },
		"announcement invalid UTF-8":   func(s *Settings) { s.SiteAnnouncement = invalidUTF8 },
		"soft delete below":            func(s *Settings) { s.SoftDeleteRetentionDays = 29 },
		"soft delete above":            func(s *Settings) { s.SoftDeleteRetentionDays = 366 },
		"audit retention below":        func(s *Settings) { s.AuditRetentionDays = 364 },
		"audit retention above":        func(s *Settings) { s.AuditRetentionDays = 2556 },
		"sample retention below":       func(s *Settings) { s.OperationalSampleRetentionDays = 0 },
		"sample retention above":       func(s *Settings) { s.OperationalSampleRetentionDays = 31 },
		"backup hour below":            func(s *Settings) { s.BackupHour = -1 },
		"backup hour above":            func(s *Settings) { s.BackupHour = 24 },
		"backup minute below":          func(s *Settings) { s.BackupMinute = -1 },
		"backup minute above":          func(s *Settings) { s.BackupMinute = 60 },
		"backup timezone":              func(s *Settings) { s.BackupTimezone = "UTC" },
		"disk warning below":           func(s *Settings) { s.DiskWarningPercent = 0 },
		"disk warning above":           func(s *Settings) { s.DiskWarningPercent, s.DiskCriticalPercent = 100, 101 },
		"disk critical equal warning":  func(s *Settings) { s.DiskWarningPercent, s.DiskCriticalPercent = 50, 50 },
		"disk critical above":          func(s *Settings) { s.DiskWarningPercent, s.DiskCriticalPercent = 99, 101 },
		"AI warning below":             func(s *Settings) { s.AIErrorWarningPercent = 0 },
		"AI warning above":             func(s *Settings) { s.AIErrorWarningPercent, s.AIErrorCriticalPercent = 100, 101 },
		"AI critical equal warning":    func(s *Settings) { s.AIErrorWarningPercent, s.AIErrorCriticalPercent = 50, 50 },
		"AI critical above":            func(s *Settings) { s.AIErrorWarningPercent, s.AIErrorCriticalPercent = 99, 101 },
		"queue warning below":          func(s *Settings) { s.ProcessingQueueWarning = 0 },
		"queue critical equal warning": func(s *Settings) { s.ProcessingQueueWarning, s.ProcessingQueueCritical = 20, 20 },
		"queue critical below warning": func(s *Settings) { s.ProcessingQueueWarning, s.ProcessingQueueCritical = 20, 19 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			settings := validSettings()
			mutate(&settings)
			if err := ValidateSettings(settings); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSettingsServiceRequiresActiveAdminWithAuditContext(t *testing.T) {
	store := &fakeStore{settings: validSettings()}
	service := NewService(store)
	admin := operationsAdmin(uuid.New())
	for name, mutate := range map[string]func(*Principal){
		"student":          func(p *Principal) { p.User.Role = auth.RoleStudent },
		"inactive":         func(p *Principal) { p.User.Status = auth.StatusDisabled },
		"nil user":         func(p *Principal) { p.User.ID = uuid.Nil },
		"blank request ID": func(p *Principal) { p.RequestID = " " },
		"long request ID":  func(p *Principal) { p.RequestID = strings.Repeat("r", 65) },
		"nil IP":           func(p *Principal) { p.IP = nil },
		"malformed IP":     func(p *Principal) { p.IP = net.IP{1} },
	} {
		t.Run(name, func(t *testing.T) {
			principal := admin
			mutate(&principal)
			if _, err := service.GetSettings(context.Background(), principal); !errors.Is(err, ErrForbidden) {
				t.Fatalf("get error=%v", err)
			}
			if _, err := service.UpdateSettings(context.Background(), principal, validSettings()); !errors.Is(err, ErrForbidden) {
				t.Fatalf("update error=%v", err)
			}
		})
	}
	if store.getCalls != 0 || store.updateCalls != 0 {
		t.Fatalf("unauthorized calls reached store: get=%d update=%d", store.getCalls, store.updateCalls)
	}
}

func TestSettingsServiceAuditsOnlyAuthenticatedHighRiskRejections(t *testing.T) {
	admin := operationsAdmin(uuid.New())
	for name, tc := range map[string]struct {
		mutate func(*Settings)
		reason string
	}{
		"soft delete retention": {func(s *Settings) { s.SoftDeleteRetentionDays = 29 }, "retention"},
		"audit retention":       {func(s *Settings) { s.AuditRetentionDays = 364 }, "retention"},
		"sample retention":      {func(s *Settings) { s.OperationalSampleRetentionDays = 0 }, "retention"},
		"backup hour":           {func(s *Settings) { s.BackupHour = 24 }, "backup_schedule"},
		"backup minute":         {func(s *Settings) { s.BackupMinute = 60 }, "backup_schedule"},
		"backup timezone":       {func(s *Settings) { s.BackupTimezone = "UTC" }, "backup_schedule"},
		"disk threshold":        {func(s *Settings) { s.DiskCriticalPercent = s.DiskWarningPercent }, "threshold"},
		"AI threshold":          {func(s *Settings) { s.AIErrorCriticalPercent = s.AIErrorWarningPercent }, "threshold"},
		"queue threshold":       {func(s *Settings) { s.ProcessingQueueCritical = s.ProcessingQueueWarning }, "threshold"},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{settings: validSettings()}
			service := NewService(store)
			highRisk := validSettings()
			tc.mutate(&highRisk)
			if _, err := service.UpdateSettings(context.Background(), admin, highRisk); !errors.Is(err, ErrInvalid) {
				t.Fatalf("high-risk error=%v", err)
			}
			if store.rejectionCalls != 1 || store.rejectionReason != tc.reason {
				t.Fatalf("rejections=%d reason=%q", store.rejectionCalls, store.rejectionReason)
			}
		})
	}

	store := &fakeStore{settings: validSettings()}
	service := NewService(store)
	mundane := validSettings()
	mundane.SiteName = ""
	if _, err := service.UpdateSettings(context.Background(), admin, mundane); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mundane error=%v", err)
	}
	if store.rejectionCalls != 0 {
		t.Fatalf("mundane invalid input was audited as high risk: %d", store.rejectionCalls)
	}

	student := admin
	student.User.Role = auth.RoleStudent
	highRisk := validSettings()
	highRisk.AuditRetentionDays = 0
	if _, err := service.UpdateSettings(context.Background(), student, highRisk); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized error=%v", err)
	}
	if store.rejectionCalls != 0 {
		t.Fatalf("unauthorized rejection was audited: %d", store.rejectionCalls)
	}

	auditFailure := errors.New("settings rejection audit unavailable")
	failingStore := &fakeStore{settings: validSettings(), rejectionErr: auditFailure}
	if _, err := NewService(failingStore).UpdateSettings(context.Background(), admin, highRisk); !errors.Is(err, auditFailure) {
		t.Fatalf("audit failure error=%v", err)
	}
	if failingStore.updateCalls != 0 {
		t.Fatal("audit failure reached settings mutation")
	}
}

func TestPostgresSettingsDefaultsConflictAuditAndRollback(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	admin := seedOperationsAdmin(t, ctx, pool)
	store := NewPostgresStore(pool)
	service := NewService(store)

	settings, err := service.GetSettings(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	want := validSettings()
	if settings.Version != want.Version || settings.SiteName != want.SiteName ||
		settings.SiteAnnouncement != want.SiteAnnouncement ||
		settings.SoftDeleteRetentionDays != want.SoftDeleteRetentionDays ||
		settings.AuditRetentionDays != want.AuditRetentionDays ||
		settings.OperationalSampleRetentionDays != want.OperationalSampleRetentionDays ||
		settings.BackupHour != want.BackupHour || settings.BackupMinute != want.BackupMinute ||
		settings.BackupTimezone != want.BackupTimezone ||
		settings.DiskWarningPercent != want.DiskWarningPercent ||
		settings.DiskCriticalPercent != want.DiskCriticalPercent ||
		settings.AIErrorWarningPercent != want.AIErrorWarningPercent ||
		settings.AIErrorCriticalPercent != want.AIErrorCriticalPercent ||
		settings.ProcessingQueueWarning != want.ProcessingQueueWarning ||
		settings.ProcessingQueueCritical != want.ProcessingQueueCritical ||
		settings.UpdatedBy != uuid.Nil || settings.UpdatedAt.IsZero() {
		t.Fatalf("unexpected defaults: %#v", settings)
	}

	settings.SiteName = "Vayzra 学习"
	updated, err := service.UpdateSettings(ctx, admin, settings)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != settings.Version+1 || updated.UpdatedBy != admin.User.ID ||
		updated.SiteName != "Vayzra 学习" || updated.UpdatedAt.IsZero() {
		t.Fatalf("updated=%#v", updated)
	}
	if _, err := service.UpdateSettings(ctx, admin, settings); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	var audits int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM audit_logs
WHERE action='operations.settings_updated' AND target_type='system_settings'
  AND target_id='global' AND actor_user_id=$1 AND request_id=$2 AND ip=$3
  AND metadata='{}'::jsonb`, admin.User.ID, admin.RequestID, admin.IP).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("settings audits=%d", audits)
	}

	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION operations_reject_audit() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'forced operations audit failure'; END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER operations_reject_audit BEFORE INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION operations_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS operations_reject_audit ON audit_logs;
DROP FUNCTION IF EXISTS operations_reject_audit()`)
	})
	before := updated
	before.SiteAnnouncement = "must roll back"
	if _, err := service.UpdateSettings(ctx, admin, before); err == nil {
		t.Fatal("expected forced audit failure")
	}
	var version int64
	var announcement string
	if err := pool.QueryRow(ctx, `SELECT version,site_announcement FROM system_settings WHERE singleton_id=true`).
		Scan(&version, &announcement); err != nil {
		t.Fatal(err)
	}
	if version != updated.Version || announcement != updated.SiteAnnouncement {
		t.Fatalf("mutation escaped rollback: version=%d announcement=%q", version, announcement)
	}
}

func TestPostgresHighRiskSettingsRejectionAuditIsRedacted(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	admin := seedOperationsAdmin(t, ctx, pool)
	service := NewService(NewPostgresStore(pool))
	for name, tc := range map[string]struct {
		mutate func(*Settings)
		reason string
	}{
		"soft delete retention": {func(s *Settings) { s.SoftDeleteRetentionDays = 29 }, "retention"},
		"audit retention":       {func(s *Settings) { s.AuditRetentionDays = 364 }, "retention"},
		"sample retention":      {func(s *Settings) { s.OperationalSampleRetentionDays = 0 }, "retention"},
		"backup hour":           {func(s *Settings) { s.BackupHour = 24 }, "backup_schedule"},
		"backup minute":         {func(s *Settings) { s.BackupMinute = 60 }, "backup_schedule"},
		"backup timezone":       {func(s *Settings) { s.BackupTimezone = "UTC" }, "backup_schedule"},
		"disk threshold":        {func(s *Settings) { s.DiskCriticalPercent = s.DiskWarningPercent }, "threshold"},
		"AI threshold":          {func(s *Settings) { s.AIErrorCriticalPercent = s.AIErrorWarningPercent }, "threshold"},
		"queue threshold":       {func(s *Settings) { s.ProcessingQueueCritical = s.ProcessingQueueWarning }, "threshold"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := validSettings()
			settings.SiteAnnouncement = "submitted-marker-must-not-leak"
			tc.mutate(&settings)
			if _, err := service.UpdateSettings(ctx, admin, settings); !errors.Is(err, ErrInvalid) {
				t.Fatalf("rejection error=%v", err)
			}
			var metadata []byte
			if err := pool.QueryRow(ctx, `
SELECT metadata FROM audit_logs
WHERE action='operations.settings_rejected'
  AND target_type='system_settings' AND target_id='global'
  AND actor_user_id=$1 AND request_id=$2 AND ip=$3
ORDER BY id DESC LIMIT 1`,
				admin.User.ID, admin.RequestID, admin.IP).Scan(&metadata); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(metadata, []byte(`"category": "high_risk"`)) ||
				!bytes.Contains(metadata, []byte(`"reason": "`+tc.reason+`"`)) {
				t.Fatalf("unexpected rejection metadata=%s", metadata)
			}
			if bytes.Contains(metadata, []byte(settings.SiteAnnouncement)) ||
				bytes.Contains(metadata, []byte(settings.SiteName)) ||
				bytes.Contains(metadata, []byte("365")) {
				t.Fatalf("settings values leaked in rejection metadata=%s", metadata)
			}
		})
	}
	var version int64
	if err := pool.QueryRow(ctx, `SELECT version FROM system_settings WHERE singleton_id=true`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("rejected settings mutated version=%d", version)
	}
}

func TestPostgresLeaseLifecycleAndAdvisoryGate(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	owner := uuid.New()
	expires := time.Now().UTC().Add(time.Minute)

	sharedRelease, err := store.AcquireShared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := store.AcquireLease(blockedCtx, LeaseRequest{Mode: "draining", OwnerID: owner, ExpiresAt: expires}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive acquisition while shared held error=%v", err)
	}
	sharedRelease()

	lease, err := store.AcquireLease(ctx, LeaseRequest{Mode: "draining", OwnerID: owner, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Mode != "draining" || lease.OwnerID != owner || len(lease.Token) != 32 ||
		lease.Version != 2 || !lease.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("lease=%#v", lease)
	}
	tokenHash := sha256.Sum256(lease.Token)
	var storedHash []byte
	if err := pool.QueryRow(ctx, `SELECT lease_token_hash FROM operational_modes WHERE singleton_id=true`).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedHash, tokenHash[:]) || bytes.Equal(storedHash, lease.Token) {
		t.Fatal("lease token was not stored solely as SHA-256")
	}
	mode, err := store.GetMode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode != lease.Mode || mode.OwnerID != lease.OwnerID || mode.Version != lease.Version ||
		mode.ExpiresAt.IsZero() {
		t.Fatalf("mode=%#v lease=%#v", mode, lease)
	}

	if _, err := store.AcquireLease(ctx, LeaseRequest{Mode: "release", OwnerID: uuid.New(), ExpiresAt: expires}); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second acquisition error=%v", err)
	}
	renewedExpiry := expires.Add(time.Minute)
	renewed, err := store.RenewLease(ctx, lease, renewedExpiry)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Version != lease.Version+1 || !renewed.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("renewed=%#v", renewed)
	}
	backup, err := store.TransitionLease(ctx, renewed, "backup", renewedExpiry.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if backup.Mode != "backup" || backup.Version != renewed.Version+1 {
		t.Fatalf("transitioned=%#v", backup)
	}

	readCtx, readCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer readCancel()
	if release, err := store.AcquireShared(readCtx); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			release()
		}
		t.Fatalf("shared lock while lease held error=%v", err)
	}
	if err := store.ReleaseLease(ctx, backup); err != nil {
		t.Fatal(err)
	}
	release, err := store.AcquireShared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	mode, err = store.GetMode(ctx)
	if err != nil || mode.Mode != "normal" || mode.OwnerID != uuid.Nil || !mode.ExpiresAt.IsZero() ||
		mode.Version != backup.Version+1 {
		t.Fatalf("released mode=%#v err=%v", mode, err)
	}
}

func TestPostgresExpiredLeaseTakeoverIsAuditedWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	oldOwner, newOwner := uuid.New(), uuid.New()
	oldToken := bytes.Repeat([]byte{0x5a}, 32)
	oldHash := sha256.Sum256(oldToken)
	if _, err := pool.Exec(ctx, `
UPDATE operational_modes
SET mode='backup',owner_id=$1,lease_token_hash=$2,
    lease_expires_at=now()-interval '1 minute',entered_at=now()-interval '2 minutes',
    updated_at=now(),version=8
WHERE singleton_id=true`, oldOwner, oldHash[:]); err != nil {
		t.Fatal(err)
	}

	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "release", OwnerID: newOwner, ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ReleaseLease(context.Background(), lease) })
	if lease.OwnerID != newOwner || lease.Mode != "release" || lease.Version != 9 ||
		bytes.Equal(lease.Token, oldToken) {
		t.Fatalf("takeover=%#v", lease)
	}
	var actor *uuid.UUID
	var metadata string
	if err := pool.QueryRow(ctx, `
SELECT actor_user_id,metadata::text FROM audit_logs
WHERE action='operations.lease_taken_over' AND target_type='operational_mode'
  AND target_id='global' AND request_id='operations-lease-takeover'
  AND ip='127.0.0.1'::inet`).Scan(&actor, &metadata); err != nil {
		t.Fatal(err)
	}
	if actor != nil || metadata != "{}" {
		t.Fatalf("unsafe takeover audit actor=%v metadata=%s", actor, metadata)
	}
}

func TestPostgresExpiredLeaseTakeoverRollsBackWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	oldOwner := uuid.New()
	oldHash := sha256.Sum256(bytes.Repeat([]byte{0x33}, 32))
	if _, err := pool.Exec(ctx, `
UPDATE operational_modes
SET mode='backup',owner_id=$1,lease_token_hash=$2,
    lease_expires_at=now()-interval '1 minute',entered_at=now()-interval '2 minutes',
    updated_at=now(),version=8
WHERE singleton_id=true`, oldOwner, oldHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION operations_reject_audit() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'forced operations audit failure'; END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER operations_reject_audit BEFORE INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION operations_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS operations_reject_audit ON audit_logs;
DROP FUNCTION IF EXISTS operations_reject_audit()`)
	})

	if _, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "release", OwnerID: uuid.New(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	}); err == nil {
		t.Fatal("expected forced takeover audit failure")
	}
	var mode string
	var owner uuid.UUID
	var hash []byte
	var version int64
	if err := pool.QueryRow(ctx, `
SELECT mode,owner_id,lease_token_hash,version
FROM operational_modes WHERE singleton_id=true`).
		Scan(&mode, &owner, &hash, &version); err != nil {
		t.Fatal(err)
	}
	if mode != "backup" || owner != oldOwner || !bytes.Equal(hash, oldHash[:]) || version != 8 {
		t.Fatalf("takeover escaped rollback: mode=%q owner=%s hash=%x version=%d", mode, owner, hash, version)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, operationsAdvisoryKey).Scan(&acquired); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if !acquired {
		conn.Release()
		t.Fatal("failed takeover retained exclusive advisory lock")
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, operationsAdvisoryKey); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	if release, err := store.AcquireShared(ctx); !errors.Is(err, ErrLeaseHeld) {
		if err == nil {
			release()
		}
		t.Fatalf("durable non-normal mode did not fail closed: %v", err)
	}
}

func TestPostgresLeaseRejectsStaleOwnerWithoutClearingCurrentLease(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ReleaseLease(context.Background(), lease) })

	stale := lease
	stale.Token = bytes.Repeat([]byte{0x11}, 32)
	stale.OwnerID = uuid.New()
	for name, mutate := range map[string]func() error{
		"renew": func() error {
			_, err := store.RenewLease(ctx, stale, time.Now().UTC().Add(2*time.Minute))
			return err
		},
		"transition": func() error {
			_, err := store.TransitionLease(ctx, stale, "backup", time.Now().UTC().Add(2*time.Minute))
			return err
		},
		"release": func() error { return store.ReleaseLease(ctx, stale) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); !errors.Is(err, ErrStaleLease) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	mode, err := store.GetMode(ctx)
	if err != nil || mode.Mode != lease.Mode || mode.OwnerID != lease.OwnerID || mode.Version != lease.Version {
		t.Fatalf("stale operation changed mode=%#v err=%v", mode, err)
	}
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseExclusiveConnectionDiscardsSessionWhenUnlockWasNotOwned(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgresWithMaxConns(t, 2)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var backendPID int
	if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		conn.Release()
		t.Fatal(err)
	}

	releaseExclusiveConnection(conn)

	var stillConnected int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM pg_stat_activity WHERE pid=$1`, backendPID).
		Scan(&stillConnected); err != nil {
		t.Fatal(err)
	}
	if stillConnected != 0 {
		t.Fatalf("session without owned advisory lock was returned to pool: pid=%d", backendPID)
	}
}

func TestCanceledLeaseMutationRetainsExclusiveSessionAndCanRetry(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.ReleaseLease(context.Background(), lease)
		_, _ = pool.Exec(context.Background(), `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=now(),version=version+1
WHERE singleton_id=true`)
	})

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.RenewLease(canceled, lease, time.Now().UTC().Add(2*time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("renew error=%v", err)
	}
	store.mu.Lock()
	heldSessions := len(store.sessions)
	store.mu.Unlock()
	if heldSessions != 1 {
		t.Fatalf("canceled mutation retained %d lease session entries", heldSessions)
	}

	probeCtx, probeCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer probeCancel()
	if release, err := store.AcquireShared(probeCtx); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			release()
		}
		t.Fatalf("canceled mutation opened shared gate: %v", err)
	}
	mode, err := store.GetMode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode != "draining" || mode.OwnerID != lease.OwnerID {
		t.Fatalf("canceled mutation changed durable mode: %#v", mode)
	}
	renewed, err := store.RenewLease(ctx, lease, time.Now().UTC().Add(3*time.Minute))
	if err != nil {
		t.Fatalf("retry renew: %v", err)
	}
	if err := store.ReleaseLease(ctx, renewed); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledLeaseReleaseRetainsExclusiveSessionAndCanRetry(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "backup", OwnerID: uuid.New(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.ReleaseLease(canceled, lease); !errors.Is(err, context.Canceled) {
		t.Fatalf("release error=%v", err)
	}
	mode, err := store.GetMode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode != "backup" || mode.OwnerID != lease.OwnerID {
		t.Fatalf("canceled release changed non-normal mode: %#v", mode)
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer probeCancel()
	if release, err := store.AcquireShared(probeCtx); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			release()
		}
		t.Fatalf("canceled release opened shared gate: %v", err)
	}
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
}

func TestRenewLeasePreservesCurrentTransitionedMode(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	original, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	transitioned, err := store.TransitionLease(ctx, original, "backup", time.Now().UTC().Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ReleaseLease(context.Background(), transitioned) })

	renewed, err := store.RenewLease(ctx, original, time.Now().UTC().Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Mode != "backup" {
		t.Fatalf("renewal reverted current mode: %#v", renewed)
	}
	mode, err := store.GetMode(ctx)
	if err != nil || mode.Mode != "backup" {
		t.Fatalf("durable mode=%#v err=%v", mode, err)
	}
	if err := store.ReleaseLease(ctx, renewed); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredLeaseOwnerCannotRenewAndTakeoverCanAcquire(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	oldStore := NewPostgresStore(pool)
	expiresAt := time.Now().UTC().Add(150 * time.Millisecond)
	oldLease, err := oldStore.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldStore.ReleaseLease(context.Background(), oldLease) })
	timer := time.NewTimer(time.Until(expiresAt) + 50*time.Millisecond)
	defer timer.Stop()
	<-timer.C

	sharedCtx, sharedCancel := context.WithTimeout(ctx, time.Second)
	defer sharedCancel()
	if release, err := oldStore.AcquireShared(sharedCtx); !errors.Is(err, ErrLeaseHeld) {
		if err == nil {
			release()
		}
		t.Fatalf("shared gate did not fail closed after expiry: %v", err)
	}
	newStore := NewPostgresStore(pool)
	takeoverCtx, takeoverCancel := context.WithTimeout(ctx, time.Second)
	defer takeoverCancel()
	takeover, err := newStore.AcquireLease(takeoverCtx, LeaseRequest{
		Mode: "release", OwnerID: uuid.New(), ExpiresAt: time.Now().UTC().Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("takeover after stale renewal: %v", err)
	}
	if takeover.Mode != "release" || takeover.OwnerID == oldLease.OwnerID {
		t.Fatalf("takeover=%#v", takeover)
	}
	if _, err := oldStore.RenewLease(ctx, oldLease, time.Now().UTC().Add(2*time.Minute)); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired owner renewal error=%v", err)
	}
	if err := newStore.ReleaseLease(ctx, takeover); err != nil {
		t.Fatal(err)
	}
}

func TestRenewedLeaseIgnoresAlreadyWaitingExpiryCallback(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ReleaseLease(context.Background(), lease) })
	tokenHash := sha256.Sum256(lease.Token)
	store.mu.Lock()
	session := store.sessions[tokenHash]
	store.mu.Unlock()
	if session == nil {
		t.Fatal("missing acquired lease session")
	}

	session.mu.Lock()
	store.scheduleLeaseExpiryLocked(tokenHash, session, time.Now().UTC().Add(40*time.Millisecond))
	newExpiry := time.Now().UTC().Add(300 * time.Millisecond)
	type renewResult struct {
		lease Lease
		err   error
	}
	renewed := make(chan renewResult, 1)
	go func() {
		next, err := store.RenewLease(ctx, lease, newExpiry)
		renewed <- renewResult{lease: next, err: err}
	}()
	time.Sleep(80 * time.Millisecond)
	session.mu.Unlock()

	var result renewResult
	select {
	case result = <-renewed:
	case <-time.After(time.Second):
		t.Fatal("renewal did not complete")
	}
	if result.err != nil {
		t.Fatalf("renew lease: %v", result.err)
	}
	lease = result.lease
	time.Sleep(30 * time.Millisecond)
	store.mu.Lock()
	_, retained := store.sessions[tokenHash]
	store.mu.Unlock()
	if !retained {
		t.Fatal("stale expiry callback released the rescheduled lease")
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer probeCancel()
	if release, err := store.AcquireShared(probeCtx); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			release()
		}
		t.Fatalf("stale expiry callback opened shared gate: %v", err)
	}

	timer := time.NewTimer(time.Until(newExpiry) + 50*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	store.mu.Lock()
	_, retained = store.sessions[tokenHash]
	store.mu.Unlock()
	if retained {
		t.Fatal("rescheduled lease remained after its new expiry")
	}
}

func TestCanceledRenewAfterCommitReschedulesExpiryBeforeReturning(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	oldExpiry := time.Now().UTC().Add(400 * time.Millisecond)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: oldExpiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ReleaseLease(context.Background(), lease) })

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `
SELECT singleton_id FROM operational_modes
WHERE singleton_id=true FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	newExpiry := time.Now().UTC().Add(900 * time.Millisecond)
	renewCtx, cancelRenew := context.WithCancel(ctx)
	type renewResult struct {
		lease Lease
		err   error
	}
	renewed := make(chan renewResult, 1)
	go func() {
		next, err := store.RenewLease(renewCtx, lease, newExpiry)
		renewed <- renewResult{lease: next, err: err}
	}()

	waitDeadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1
	FROM pg_stat_activity
	WHERE datname=current_database()
	  AND pid<>pg_backend_pid()
	  AND state='active'
	  AND wait_event_type='Lock'
	  AND query LIKE '%SELECT mode,owner_id,lease_token_hash%'
)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("renewal did not block on the operational mode row")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancelRenew()
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var result renewResult
	select {
	case result = <-renewed:
	case <-time.After(time.Second):
		t.Fatal("renewal did not return after the row blocker committed")
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("renewal error = %v, want context canceled", result.err)
	}
	var durableExpiry time.Time
	if err := pool.QueryRow(ctx, `
SELECT lease_expires_at FROM operational_modes WHERE singleton_id=true`,
	).Scan(&durableExpiry); err != nil {
		t.Fatal(err)
	}
	if delta := durableExpiry.Sub(newExpiry); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("durable expiry = %v, want %v", durableExpiry, newExpiry)
	}

	timer := time.NewTimer(time.Until(oldExpiry) + 75*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	tokenHash := sha256.Sum256(lease.Token)
	store.mu.Lock()
	_, retained := store.sessions[tokenHash]
	store.mu.Unlock()
	if !retained {
		t.Fatal("committed renewal retained the old expiry timer")
	}

	timer.Reset(time.Until(newExpiry) + 75*time.Millisecond)
	<-timer.C
	store.mu.Lock()
	_, retained = store.sessions[tokenHash]
	store.mu.Unlock()
	if retained {
		t.Fatal("committed renewal remained registered after its new expiry")
	}
}

func validSettings() Settings {
	return Settings{
		Version:                        1,
		SiteName:                       "HappyLearn",
		SiteAnnouncement:               "",
		SoftDeleteRetentionDays:        30,
		AuditRetentionDays:             365,
		OperationalSampleRetentionDays: 7,
		BackupHour:                     3,
		BackupMinute:                   0,
		BackupTimezone:                 "Asia/Shanghai",
		DiskWarningPercent:             75,
		DiskCriticalPercent:            90,
		AIErrorWarningPercent:          10,
		AIErrorCriticalPercent:         25,
		ProcessingQueueWarning:         20,
		ProcessingQueueCritical:        100,
	}
}

func operationsAdmin(id uuid.UUID) Principal {
	return Principal{
		User:      auth.User{ID: id, Role: auth.RoleAdmin, Status: auth.StatusActive},
		RequestID: "operations-request",
		IP:        net.ParseIP("192.0.2.40"),
	}
}

func migratedOperationsStore(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
DROP TRIGGER IF EXISTS operations_reject_audit ON audit_logs;
DROP FUNCTION IF EXISTS operations_reject_audit();
TRUNCATE TABLE users CASCADE;
INSERT INTO system_settings(singleton_id) VALUES(true)
ON CONFLICT (singleton_id) DO NOTHING;
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=now(),version=1
WHERE singleton_id=true`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func seedOperationsAdmin(t *testing.T, ctx context.Context, pool *pgxpool.Pool) Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users(id,username,display_name,role,status,password_hash,must_change_password)
VALUES($1,$2,'Operations Admin','admin','active','x',false)`,
		id, "operations_"+strings.ReplaceAll(id.String(), "-", "")); err != nil {
		t.Fatal(err)
	}
	return operationsAdmin(id)
}

type fakeStore struct {
	settings              Settings
	getCalls, updateCalls int
	rejectionCalls        int
	rejectionReason       string
	rejectionErr          error
}

func (s *fakeStore) GetSettings(context.Context) (Settings, error) {
	s.getCalls++
	return s.settings, nil
}
func (s *fakeStore) UpdateSettings(_ context.Context, _ Principal, settings Settings) (Settings, error) {
	s.updateCalls++
	return settings, nil
}
func (s *fakeStore) AuditSettingsRejection(_ context.Context, _ Principal, reason string) error {
	s.rejectionCalls++
	s.rejectionReason = reason
	return s.rejectionErr
}
func (*fakeStore) GetMode(context.Context) (ModeSnapshot, error) { return ModeSnapshot{}, nil }
func (*fakeStore) AcquireLease(context.Context, LeaseRequest) (Lease, error) {
	return Lease{}, nil
}
func (*fakeStore) RenewLease(context.Context, Lease, time.Time) (Lease, error) {
	return Lease{}, nil
}
func (*fakeStore) TransitionLease(context.Context, Lease, string, time.Time) (Lease, error) {
	return Lease{}, nil
}
func (*fakeStore) ReleaseLease(context.Context, Lease) error { return nil }
