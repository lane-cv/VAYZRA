package operations

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDashboardDTOContainsOnlySafeAggregateFields(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
	dashboard := Dashboard{
		ObservedAt: observedAt,
		Students: StudentSummary{
			State: DataStateHealthy, ObservedAt: timePointer(observedAt),
			Active: 3, Disabled: 1,
		},
		Questions: QuestionSummary{
			State: DataStateHealthy, ObservedAt: timePointer(observedAt),
			Waiting: 2, OldestWaitSeconds: 15,
		},
		AI: AISummary{
			State: DataStateHealthy, ObservedAt: timePointer(observedAt),
			Requests: 4, SuccessRatePercent: 75,
			FirstByteLatencyMilliseconds: 120, TotalLatencyMilliseconds: 840,
			DailyCostMicroUSD: 123,
		},
		Storage: StorageSummary{
			State: DataStateHealthy, ObservedAt: timePointer(observedAt),
			UsedBytes: 1024, CapacityBytes: 4096, WarningPercent: 75,
		},
		Services: []ServiceHealth{{
			Service: ServiceApp, State: DataStateHealthy,
			ObservedAt: timePointer(observedAt), LatencyMilliseconds: 5,
		}},
		Queues: []QueueSummary{{
			Queue: QueueAI, State: DataStateDegraded,
			ObservedAt: timePointer(observedAt), Queued: 2, Streaming: 1,
		}},
		Backup: BackupSummary{
			State: DataStateHealthy, ObservedAt: timePointer(observedAt),
			Local: BackupPointSummary{
				State: RecoveryStateSucceeded, CompletedAt: timePointer(observedAt),
			},
			Remote: BackupPointSummary{State: RecoveryStateEmpty},
			Restore: RestorePointSummary{
				State: RecoveryStateSucceeded, CompletedAt: timePointer(observedAt),
				RTOSeconds: 30,
			},
		},
		Alerts: AlertSummary{
			State: DataStateHealthy, ObservedAt: timePointer(observedAt),
			OpenWarning: 2, OpenCritical: 1,
		},
		RecentAuditState: DataStateHealthy,
		RecentAudit: []AuditSummary{{
			Category:   AuditCategoryOperations,
			Outcome:    AuditOutcomeSucceeded,
			OccurredAt: observedAt,
		}},
	}

	encoded, err := json.Marshal(dashboard)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, required := range []string{
		`"observedAt"`, `"students"`, `"active":3`, `"disabled":1`,
		`"questions"`, `"waiting":2`, `"oldestWaitSeconds":15`,
		`"successRatePercent":75`, `"dailyCostMicroUSD":123`,
		`"usedBytes":1024`, `"capacityBytes":4096`,
		`"service":"app"`, `"queue":"ai"`, `"rtoSeconds":30`,
		`"openWarning":2`, `"openCritical":1`,
		`"category":"operations"`, `"outcome":"succeeded"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("missing %s in %s", required, body)
		}
	}
	for _, forbidden := range []string{
		"studentId", "userId", "name", "ip", "path", "objectKey", "prompt",
		"url", "trace", "requestId", "backupRunId", "alertId",
	} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("forbidden field %q in %s", forbidden, body)
		}
	}
}

func TestDashboardSafeEnumsAreClosed(t *testing.T) {
	for _, state := range []DataState{
		DataStateHealthy,
		DataStateDegraded,
		DataStateUnavailable,
		DataStateStale,
		DataStateTimeout,
		DataStateEmpty,
	} {
		if !validDataState(state) {
			t.Fatalf("state %q rejected", state)
		}
	}
	if validDataState("healthy/student-secret") {
		t.Fatal("arbitrary state accepted")
	}

	wantServices := []DashboardService{
		ServiceApp,
		ServiceCaddy,
		ServicePostgres,
		ServiceRedis,
		ServiceObjectStore,
		ServiceWorker,
	}
	services := DashboardServiceOrder()
	if len(services) != len(wantServices) {
		t.Fatalf("services=%#v want=%#v", services, wantServices)
	}
	for i, service := range services {
		if service != wantServices[i] {
			t.Fatalf("services[%d]=%q want=%q", i, service, wantServices[i])
		}
		if !validDashboardService(service) {
			t.Fatalf("service %q rejected", service)
		}
	}
	if validDashboardService("postgres/tenant") {
		t.Fatal("arbitrary service accepted")
	}

	for _, queue := range DashboardQueueOrder() {
		if !validDashboardQueue(queue) {
			t.Fatalf("queue %q rejected", queue)
		}
	}
	if validDashboardQueue("ai student-secret") {
		t.Fatal("arbitrary queue accepted")
	}

	if !validAuditCategory(AuditCategoryBackup) ||
		!validAuditOutcome(AuditOutcomeDenied) ||
		validAuditCategory("backup/tenant") ||
		validAuditOutcome("failed student-secret") {
		t.Fatal("audit enum allowlist is not closed")
	}
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
