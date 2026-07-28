package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresUnsafeWriteGateFailsFastDuringActiveMaintenance(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	handlerCalls := atomic.Int32{}
	handler := UnsafeWriteGate(store)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			handlerCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		},
	))
	type response struct {
		code int
		body string
	}
	result := make(chan response, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodPost, "/api/v1/student/progress", nil),
		)
		result <- response{code: recorder.Code, body: recorder.Body.String()}
	}()
	select {
	case got := <-result:
		if got.code != http.StatusServiceUnavailable ||
			!strings.Contains(got.body, `"code":"maintenance_mode"`) ||
			handlerCalls.Load() != 0 {
			t.Fatalf(
				"status=%d calls=%d body=%s",
				got.code,
				handlerCalls.Load(),
				got.body,
			)
		}
	case <-time.After(time.Second):
		_ = store.ReleaseLease(ctx, lease)
		<-result
		t.Fatal("unsafe write did not fail fast during maintenance")
	}
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/student/progress", nil),
	)
	if recorder.Code != http.StatusNoContent || handlerCalls.Load() != 1 {
		t.Fatalf("status=%d calls=%d", recorder.Code, handlerCalls.Load())
	}
}

func TestPostgresSharedAdmissionTimeoutReturnsCleanConnectionToPool(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgresWithMaxConns(t, 3)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=now(),version=version+1
WHERE singleton_id=true`); err != nil {
		t.Fatal(err)
	}
	reserved, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reservedPID int
	if err := reserved.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&reservedPID); err != nil {
		reserved.Release()
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(time.Minute),
	})
	if err != nil {
		reserved.Release()
		t.Fatal(err)
	}
	reserved.Release()

	requireSharedAdmissionRejectedQuickly(t, store)
	reused, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reused.Release()
	var reusedPID int
	if err := reused.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&reusedPID); err != nil {
		t.Fatal(err)
	}
	if reusedPID != reservedPID {
		t.Fatalf("timed-out connection replaced: before=%d after=%d", reservedPID, reusedPID)
	}
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresClaimAdmissionRejectsDurableNonNormalMode(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	var admissionTx pgx.Tx
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if admissionTx != nil {
			if err := admissionTx.Rollback(cleanupCtx); err != nil &&
				!errors.Is(err, pgx.ErrTxClosed) {
				t.Errorf("rollback admission transaction: %v", err)
			}
		}
		tag, err := pool.Exec(cleanupCtx, `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=clock_timestamp(),version=version+1
WHERE singleton_id=true`)
		if err != nil {
			t.Errorf("restore operational mode: %v", err)
		} else if tag.RowsAffected() != 1 {
			t.Errorf("restore operational mode rows=%d", tag.RowsAffected())
		}
	})
	tokenHash := sha256.Sum256([]byte("durable-maintenance-mode"))
	if _, err := pool.Exec(ctx, `
UPDATE operational_modes
SET mode='draining',owner_id=$1,lease_token_hash=$2,
    lease_expires_at=clock_timestamp()+interval '1 minute',
    entered_at=clock_timestamp(),updated_at=clock_timestamp(),version=version+1
WHERE singleton_id=true`, uuid.New(), tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admissionTx = tx
	started := time.Now()
	if err := AdmitClaim(ctx, tx); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("admission error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("durable non-normal admission took %v", elapsed)
	}
}

func TestPostgresClaimAdmissionRestoresLockTimeoutAfterAdmission(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := AdmitClaim(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var lockTimeout string
	if err := tx.QueryRow(ctx, `SHOW lock_timeout`).Scan(&lockTimeout); err != nil {
		t.Fatal(err)
	}
	if lockTimeout != "0" {
		t.Fatalf("lock_timeout=%q", lockTimeout)
	}
}

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
			caseAdmin := admin
			caseAdmin.RequestID = "settings-reject-" + uuid.NewString()
			settings := validSettings()
			settings.SiteAnnouncement = "submitted-marker-must-not-leak"
			tc.mutate(&settings)
			if _, err := service.UpdateSettings(ctx, caseAdmin, settings); !errors.Is(err, ErrInvalid) {
				t.Fatalf("rejection error=%v", err)
			}
			var count int
			var metadata []byte
			if err := pool.QueryRow(ctx, `
SELECT count(*),min(metadata::text)::bytea FROM audit_logs
WHERE action='operations.settings_rejected'
  AND target_type='system_settings' AND target_id='global'
  AND actor_user_id=$1 AND request_id=$2 AND ip=$3
HAVING count(*) > 0`,
				caseAdmin.User.ID, caseAdmin.RequestID, caseAdmin.IP).Scan(&count, &metadata); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("rejection audits for request %q=%d", caseAdmin.RequestID, count)
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

func TestHighRiskSettingsRejectionAuditSurvivesPreCanceledContext(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	admin := seedOperationsAdmin(t, ctx, pool)
	admin.RequestID = "settings-reject-precanceled"
	service := NewService(NewPostgresStore(pool))
	settings := validSettings()
	settings.AuditRetentionDays = 0
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := service.UpdateSettings(canceledCtx, admin, settings); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pre-canceled rejection error=%v", err)
	}
	assertRedactedSettingsRejectionAudit(t, pool, admin, "retention", 1)
}

func TestHighRiskSettingsRejectionAuditSurvivesCancellationWhileBlocked(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	admin := seedOperationsAdmin(t, ctx, pool)
	admin.RequestID = "settings-reject-blocked"
	service := NewService(NewPostgresStore(pool))
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `LOCK TABLE audit_logs IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	settings := validSettings()
	settings.BackupTimezone = "UTC"
	rejectionCtx, cancelRejection := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := service.UpdateSettings(rejectionCtx, admin, settings)
		result <- err
	}()
	waitForBlockedPostgresQuery(t, pool, "INSERT INTO audit_logs")
	cancelRejection()
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("blocked rejection error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked rejection did not return after table lock released")
	}
	assertRedactedSettingsRejectionAudit(t, pool, admin, "backup_schedule", 1)
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
	type acquireResult struct {
		lease Lease
		err   error
	}
	acquired := make(chan acquireResult, 1)
	go func() {
		lease, err := store.AcquireLease(ctx, LeaseRequest{
			Mode: "draining", OwnerID: owner, ExpiresAt: expires,
		})
		acquired <- acquireResult{lease: lease, err: err}
	}()
	waitForBlockedPostgresQuery(t, pool, "SELECT pg_advisory_lock")
	sharedRelease()

	var lease Lease
	select {
	case result := <-acquired:
		if result.err != nil {
			t.Fatalf("exclusive acquisition after shared release: %v", result.err)
		}
		lease = result.lease
	case <-time.After(2 * time.Second):
		t.Fatal("exclusive acquisition did not drain and pass the shared gate")
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

	requireSharedAdmissionRejectedQuickly(t, store)
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

	requireSharedAdmissionRejectedQuickly(t, store)
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
	requireSharedAdmissionRejectedQuickly(t, store)
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
	expiresAt := postgresClock(t, pool).Add(700 * time.Millisecond)
	oldLease, err := oldStore.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldStore.ReleaseLease(context.Background(), oldLease) })
	waitForPostgresClock(t, pool, expiresAt)
	waitForLeaseSessionRelease(t, oldStore, sha256.Sum256(oldLease.Token))

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

func TestLeaseMutationAfterRowWaitUsesPostLockDatabaseTime(t *testing.T) {
	for name, mutate := range map[string]func(context.Context, *PostgresStore, Lease, time.Time) (Lease, error){
		"renew": func(ctx context.Context, store *PostgresStore, lease Lease, expiresAt time.Time) (Lease, error) {
			return store.RenewLease(ctx, lease, expiresAt)
		},
		"transition": func(ctx context.Context, store *PostgresStore, lease Lease, expiresAt time.Time) (Lease, error) {
			return store.TransitionLease(ctx, lease, "backup", expiresAt)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pool := migratedOperationsStore(t)
			store := NewPostgresStore(pool)
			oldExpiry := postgresClock(t, pool).Add(700 * time.Millisecond)
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

			type mutationResult struct {
				lease Lease
				err   error
			}
			result := make(chan mutationResult, 1)
			newExpiry := oldExpiry.Add(5 * time.Minute)
			go func() {
				updated, err := mutate(ctx, store, lease, newExpiry)
				result <- mutationResult{lease: updated, err: err}
			}()
			waitForBlockedPostgresQuery(t, pool, "SELECT mode,owner_id,lease_token_hash")
			waitForPostgresClock(t, pool, oldExpiry.Add(20*time.Millisecond))
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			select {
			case got := <-result:
				if !errors.Is(got.err, ErrStaleLease) {
					t.Fatalf("mutation after expiry returned lease=%#v err=%v", got.lease, got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("mutation did not return after row blocker committed")
			}

			newStore := NewPostgresStore(pool)
			takeoverCtx, cancelTakeover := context.WithTimeout(ctx, 2*time.Second)
			defer cancelTakeover()
			takeover, err := newStore.AcquireLease(takeoverCtx, LeaseRequest{
				Mode:      "release",
				OwnerID:   uuid.New(),
				ExpiresAt: postgresClock(t, pool).Add(time.Minute),
			})
			if err != nil {
				t.Fatalf("bounded takeover after stale mutation: %v", err)
			}
			if err := newStore.ReleaseLease(ctx, takeover); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAcquireTakeoverAfterRowWaitUsesPostLockDatabaseTime(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	oldOwner := uuid.New()
	oldHash := sha256.Sum256(bytes.Repeat([]byte{0x44}, 32))
	var staleExpiry time.Time
	if err := blocker.QueryRow(ctx, `
UPDATE operational_modes
SET mode='backup',owner_id=$1,lease_token_hash=$2,
    lease_expires_at=clock_timestamp()+interval '700 milliseconds',
    entered_at=clock_timestamp(),updated_at=clock_timestamp(),version=version+1
WHERE singleton_id=true
RETURNING lease_expires_at`, oldOwner, oldHash[:]).Scan(&staleExpiry); err != nil {
		t.Fatal(err)
	}

	type acquireResult struct {
		lease Lease
		err   error
	}
	result := make(chan acquireResult, 1)
	request := LeaseRequest{
		Mode:      "release",
		OwnerID:   uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(5 * time.Minute),
	}
	go func() {
		lease, err := store.AcquireLease(ctx, request)
		result <- acquireResult{lease: lease, err: err}
	}()
	waitForBlockedPostgresQuery(t, pool, "SELECT mode,owner_id,lease_token_hash")
	waitForPostgresClock(t, pool, staleExpiry.Add(20*time.Millisecond))
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var acquired Lease
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("takeover after row wait: %v", got.err)
		}
		acquired = got.lease
	case <-time.After(2 * time.Second):
		t.Fatal("acquisition did not return after row blocker committed")
	}
	if acquired.OwnerID != request.OwnerID || acquired.Mode != request.Mode {
		t.Fatalf("acquired lease=%#v request=%#v", acquired, request)
	}
	if err := store.ReleaseLease(ctx, acquired); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledAcquireAfterPreflightReturnsCommittedManagedLease(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
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

	request := LeaseRequest{
		Mode:      "draining",
		OwnerID:   uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(5 * time.Minute),
	}
	acquireCtx, cancelAcquire := context.WithCancel(ctx)
	type acquireResult struct {
		lease Lease
		err   error
	}
	result := make(chan acquireResult, 1)
	go func() {
		lease, err := store.AcquireLease(acquireCtx, request)
		result <- acquireResult{lease: lease, err: err}
	}()
	waitForBlockedPostgresQuery(t, pool, "SELECT mode,owner_id,lease_token_hash")
	cancelAcquire()
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var committed Lease
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("committed acquisition returned error: %v", got.err)
		}
		committed = got.lease
	case <-time.After(2 * time.Second):
		t.Fatal("acquisition did not return after row blocker committed")
	}
	if committed.OwnerID != request.OwnerID || committed.Mode != request.Mode ||
		len(committed.Token) != sha256.Size {
		t.Fatalf("committed lease=%#v", committed)
	}
	tokenHash := sha256.Sum256(committed.Token)
	var durableOwner uuid.UUID
	var durableHash []byte
	if err := pool.QueryRow(ctx, `
SELECT owner_id,lease_token_hash
FROM operational_modes WHERE singleton_id=true`,
	).Scan(&durableOwner, &durableHash); err != nil {
		t.Fatal(err)
	}
	if durableOwner != committed.OwnerID || !bytes.Equal(durableHash, tokenHash[:]) {
		t.Fatalf("durable owner=%s hash=%x, lease owner=%s hash=%x",
			durableOwner, durableHash, committed.OwnerID, tokenHash)
	}
	store.mu.Lock()
	session := store.sessions[tokenHash]
	store.mu.Unlock()
	if session == nil {
		t.Fatal("committed acquisition was not registered as a managed session")
	}
	session.mu.Lock()
	managed := !session.released && session.timer != nil
	session.mu.Unlock()
	if !managed {
		t.Fatal("committed acquisition session was not active and timed")
	}
	if err := store.ReleaseLease(ctx, committed); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedLeaseOperationsRecoverAfterAmbiguousConnectionLoss(t *testing.T) {
	for _, operation := range []string{"acquire", "renew", "transition", "release"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			pool := migratedOperationsStore(t)
			store := NewPostgresStore(pool)
			t.Cleanup(func() {
				closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancelClose()
				_ = store.Close(closeCtx)
			})
			var terminatedPID int
			store.afterCommit = terminatingCommitHook(pool, operation, &terminatedPID)

			request := LeaseRequest{
				Mode:      "draining",
				OwnerID:   uuid.New(),
				ExpiresAt: postgresClock(t, pool).Add(5 * time.Minute),
			}
			lease, err := store.AcquireLease(ctx, request)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if operation == "acquire" {
				assertRecoveredLeaseSession(t, store, lease, terminatedPID)
			}

			switch operation {
			case "renew":
				renewed, err := store.RenewLease(
					ctx,
					lease,
					postgresClock(t, pool).Add(10*time.Minute),
				)
				if err != nil {
					t.Fatalf("renew after committed connection loss: %v", err)
				}
				if renewed.Version != lease.Version+1 || renewed.Mode != lease.Mode {
					t.Fatalf("renewed lease=%#v", renewed)
				}
				lease = renewed
				assertRecoveredLeaseSession(t, store, lease, terminatedPID)
			case "transition":
				transitioned, err := store.TransitionLease(
					ctx,
					lease,
					"backup",
					postgresClock(t, pool).Add(10*time.Minute),
				)
				if err != nil {
					t.Fatalf("transition after committed connection loss: %v", err)
				}
				if transitioned.Version != lease.Version+1 || transitioned.Mode != "backup" {
					t.Fatalf("transitioned lease=%#v", transitioned)
				}
				lease = transitioned
				assertRecoveredLeaseSession(t, store, lease, terminatedPID)
			case "release":
				if err := store.ReleaseLease(ctx, lease); err != nil {
					t.Fatalf("release after committed connection loss: %v", err)
				}
				mode, err := store.GetMode(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if mode.Mode != "normal" || mode.OwnerID != uuid.Nil || !mode.ExpiresAt.IsZero() {
					t.Fatalf("ambiguous committed release left mode=%#v", mode)
				}
				store.mu.Lock()
				retained := len(store.sessions)
				store.mu.Unlock()
				if retained != 0 {
					t.Fatalf("ambiguous committed release retained %d sessions", retained)
				}
				assertExclusiveAdvisoryAvailable(t, pool)
				return
			}

			if err := store.ReleaseLease(ctx, lease); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLeaseOperationsRecoverWhenDedicatedBackendDiesBetweenCalls(t *testing.T) {
	for _, operation := range []string{"renew", "transition", "release"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			pool := migratedOperationsStore(t)
			store := NewPostgresStore(pool)
			t.Cleanup(func() {
				closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancelClose()
				_ = store.Close(closeCtx)
			})

			lease, err := store.AcquireLease(ctx, LeaseRequest{
				Mode:      "draining",
				OwnerID:   uuid.New(),
				ExpiresAt: postgresClock(t, pool).Add(5 * time.Minute),
			})
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			terminatedPID := terminateLeaseSessionBackend(t, pool, store, lease)

			switch operation {
			case "renew":
				renewed, err := store.RenewLease(
					ctx,
					lease,
					postgresClock(t, pool).Add(10*time.Minute),
				)
				if err != nil {
					t.Fatalf("renew after backend termination: %v", err)
				}
				if renewed.Version != lease.Version+1 || renewed.Mode != lease.Mode {
					t.Fatalf("renewed lease=%#v", renewed)
				}
				lease = renewed
				assertRecoveredLeaseSession(t, store, lease, terminatedPID)
			case "transition":
				transitioned, err := store.TransitionLease(
					ctx,
					lease,
					"backup",
					postgresClock(t, pool).Add(10*time.Minute),
				)
				if err != nil {
					t.Fatalf("transition after backend termination: %v", err)
				}
				if transitioned.Version != lease.Version+1 || transitioned.Mode != "backup" {
					t.Fatalf("transitioned lease=%#v", transitioned)
				}
				lease = transitioned
				assertRecoveredLeaseSession(t, store, lease, terminatedPID)
			case "release":
				if err := store.ReleaseLease(ctx, lease); err != nil {
					t.Fatalf("release after backend termination: %v", err)
				}
				mode, err := store.GetMode(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if mode.Mode != "normal" || mode.OwnerID != uuid.Nil || !mode.ExpiresAt.IsZero() {
					t.Fatalf("release left mode=%#v", mode)
				}
				store.mu.Lock()
				retained := len(store.sessions)
				store.mu.Unlock()
				if retained != 0 {
					t.Fatalf("release retained %d sessions", retained)
				}
				assertExclusiveAdvisoryAvailable(t, pool)
				return
			}

			if err := store.ReleaseLease(ctx, lease); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLeaseOperationsScheduleExpiryFromAuthoritativePostCommitDatabaseTime(t *testing.T) {
	for _, operation := range []string{"acquire", "renew", "transition"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			pool := migratedOperationsStore(t)
			store := NewPostgresStore(pool)
			t.Cleanup(func() {
				closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancelClose()
				_ = store.Close(closeCtx)
			})

			var current Lease
			if operation != "acquire" {
				var err error
				current, err = store.AcquireLease(ctx, LeaseRequest{
					Mode:      "draining",
					OwnerID:   uuid.New(),
					ExpiresAt: postgresClock(t, pool).Add(5 * time.Minute),
				})
				if err != nil {
					t.Fatalf("initial acquire: %v", err)
				}
			}

			committed := make(chan struct{})
			continueAfterCommit := make(chan struct{})
			store.afterCommit = func(committedOperation string, _ *pgxpool.Conn) error {
				if committedOperation == operation {
					close(committed)
					<-continueAfterCommit
				}
				return nil
			}
			expiresAt := postgresClock(t, pool).Add(time.Second)
			type leaseResult struct {
				lease Lease
				err   error
			}
			result := make(chan leaseResult, 1)
			go func() {
				var lease Lease
				var err error
				switch operation {
				case "acquire":
					lease, err = store.AcquireLease(ctx, LeaseRequest{
						Mode:      "draining",
						OwnerID:   uuid.New(),
						ExpiresAt: expiresAt,
					})
				case "renew":
					lease, err = store.RenewLease(ctx, current, expiresAt)
				case "transition":
					lease, err = store.TransitionLease(ctx, current, "backup", expiresAt)
				}
				result <- leaseResult{lease: lease, err: err}
			}()
			select {
			case <-committed:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s did not reach post-commit seam", operation)
			}
			waitForPostgresClock(t, pool, expiresAt.Add(20*time.Millisecond))
			close(continueAfterCommit)

			var lease Lease
			select {
			case got := <-result:
				if got.err != nil {
					t.Fatalf("%s after delayed commit: %v", operation, got.err)
				}
				lease = got.lease
			case <-time.After(2 * time.Second):
				t.Fatalf("%s did not return after post-commit seam released", operation)
			}
			tokenHash := sha256.Sum256(lease.Token)
			waitForLeaseSessionReleaseWithin(t, store, tokenHash, 250*time.Millisecond)
			if release, err := store.AcquireShared(ctx); !errors.Is(err, ErrLeaseHeld) {
				if err == nil {
					release()
				}
				t.Fatalf("expired durable row after %s did not fail closed: %v", operation, err)
			}

			takeoverStore := NewPostgresStore(pool)
			t.Cleanup(func() {
				closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancelClose()
				_ = takeoverStore.Close(closeCtx)
			})
			takeoverCtx, cancelTakeover := context.WithTimeout(ctx, 2*time.Second)
			defer cancelTakeover()
			takeover, err := takeoverStore.AcquireLease(takeoverCtx, LeaseRequest{
				Mode:      "release",
				OwnerID:   uuid.New(),
				ExpiresAt: postgresClock(t, pool).Add(time.Minute),
			})
			if err != nil {
				t.Fatalf("takeover after delayed expired %s: %v", operation, err)
			}
			if err := takeoverStore.ReleaseLease(ctx, takeover); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresStoreCloseClearsActiveLeaseAndReleasesDedicatedConnection(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	var _ LeaseSessionCloser = store
	baselineAcquired := pool.Stat().AcquiredConns()
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode:      "backup",
		OwnerID:   uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := pool.Stat().AcquiredConns(); got != baselineAcquired+1 {
		t.Fatalf("acquired connections with active lease=%d, want %d", got, baselineAcquired+1)
	}

	closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
	defer cancelClose()
	if err := store.Close(closeCtx); err != nil {
		t.Fatalf("close active store: %v", err)
	}
	if got := pool.Stat().AcquiredConns(); got != baselineAcquired {
		t.Fatalf("acquired connections after close=%d, want %d", got, baselineAcquired)
	}
	mode, err := store.GetMode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode != "normal" || mode.OwnerID != uuid.Nil || !mode.ExpiresAt.IsZero() {
		t.Fatalf("close left durable lease orphaned: %#v", mode)
	}

	probe, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Release()
	var acquired bool
	if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, operationsAdvisoryKey).
		Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("close retained the exclusive advisory lock")
	}
	if _, err := probe.Exec(ctx, `SELECT pg_advisory_unlock($1)`, operationsAdvisoryKey); err != nil {
		t.Fatal(err)
	}

	if _, err := store.AcquireLease(ctx, LeaseRequest{
		Mode:      "draining",
		OwnerID:   uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(time.Minute),
	}); !errors.Is(err, errStoreClosed) {
		t.Fatalf("closed store acquisition error=%v", err)
	}
	_ = lease
}

func TestPostgresStoreCloseCancelsPendingAdvisoryAcquisitionsBeforePoolClose(t *testing.T) {
	for _, operation := range []string{"exclusive", "shared"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			pool := migratedOperationsStore(t)
			storePool, err := pgxpool.New(ctx, pool.Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := pool.Acquire(ctx)
			if err != nil {
				storePool.Close()
				t.Fatal(err)
			}
			blockerSQL := `SELECT pg_advisory_lock($1)`
			waitingSQL := "SELECT pg_advisory_lock_shared"
			if operation == "exclusive" {
				blockerSQL = `SELECT pg_advisory_lock_shared($1)`
				waitingSQL = "SELECT pg_advisory_lock"
			}
			if _, err := blocker.Exec(ctx, blockerSQL, operationsAdvisoryKey); err != nil {
				blocker.Release()
				storePool.Close()
				t.Fatal(err)
			}
			unblock := func() {
				if operation == "shared" {
					releaseExclusiveConnection(blocker)
					return
				}
				releaseSharedConnection(blocker)
			}
			unblocked := false
			t.Cleanup(func() {
				if !unblocked {
					unblock()
				}
			})

			store := NewPostgresStore(storePool)
			operationCtx, cancelOperation := context.WithCancel(ctx)
			defer cancelOperation()
			result := make(chan error, 1)
			exclusiveExpiry := postgresClock(t, pool).Add(time.Hour)
			go func() {
				if operation == "exclusive" {
					_, err := store.AcquireLease(operationCtx, LeaseRequest{
						Mode:      "draining",
						OwnerID:   uuid.New(),
						ExpiresAt: exclusiveExpiry,
					})
					result <- err
					return
				}
				release, err := store.AcquireShared(operationCtx)
				if err == nil {
					release()
				}
				result <- err
			}()
			waitForBlockedPostgresQuery(t, pool, waitingSQL)

			closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
			defer cancelClose()
			if err := store.Close(closeCtx); err != nil {
				t.Fatalf("close with pending %s acquisition: %v", operation, err)
			}
			poolClosed := make(chan struct{})
			go func() {
				storePool.Close()
				close(poolClosed)
			}()
			select {
			case <-poolClosed:
			case <-time.After(time.Second):
				cancelOperation()
				t.Fatalf("pool close blocked on pending %s store connection", operation)
			}
			select {
			case err := <-result:
				if !errors.Is(err, errStoreClosed) {
					t.Fatalf("pending %s acquisition error=%v", operation, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("pending %s acquisition did not drain", operation)
			}
			unblock()
			unblocked = true
			mode, err := NewPostgresStore(pool).GetMode(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mode.Mode != "normal" || mode.OwnerID != uuid.Nil || !mode.ExpiresAt.IsZero() {
				t.Fatalf("pending %s close left durable orphan: %#v", operation, mode)
			}
		})
	}
}

func TestPostgresStoreCloseCancelsBlockedLeaseOperationsBeforePoolClose(t *testing.T) {
	for _, operation := range []string{"renew", "release"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			pool := migratedOperationsStore(t)
			storePool, err := pgxpool.New(ctx, pool.Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			store := NewPostgresStore(storePool)
			lease, err := store.AcquireLease(ctx, LeaseRequest{
				Mode:      "draining",
				OwnerID:   uuid.New(),
				ExpiresAt: postgresClock(t, pool).Add(time.Hour),
			})
			if err != nil {
				storePool.Close()
				t.Fatal(err)
			}

			blocker, err := pool.Begin(ctx)
			if err != nil {
				closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
				_ = store.Close(closeCtx)
				cancelClose()
				storePool.Close()
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			if _, err := blocker.Exec(ctx, `
SELECT singleton_id FROM operational_modes
WHERE singleton_id=true FOR UPDATE`); err != nil {
				t.Fatal(err)
			}

			result := make(chan error, 1)
			renewExpiry := postgresClock(t, pool).Add(2 * time.Hour)
			go func() {
				if operation == "renew" {
					_, err := store.RenewLease(
						ctx,
						lease,
						renewExpiry,
					)
					result <- err
					return
				}
				result <- store.ReleaseLease(ctx, lease)
			}()
			waitingSQL := "SELECT owner_id,lease_token_hash"
			if operation == "renew" {
				waitingSQL = "SELECT mode,owner_id,lease_token_hash"
			}
			waitForBlockedPostgresQuery(t, pool, waitingSQL)

			closeResult := make(chan error, 1)
			go func() {
				closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
				defer cancelClose()
				closeResult <- store.Close(closeCtx)
			}()
			select {
			case err := <-result:
				if !errors.Is(err, errStoreClosed) {
					t.Fatalf("blocked %s error=%v", operation, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("blocked %s did not drain after close", operation)
			}

			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-closeResult:
				if err != nil {
					t.Fatalf("close after blocked %s: %v", operation, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("close did not finish after blocked %s drained", operation)
			}

			poolClosed := make(chan struct{})
			go func() {
				storePool.Close()
				close(poolClosed)
			}()
			select {
			case <-poolClosed:
			case <-time.After(time.Second):
				t.Fatalf("pool close blocked after canceled %s", operation)
			}

			mode, err := NewPostgresStore(pool).GetMode(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mode.Mode != "normal" || mode.OwnerID != uuid.Nil || !mode.ExpiresAt.IsZero() {
				t.Fatalf("blocked %s close left durable orphan: %#v", operation, mode)
			}
			assertExclusiveAdvisoryAvailable(t, pool)
		})
	}
}

func TestPostgresStoreCloseHandlesFailedRecoveryAfterDedicatedBackendDies(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	var failFreshConnect atomic.Bool
	forcedRecoveryErr := errors.New("forced fresh authoritative recovery failure")
	storeConfig, err := pgxpool.ParseConfig(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	storeConfig.MaxConns = 1
	storeConfig.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		if failFreshConnect.Load() {
			return forcedRecoveryErr
		}
		return nil
	}
	storePool, err := pgxpool.NewWithConfig(ctx, storeConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(storePool.Close)
	store := NewPostgresStore(storePool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode:      "draining",
		OwnerID:   uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256(lease.Token)
	store.mu.Lock()
	session := store.sessions[tokenHash]
	store.mu.Unlock()
	if session == nil {
		t.Fatal("missing acquired lease session")
	}

	failFreshConnect.Store(true)
	terminateLeaseSessionBackend(t, pool, store, lease)
	closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
	defer cancelClose()
	if err := store.Close(closeCtx); !errors.Is(err, forcedRecoveryErr) {
		t.Fatalf("close recovery error=%v, want %v", err, forcedRecoveryErr)
	}

	store.mu.Lock()
	retained := len(store.sessions)
	store.mu.Unlock()
	if retained != 0 {
		t.Fatalf("close retained %d lease sessions", retained)
	}
	session.mu.Lock()
	released := session.released
	timer := session.timer
	conn := session.conn
	session.mu.Unlock()
	if !released || timer != nil || conn != nil {
		t.Fatalf("cleaned session released=%t timer=%v conn=%p", released, timer, conn)
	}

	poolClosed := make(chan struct{})
	go func() {
		storePool.Close()
		close(poolClosed)
	}()
	select {
	case <-poolClosed:
	case <-time.After(time.Second):
		t.Fatal("store pool close blocked after failed lease recovery")
	}
}

func TestPostgresStoreCloseDrainsReturnedSharedGateAndReleaseRemainsSafe(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	storePool, err := pgxpool.New(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(storePool)
	release, err := store.AcquireShared(ctx)
	if err != nil {
		storePool.Close()
		t.Fatal(err)
	}
	closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
	defer cancelClose()
	if err := store.Close(closeCtx); err != nil {
		release()
		storePool.Close()
		t.Fatal(err)
	}
	poolClosed := make(chan struct{})
	go func() {
		storePool.Close()
		close(poolClosed)
	}()
	select {
	case <-poolClosed:
	case <-time.After(time.Second):
		release()
		t.Fatal("pool close blocked on returned shared-gate connection")
	}
	release()

	probe, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Release()
	var acquired bool
	if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, operationsAdvisoryKey).
		Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("store close retained returned shared advisory lock")
	}
	if _, err := probe.Exec(ctx, `SELECT pg_advisory_unlock($1)`, operationsAdvisoryKey); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreCloseRacingBlockedAcquireLeavesNoCommittedOrphan(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
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

	type acquireResult struct {
		lease Lease
		err   error
	}
	result := make(chan acquireResult, 1)
	go func() {
		lease, err := store.AcquireLease(ctx, LeaseRequest{
			Mode:      "release",
			OwnerID:   uuid.New(),
			ExpiresAt: postgresClock(t, pool).Add(time.Hour),
		})
		result <- acquireResult{lease: lease, err: err}
	}()
	waitForBlockedPostgresQuery(t, pool, "SELECT mode,owner_id,lease_token_hash")

	closeCtx, cancelClose := context.WithTimeout(ctx, 2*time.Second)
	defer cancelClose()
	if err := store.Close(closeCtx); err != nil {
		t.Fatalf("close racing acquire: %v", err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if !errors.Is(got.err, errStoreClosed) {
			t.Fatalf("racing acquisition returned lease=%#v err=%v", got.lease, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("racing acquisition did not return after row blocker committed")
	}

	mode, err := store.GetMode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode != "normal" || mode.OwnerID != uuid.Nil || !mode.ExpiresAt.IsZero() {
		t.Fatalf("racing close left durable lease orphaned: %#v", mode)
	}
	probe, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Release()
	var acquired bool
	if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, operationsAdvisoryKey).
		Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("racing close left exclusive advisory lock held")
	}
	if _, err := probe.Exec(ctx, `SELECT pg_advisory_unlock($1)`, operationsAdvisoryKey); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseExpiryGenerationIgnoresStaleCallback(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(), ExpiresAt: postgresClock(t, pool).Add(time.Hour),
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
	staleGeneration := session.expiryGeneration
	store.scheduleLeaseExpiryLocked(tokenHash, session, time.Hour)
	currentGeneration := session.expiryGeneration
	session.mu.Unlock()
	if currentGeneration != staleGeneration+1 {
		t.Fatalf("expiry generation=%d, want %d", currentGeneration, staleGeneration+1)
	}
	store.expireLeaseSession(tokenHash, session, staleGeneration)
	store.mu.Lock()
	_, retained := store.sessions[tokenHash]
	store.mu.Unlock()
	if !retained {
		t.Fatal("stale expiry generation released the current lease")
	}

	store.expireLeaseSession(tokenHash, session, currentGeneration)
	store.mu.Lock()
	_, retained = store.sessions[tokenHash]
	store.mu.Unlock()
	if retained {
		t.Fatal("current expiry generation did not release its lease")
	}
}

func TestCanceledLeaseMutationReturnsCommittedLeaseAndReschedulesTimer(t *testing.T) {
	for name, mutate := range map[string]func(context.Context, *PostgresStore, Lease, time.Time) (Lease, error){
		"renew": func(ctx context.Context, store *PostgresStore, lease Lease, expiresAt time.Time) (Lease, error) {
			return store.RenewLease(ctx, lease, expiresAt)
		},
		"transition": func(ctx context.Context, store *PostgresStore, lease Lease, expiresAt time.Time) (Lease, error) {
			return store.TransitionLease(ctx, lease, "backup", expiresAt)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pool := migratedOperationsStore(t)
			store := NewPostgresStore(pool)
			lease, err := store.AcquireLease(ctx, LeaseRequest{
				Mode:      "draining",
				OwnerID:   uuid.New(),
				ExpiresAt: postgresClock(t, pool).Add(5 * time.Minute),
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
			startGeneration := session.expiryGeneration
			session.mu.Unlock()

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

			newExpiry := postgresClock(t, pool).Add(10 * time.Minute)
			mutationCtx, cancelMutation := context.WithCancel(ctx)
			type mutationResult struct {
				lease Lease
				err   error
			}
			result := make(chan mutationResult, 1)
			go func() {
				updated, err := mutate(mutationCtx, store, lease, newExpiry)
				result <- mutationResult{lease: updated, err: err}
			}()
			waitForBlockedPostgresQuery(t, pool, "SELECT mode,owner_id,lease_token_hash")
			cancelMutation()
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			var committed Lease
			select {
			case got := <-result:
				if got.err != nil {
					t.Fatalf("committed mutation returned error: %v", got.err)
				}
				committed = got.lease
			case <-time.After(2 * time.Second):
				t.Fatal("mutation did not return after row blocker committed")
			}
			wantMode := "draining"
			if name == "transition" {
				wantMode = "backup"
			}
			if committed.Mode != wantMode || committed.OwnerID != lease.OwnerID ||
				committed.Version != lease.Version+1 {
				t.Fatalf("committed lease=%#v", committed)
			}
			var durableMode string
			var durableExpiry time.Time
			if err := pool.QueryRow(ctx, `
SELECT mode,lease_expires_at
FROM operational_modes WHERE singleton_id=true`,
			).Scan(&durableMode, &durableExpiry); err != nil {
				t.Fatal(err)
			}
			if durableMode != wantMode ||
				durableExpiry.Sub(newExpiry) < -time.Millisecond ||
				durableExpiry.Sub(newExpiry) > time.Millisecond {
				t.Fatalf("durable mode=%q expiry=%v, want mode=%q expiry=%v",
					durableMode, durableExpiry, wantMode, newExpiry)
			}
			session.mu.Lock()
			rescheduled := !session.released &&
				session.timer != nil &&
				session.expiryGeneration == startGeneration+1
			session.mu.Unlock()
			if !rescheduled {
				t.Fatal("committed mutation did not reschedule its retained lease session")
			}
			if err := store.ReleaseLease(ctx, committed); err != nil {
				t.Fatal(err)
			}
		})
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

func TestPostgresOperationalGateReportsNormalMaintenanceAndReadErrors(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)

	allowed, err := store.ClaimsAllowed(ctx)
	if err != nil || !allowed {
		t.Fatalf("normal allowed=%t err=%v", allowed, err)
	}
	lease, err := store.AcquireLease(ctx, LeaseRequest{
		Mode: "draining", OwnerID: uuid.New(),
		ExpiresAt: postgresClock(t, pool).Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = store.ClaimsAllowed(ctx)
	if err != nil || allowed {
		t.Fatalf("maintenance allowed=%t err=%v", allowed, err)
	}
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}

	closedPool, err := pgxpool.New(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	closedPool.Close()
	closedStore := NewPostgresStore(closedPool)
	allowed, err = closedStore.ClaimsAllowed(ctx)
	if err == nil || allowed {
		t.Fatalf("closed pool allowed=%t err=%v", allowed, err)
	}
	if err := closedStore.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func postgresClock(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC()
}

func requireSharedAdmissionRejectedQuickly(t *testing.T, store *PostgresStore) {
	t.Helper()
	started := time.Now()
	release, err := store.AcquireShared(context.Background())
	if err == nil {
		release()
	}
	if !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("shared maintenance admission error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shared maintenance admission took %v", elapsed)
	}
}

func waitForPostgresClock(t *testing.T, pool *pgxpool.Pool, target time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !postgresClock(t, pool).Before(target) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("database clock did not reach %v: %v", target, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForLeaseSessionRelease(t *testing.T, store *PostgresStore, tokenHash [32]byte) {
	t.Helper()
	waitForLeaseSessionReleaseWithin(t, store, tokenHash, 2*time.Second)
}

func waitForLeaseSessionReleaseWithin(
	t *testing.T,
	store *PostgresStore,
	tokenHash [32]byte,
	timeout time.Duration,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		store.mu.Lock()
		_, retained := store.sessions[tokenHash]
		store.mu.Unlock()
		if !retained {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("lease session was not released: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForBlockedPostgresQuery(t *testing.T, pool *pgxpool.Pool, queryFragment string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
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
	  AND query LIKE '%' || $1 || '%'
)`, queryFragment).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("query containing %q did not block: %v", queryFragment, ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertRedactedSettingsRejectionAudit(
	t *testing.T,
	pool *pgxpool.Pool,
	principal Principal,
	reason string,
	wantCount int,
) {
	t.Helper()
	var count int
	var leaked bool
	if err := pool.QueryRow(context.Background(), `
SELECT count(*),
       bool_or(metadata <> jsonb_build_object(
           'category','high_risk',
           'reason',$4::text
       ))
FROM audit_logs
WHERE action='operations.settings_rejected'
  AND target_type='system_settings'
  AND target_id='global'
  AND actor_user_id=$1
  AND request_id=$2
  AND ip=$3`,
		principal.User.ID, principal.RequestID, principal.IP, reason,
	).Scan(&count, &leaked); err != nil {
		t.Fatal(err)
	}
	if count != wantCount || leaked {
		t.Fatalf("redacted rejection audits count=%d leaked=%t, want count=%d",
			count, leaked, wantCount)
	}
}

func terminatingCommitHook(
	pool *pgxpool.Pool,
	targetOperation string,
	terminatedPID *int,
) func(string, *pgxpool.Conn) error {
	return func(operation string, conn *pgxpool.Conn) error {
		if operation != targetOperation {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(terminatedPID); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, *terminatedPID); err != nil {
			return err
		}
		return errors.New("ambiguous post-commit connection loss")
	}
}

func terminateLeaseSessionBackend(
	t *testing.T,
	pool *pgxpool.Pool,
	store *PostgresStore,
	lease Lease,
) int {
	t.Helper()
	tokenHash := sha256.Sum256(lease.Token)
	store.mu.Lock()
	session := store.sessions[tokenHash]
	store.mu.Unlock()
	if session == nil {
		t.Fatal("acquired lease was not registered")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	var pid int
	if err := session.conn.QueryRow(context.Background(), `SELECT pg_backend_pid()`).
		Scan(&pid); err != nil {
		t.Fatalf("query dedicated backend pid: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatalf("terminate dedicated backend pid=%d: %v", pid, err)
	}
	return pid
}

func assertRecoveredLeaseSession(
	t *testing.T,
	store *PostgresStore,
	lease Lease,
	terminatedPID int,
) {
	t.Helper()
	if terminatedPID == 0 {
		t.Fatal("commit hook did not terminate the committed connection")
	}
	tokenHash := sha256.Sum256(lease.Token)
	store.mu.Lock()
	session := store.sessions[tokenHash]
	store.mu.Unlock()
	if session == nil {
		t.Fatal("committed lease was not registered")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.released || session.timer == nil {
		t.Fatal("recovered lease session is not active and timed")
	}
	var recoveredPID int
	if err := session.conn.QueryRow(context.Background(), `SELECT pg_backend_pid()`).
		Scan(&recoveredPID); err != nil {
		t.Fatalf("query recovered lease session: %v", err)
	}
	if recoveredPID == terminatedPID {
		t.Fatalf("lease retained terminated backend pid=%d", terminatedPID)
	}
}

func assertExclusiveAdvisoryAvailable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, operationsAdvisoryKey).
		Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("exclusive advisory lock remained held")
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, operationsAdvisoryKey); err != nil {
		t.Fatal(err)
	}
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
