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
	dashboard   DashboardReader
	alerts      AlertStore
	webhook     WebhookTestSender
}

func NewServiceWithDashboardAlertsAndWebhook(
	store ServiceStore,
	auditReader audit.FilteredReader,
	dashboard DashboardReader,
	alerts AlertStore,
	webhook WebhookTestSender,
) (HTTPService, error) {
	if webhook == nil {
		return nil, ErrInvalid
	}
	base, err := NewServiceWithDashboardAndAlerts(
		store,
		auditReader,
		dashboard,
		alerts,
	)
	if err != nil {
		return nil, err
	}
	concrete, ok := base.(*service)
	if !ok {
		return nil, ErrInvalid
	}
	concrete.webhook = webhook
	return concrete, nil
}

func NewServiceWithDashboardAndAlerts(
	store ServiceStore,
	auditReader audit.FilteredReader,
	dashboard DashboardReader,
	alerts AlertStore,
) (HTTPService, error) {
	if alerts == nil {
		return nil, ErrInvalid
	}
	base, err := NewServiceWithDashboard(store, auditReader, dashboard)
	if err != nil {
		return nil, err
	}
	concrete, ok := base.(*service)
	if !ok {
		return nil, ErrInvalid
	}
	concrete.alerts = alerts
	return concrete, nil
}

func NewService(store ServiceStore, readers ...audit.FilteredReader) HTTPService {
	var reader audit.FilteredReader
	if len(readers) > 0 {
		reader = readers[0]
	}
	return &service{store: store, auditReader: reader}
}

func NewServiceWithDashboard(
	store ServiceStore,
	auditReader audit.FilteredReader,
	dashboard DashboardReader,
) (HTTPService, error) {
	if store == nil || auditReader == nil || dashboard == nil {
		return nil, ErrInvalid
	}
	return &service{
		store:       store,
		auditReader: auditReader,
		dashboard:   dashboard,
	}, nil
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

func (s *service) GetDashboard(
	ctx context.Context,
	principal Principal,
) (Dashboard, error) {
	if err := authorizeSettings(principal); err != nil {
		return Dashboard{}, err
	}
	if s.dashboard == nil {
		return Dashboard{}, errDashboardDependencyUnavailable
	}
	return s.dashboard.Assemble(ctx)
}

func (s *service) ListAlerts(
	ctx context.Context,
	principal Principal,
	filter AlertFilter,
) (AlertPage, error) {
	if err := authorizeSettings(principal); err != nil {
		return AlertPage{}, err
	}
	if s.alerts == nil {
		return AlertPage{}, errors.New("operations alert store unavailable")
	}
	if err := validateAlertFilter(filter); err != nil {
		return AlertPage{}, err
	}
	return s.alerts.ListAlerts(ctx, filter)
}

func (s *service) AcknowledgeAlert(
	ctx context.Context,
	principal Principal,
	id uuid.UUID,
) (Alert, error) {
	if err := authorizeSettings(principal); err != nil {
		return Alert{}, err
	}
	if s.alerts == nil {
		return Alert{}, errors.New("operations alert store unavailable")
	}
	if id == uuid.Nil {
		return Alert{}, ErrInvalid
	}
	return s.alerts.AcknowledgeAlert(ctx, principal, id)
}

func (s *service) TestWebhook(
	ctx context.Context,
	principal Principal,
) error {
	if err := authorizeSettings(principal); err != nil {
		return err
	}
	if s.webhook == nil || !s.webhook.Enabled() {
		return ErrWebhookNotConfigured
	}
	result := s.webhook.Send(ctx, syntheticWebhookTestPayload())
	if !validWebhookDeliveryResult(result) || !result.Succeeded {
		return ErrWebhookDeliveryFailed
	}
	return nil
}

func syntheticWebhookTestPayload() WebhookPayload {
	observedAt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	return WebhookPayload{
		SchemaVersion:   WebhookSchemaVersion,
		AlertID:         "00000000-0000-4000-8000-000000000001",
		Category:        "processing",
		Severity:        AlertSeverityWarning,
		State:           AlertStateOpen,
		Summary:         "Webhook connectivity test",
		FirstObservedAt: observedAt,
		LastObservedAt:  observedAt,
		CurrentValue:    0,
		Threshold:       0,
		DashboardPath:   WebhookDashboardPath,
	}
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
