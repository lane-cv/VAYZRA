package operations

import (
	"math"
	"time"
)

const (
	MaxRecentAudit       = 10
	maxDashboardInteger  = int64(9_007_199_254_740_991)
	maxDashboardDuration = int64((24 * time.Hour) / time.Millisecond)
)

type DataState string

const (
	DataStateHealthy     DataState = "healthy"
	DataStateDegraded    DataState = "degraded"
	DataStateUnavailable DataState = "unavailable"
	DataStateStale       DataState = "stale"
	DataStateTimeout     DataState = "timeout"
	DataStateEmpty       DataState = "empty"
)

type DashboardService string

const (
	ServiceApp         DashboardService = "app"
	ServiceCaddy       DashboardService = "caddy"
	ServicePostgres    DashboardService = "postgres"
	ServiceRedis       DashboardService = "redis"
	ServiceObjectStore DashboardService = "object_store"
	ServiceWorker      DashboardService = "worker"
)

type DashboardQueue string

const (
	QueueProcessing DashboardQueue = "processing"
	QueueAI         DashboardQueue = "ai"
	QueueOutbox     DashboardQueue = "outbox"
)

type RecoveryState string

const (
	RecoveryStateSucceeded RecoveryState = "succeeded"
	RecoveryStateDegraded  RecoveryState = "degraded"
	RecoveryStateFailed    RecoveryState = "failed"
	RecoveryStateEmpty     RecoveryState = "empty"
)

type AuditCategory string

const (
	AuditCategoryAuthentication AuditCategory = "authentication"
	AuditCategoryAuthorization  AuditCategory = "authorization"
	AuditCategoryFiles          AuditCategory = "files"
	AuditCategoryTeaching       AuditCategory = "teaching"
	AuditCategoryAI             AuditCategory = "ai"
	AuditCategoryOperations     AuditCategory = "operations"
	AuditCategoryBackup         AuditCategory = "backup"
)

type AuditOutcome string

const (
	AuditOutcomeSucceeded AuditOutcome = "succeeded"
	AuditOutcomeFailed    AuditOutcome = "failed"
	AuditOutcomeDenied    AuditOutcome = "denied"
	AuditOutcomeRejected  AuditOutcome = "rejected"
)

type Dashboard struct {
	ObservedAt       time.Time       `json:"observedAt"`
	ReleaseVersion   string          `json:"releaseVersion"`
	Students         StudentSummary  `json:"students"`
	Questions        QuestionSummary `json:"questions"`
	AI               AISummary       `json:"ai"`
	Storage          StorageSummary  `json:"storage"`
	Services         []ServiceHealth `json:"services"`
	Queues           []QueueSummary  `json:"queues"`
	Backup           BackupSummary   `json:"backup"`
	Alerts           AlertSummary    `json:"alerts"`
	RecentAuditState DataState       `json:"recentAuditState"`
	RecentAudit      []AuditSummary  `json:"recentAudit"`
}

type StudentSummary struct {
	State      DataState  `json:"state"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	Active     int64      `json:"active"`
	Disabled   int64      `json:"disabled"`
}

type QuestionSummary struct {
	State             DataState  `json:"state"`
	ObservedAt        *time.Time `json:"observedAt,omitempty"`
	Waiting           int64      `json:"waiting"`
	OldestWaitSeconds int64      `json:"oldestWaitSeconds"`
}

type AISummary struct {
	State                        DataState  `json:"state"`
	ObservedAt                   *time.Time `json:"observedAt,omitempty"`
	Requests                     int64      `json:"requests"`
	SuccessRatePercent           float64    `json:"successRatePercent"`
	FirstByteLatencyMilliseconds int64      `json:"firstByteLatencyMilliseconds"`
	TotalLatencyMilliseconds     int64      `json:"totalLatencyMilliseconds"`
	DailyCostMicroUSD            int64      `json:"dailyCostMicroUSD"`
}

type StorageSummary struct {
	State          DataState  `json:"state"`
	ObservedAt     *time.Time `json:"observedAt,omitempty"`
	UsedBytes      int64      `json:"usedBytes"`
	CapacityBytes  int64      `json:"capacityBytes"`
	WarningPercent int        `json:"warningPercent"`
}

type ServiceHealth struct {
	Service             DashboardService `json:"service"`
	State               DataState        `json:"state"`
	ObservedAt          *time.Time       `json:"observedAt,omitempty"`
	LatencyMilliseconds int64            `json:"latencyMilliseconds"`
}

type QueueSummary struct {
	Queue      DashboardQueue `json:"queue"`
	State      DataState      `json:"state"`
	ObservedAt *time.Time     `json:"observedAt,omitempty"`
	Queued     int64          `json:"queued"`
	Streaming  int64          `json:"streaming"`
	Failed     int64          `json:"failed"`
	Expired    int64          `json:"expired"`
}

type BackupPointSummary struct {
	State       RecoveryState `json:"state"`
	CompletedAt *time.Time    `json:"completedAt,omitempty"`
}

type RestorePointSummary struct {
	State       RecoveryState `json:"state"`
	CompletedAt *time.Time    `json:"completedAt,omitempty"`
	RTOSeconds  int64         `json:"rtoSeconds"`
}

type BackupSummary struct {
	State      DataState           `json:"state"`
	ObservedAt *time.Time          `json:"observedAt,omitempty"`
	Local      BackupPointSummary  `json:"local"`
	Remote     BackupPointSummary  `json:"remote"`
	Restore    RestorePointSummary `json:"restore"`
}

type AlertSummary struct {
	State        DataState  `json:"state"`
	ObservedAt   *time.Time `json:"observedAt,omitempty"`
	OpenWarning  int64      `json:"openWarning"`
	OpenCritical int64      `json:"openCritical"`
}

type AuditSummary struct {
	Category   AuditCategory `json:"category"`
	Outcome    AuditOutcome  `json:"outcome"`
	OccurredAt time.Time     `json:"occurredAt"`
}

var dashboardServiceOrder = [...]DashboardService{
	ServiceApp,
	ServiceCaddy,
	ServicePostgres,
	ServiceRedis,
	ServiceObjectStore,
	ServiceWorker,
}

var dashboardQueueOrder = [...]DashboardQueue{
	QueueProcessing,
	QueueAI,
	QueueOutbox,
}

func DashboardServiceOrder() []DashboardService {
	out := make([]DashboardService, len(dashboardServiceOrder))
	copy(out, dashboardServiceOrder[:])
	return out
}

func DashboardQueueOrder() []DashboardQueue {
	out := make([]DashboardQueue, len(dashboardQueueOrder))
	copy(out, dashboardQueueOrder[:])
	return out
}

func validDataState(state DataState) bool {
	switch state {
	case DataStateHealthy, DataStateDegraded, DataStateUnavailable,
		DataStateStale, DataStateTimeout, DataStateEmpty:
		return true
	default:
		return false
	}
}

func validDashboardService(service DashboardService) bool {
	switch service {
	case ServiceApp, ServiceCaddy, ServicePostgres, ServiceRedis, ServiceObjectStore, ServiceWorker:
		return true
	default:
		return false
	}
}

func validDashboardQueue(queue DashboardQueue) bool {
	switch queue {
	case QueueProcessing, QueueAI, QueueOutbox:
		return true
	default:
		return false
	}
}

func validRecoveryState(state RecoveryState) bool {
	switch state {
	case RecoveryStateSucceeded, RecoveryStateDegraded, RecoveryStateFailed, RecoveryStateEmpty:
		return true
	default:
		return false
	}
}

func validAuditCategory(category AuditCategory) bool {
	switch category {
	case AuditCategoryAuthentication, AuditCategoryAuthorization,
		AuditCategoryFiles, AuditCategoryTeaching, AuditCategoryAI,
		AuditCategoryOperations, AuditCategoryBackup:
		return true
	default:
		return false
	}
}

func validAuditOutcome(outcome AuditOutcome) bool {
	switch outcome {
	case AuditOutcomeSucceeded, AuditOutcomeFailed, AuditOutcomeDenied, AuditOutcomeRejected:
		return true
	default:
		return false
	}
}

func validDashboardCount(value int64) bool {
	return value >= 0 && value <= maxDashboardInteger
}

func validDashboardDurationMilliseconds(value int64) bool {
	return value >= 0 && value <= maxDashboardDuration
}

func validDashboardSeconds(value int64) bool {
	return value >= 0 && value <= int64((365*24*time.Hour)/time.Second)
}

func validDashboardRate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) &&
		value >= 0 && value <= 100 &&
		(value != 0 || !math.Signbit(value))
}

func validDashboardTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999
}
