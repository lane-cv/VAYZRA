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
	Version                                         int64
	SiteName, SiteAnnouncement                      string
	SoftDeleteRetentionDays, AuditRetentionDays     int
	OperationalSampleRetentionDays                  int
	BackupHour, BackupMinute                        int
	BackupTimezone                                  string
	DiskWarningPercent, DiskCriticalPercent         int
	AIErrorWarningPercent, AIErrorCriticalPercent   int
	ProcessingQueueWarning, ProcessingQueueCritical int
	UpdatedBy                                       uuid.UUID
	UpdatedAt                                       time.Time
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
		settings.DiskWarningPercent < 1 || settings.DiskWarningPercent > 99 ||
		settings.DiskCriticalPercent <= settings.DiskWarningPercent || settings.DiskCriticalPercent > 100 ||
		settings.AIErrorWarningPercent < 1 || settings.AIErrorWarningPercent > 99 ||
		settings.AIErrorCriticalPercent <= settings.AIErrorWarningPercent || settings.AIErrorCriticalPercent > 100 ||
		settings.ProcessingQueueWarning < 1 ||
		settings.ProcessingQueueCritical <= settings.ProcessingQueueWarning {
		return ErrInvalid
	}
	return nil
}
