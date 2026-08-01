package operations

import (
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrForbidden  = errors.New("forbidden")
	ErrInvalid    = errors.New("invalid operations input")
	ErrConflict   = errors.New("operations conflict")
	ErrLeaseHeld  = errors.New("operational lease held")
	ErrStaleLease = errors.New("stale operational lease")
)

type Settings struct {
	Version                          int64
	SiteName                         string
	SiteAnnouncement                 string
	SoftDeleteRetentionDays          int
	AuditRetentionDays               int
	OperationalSampleRetentionDays   int
	BackupHour                       int
	BackupMinute                     int
	BackupTimezone                   string
	DiskWarningPercent               int
	DiskCriticalPercent              int
	BackupFilesystemWarningPercent   int
	BackupFilesystemCriticalPercent  int
	LocalBackupAgeWarningHours       int
	LocalBackupAgeCriticalHours      int
	AIErrorWarningPercent            int
	AIErrorCriticalPercent           int
	ProcessingQueueWarning           int
	ProcessingQueueCritical          int
	ProcessingFailureWarningCount    int
	ProcessingFailureCriticalCount   int
	LoginFailureWarningCount         int
	LoginFailureCriticalCount        int
	AuthorizationDenialWarningCount  int
	AuthorizationDenialCriticalCount int
	Infrastructure                   []InfrastructureStatus
	UpdatedBy                        uuid.UUID
	UpdatedAt                        time.Time
}

type InfrastructureKey uint8

const (
	InfrastructureApplicationDatabase InfrastructureKey = iota + 1
	InfrastructureRedisSecurity
	InfrastructureObjectStore
	InfrastructureAIEncryption
	InfrastructureInternalMetrics
	InfrastructureHostMetricsIngestion
	InfrastructureAlertWebhook
	InfrastructureLocalBackup
	InfrastructureRemoteBackup
)

var infrastructureKeyOrder = [...]InfrastructureKey{
	InfrastructureApplicationDatabase,
	InfrastructureRedisSecurity,
	InfrastructureObjectStore,
	InfrastructureAIEncryption,
	InfrastructureInternalMetrics,
	InfrastructureHostMetricsIngestion,
	InfrastructureAlertWebhook,
	InfrastructureLocalBackup,
	InfrastructureRemoteBackup,
}

type InfrastructureStatus struct {
	Key             InfrastructureKey
	Configured      bool
	LastValidatedAt *time.Time
}

func NormalizeInfrastructureStatuses(input []InfrastructureStatus) []InfrastructureStatus {
	byKey := make(map[InfrastructureKey]InfrastructureStatus, len(input))
	for _, status := range input {
		if infrastructureStorageKey(status.Key) == "" {
			continue
		}
		if status.LastValidatedAt != nil && !status.LastValidatedAt.IsZero() {
			validatedAt := status.LastValidatedAt.UTC()
			status.LastValidatedAt = &validatedAt
		} else {
			status.LastValidatedAt = nil
		}
		byKey[status.Key] = status
	}
	statuses := make([]InfrastructureStatus, 0, len(infrastructureKeyOrder))
	for _, key := range infrastructureKeyOrder {
		status, ok := byKey[key]
		if !ok {
			status = InfrastructureStatus{Key: key}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func infrastructureStorageKey(key InfrastructureKey) string {
	switch key {
	case InfrastructureApplicationDatabase:
		return "application_database"
	case InfrastructureRedisSecurity:
		return "redis_security"
	case InfrastructureObjectStore:
		return "object_store"
	case InfrastructureAIEncryption:
		return "ai_encryption"
	case InfrastructureInternalMetrics:
		return "internal_metrics"
	case InfrastructureHostMetricsIngestion:
		return "host_metrics_ingestion"
	case InfrastructureAlertWebhook:
		return "alert_webhook"
	case InfrastructureLocalBackup:
		return "local_backup"
	case InfrastructureRemoteBackup:
		return "remote_backup"
	default:
		return ""
	}
}

func infrastructureKeyFromStorage(value string) (InfrastructureKey, bool) {
	for _, key := range infrastructureKeyOrder {
		if infrastructureStorageKey(key) == value {
			return key, true
		}
	}
	return 0, false
}

type Lease struct {
	Mode      string
	OwnerID   uuid.UUID
	Token     []byte
	ExpiresAt time.Time
	Version   int64
}

func ValidateSettings(settings Settings) error {
	siteNameLength := utf8.RuneCountInString(settings.SiteName)
	announcementLength := utf8.RuneCountInString(settings.SiteAnnouncement)
	if settings.Version < 1 ||
		!utf8.ValidString(settings.SiteName) || siteNameLength < 1 || siteNameLength > 80 ||
		!utf8.ValidString(settings.SiteAnnouncement) || announcementLength > 1000 ||
		settings.SoftDeleteRetentionDays < 30 || settings.SoftDeleteRetentionDays > 365 ||
		settings.AuditRetentionDays < 365 || settings.AuditRetentionDays > 2555 ||
		settings.OperationalSampleRetentionDays < 1 || settings.OperationalSampleRetentionDays > 30 ||
		settings.BackupHour < 0 || settings.BackupHour > 23 ||
		settings.BackupMinute < 0 || settings.BackupMinute > 59 ||
		settings.BackupTimezone != "Asia/Shanghai" ||
		!validPercentThresholdPair(settings.DiskWarningPercent, settings.DiskCriticalPercent) ||
		!validPercentThresholdPair(settings.BackupFilesystemWarningPercent, settings.BackupFilesystemCriticalPercent) ||
		!validCountThresholdPair(settings.LocalBackupAgeWarningHours, settings.LocalBackupAgeCriticalHours) ||
		!validPercentThresholdPair(settings.AIErrorWarningPercent, settings.AIErrorCriticalPercent) ||
		!validCountThresholdPair(settings.ProcessingQueueWarning, settings.ProcessingQueueCritical) ||
		!validCountThresholdPair(settings.ProcessingFailureWarningCount, settings.ProcessingFailureCriticalCount) ||
		!validCountThresholdPair(settings.LoginFailureWarningCount, settings.LoginFailureCriticalCount) ||
		!validCountThresholdPair(settings.AuthorizationDenialWarningCount, settings.AuthorizationDenialCriticalCount) {
		return ErrInvalid
	}
	return nil
}

func validPercentThresholdPair(warning, critical int) bool {
	return warning >= 1 && warning <= 99 &&
		critical > warning && critical <= 100
}

func validCountThresholdPair(warning, critical int) bool {
	return warning >= 1 && warning <= 2_147_483_646 &&
		critical > warning && critical <= 2_147_483_647
}
