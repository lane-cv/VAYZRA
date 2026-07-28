package operations

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

type service struct {
	store       ServiceStore
	auditReader audit.FilteredReader
}

func NewService(store ServiceStore, readers ...audit.FilteredReader) HTTPService {
	var reader audit.FilteredReader
	if len(readers) > 0 {
		reader = readers[0]
	}
	return &service{store: store, auditReader: reader}
}

func (s *service) GetSettings(ctx context.Context, principal Principal) (Settings, error) {
	if err := authorizeSettings(principal); err != nil {
		return Settings{}, err
	}
	return s.store.GetSettings(ctx)
}

func (s *service) UpdateSettings(ctx context.Context, principal Principal, settings Settings) (Settings, error) {
	if err := authorizeSettings(principal); err != nil {
		return Settings{}, err
	}
	if err := ValidateSettings(settings); err != nil {
		if reason := highRiskSettingsReason(settings); reason != "" {
			auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancelAudit()
			if auditErr := s.store.AuditSettingsRejection(auditCtx, principal, reason); auditErr != nil {
				return Settings{}, auditErr
			}
		}
		return Settings{}, err
	}
	return s.store.UpdateSettings(ctx, principal, settings)
}

func (s *service) ListAudit(ctx context.Context, principal Principal, filter audit.AuditFilter) (audit.AuditPage, error) {
	if err := authorizeSettings(principal); err != nil {
		return audit.AuditPage{}, err
	}
	if s.auditReader == nil {
		return audit.AuditPage{}, errors.New("operations audit reader unavailable")
	}
	return s.auditReader.ListFiltered(ctx, filter)
}

func highRiskSettingsReason(settings Settings) string {
	if settings.SoftDeleteRetentionDays < 30 || settings.SoftDeleteRetentionDays > 365 ||
		settings.AuditRetentionDays < 365 || settings.AuditRetentionDays > 2555 ||
		settings.OperationalSampleRetentionDays < 1 || settings.OperationalSampleRetentionDays > 30 {
		return "retention"
	}
	if settings.BackupHour < 0 || settings.BackupHour > 23 ||
		settings.BackupMinute < 0 || settings.BackupMinute > 59 ||
		settings.BackupTimezone != "Asia/Shanghai" {
		return "backup_schedule"
	}
	if settings.DiskWarningPercent < 1 || settings.DiskWarningPercent > 99 ||
		settings.DiskCriticalPercent <= settings.DiskWarningPercent || settings.DiskCriticalPercent > 100 ||
		settings.AIErrorWarningPercent < 1 || settings.AIErrorWarningPercent > 99 ||
		settings.AIErrorCriticalPercent <= settings.AIErrorWarningPercent || settings.AIErrorCriticalPercent > 100 ||
		settings.ProcessingQueueWarning < 1 ||
		settings.ProcessingQueueCritical <= settings.ProcessingQueueWarning {
		return "threshold"
	}
	return ""
}

func authorizeSettings(principal Principal) error {
	if principal.User.ID == uuid.Nil ||
		principal.User.Role != auth.RoleAdmin ||
		principal.User.Status != auth.StatusActive ||
		strings.TrimSpace(principal.RequestID) == "" ||
		len(principal.RequestID) > 64 ||
		principal.IP == nil || principal.IP.To16() == nil {
		return ErrForbidden
	}
	return nil
}
