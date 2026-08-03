package operations

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

const DashboardDependencyTimeout = 2 * time.Second

type StudentDashboardReader interface {
	ReadStudentSummary(context.Context, time.Time) (StudentSummary, error)
}

type QuestionDashboardReader interface {
	ReadQuestionSummary(context.Context, time.Time) (QuestionSummary, error)
}

type AIDashboardReader interface {
	ReadAISummary(context.Context, time.Time) (AISummary, error)
}

type StorageDashboardReader interface {
	ReadStorageSummary(context.Context, time.Time) (StorageSummary, error)
}

type ServiceDashboardReader interface {
	ReadServiceHealth(context.Context, time.Time) ([]ServiceHealth, error)
}

type QueueDashboardReader interface {
	ReadQueueSummaries(context.Context, time.Time) ([]QueueSummary, error)
}

type BackupDashboardReader interface {
	ReadBackupSummary(context.Context, time.Time) (BackupSummary, error)
}

type AlertDashboardReader interface {
	ReadAlertSummary(context.Context, time.Time) (AlertSummary, error)
}

type AuditDashboardReader interface {
	ReadRecentAudit(context.Context, time.Time, int) ([]AuditSummary, error)
}

type DashboardDependencies struct {
	ReleaseVersion string
	Students       StudentDashboardReader
	Questions      QuestionDashboardReader
	AI             AIDashboardReader
	Storage        StorageDashboardReader
	Services       ServiceDashboardReader
	Queues         QueueDashboardReader
	Backup         BackupDashboardReader
	Alerts         AlertDashboardReader
	Audit          AuditDashboardReader
}

type DashboardAssembler struct {
	clock             func() time.Time
	freshFor          time.Duration
	dependencyTimeout time.Duration
	dependencies      DashboardDependencies
	dependencySlots   [9]chan struct{}
	flightMu          sync.Mutex
	flight            *dashboardFlight
}

type dashboardFlight struct {
	done      chan struct{}
	dashboard Dashboard
	err       error
}

func NewDashboardAssembler(
	clock func() time.Time,
	freshFor time.Duration,
	dependencies DashboardDependencies,
) (*DashboardAssembler, error) {
	if clock == nil || freshFor <= 0 || freshFor > MaxSampleFreshFor {
		return nil, ErrInvalid
	}
	assembler := &DashboardAssembler{
		clock:             clock,
		freshFor:          freshFor,
		dependencyTimeout: DashboardDependencyTimeout,
		dependencies:      dependencies,
	}
	for index := range assembler.dependencySlots {
		assembler.dependencySlots[index] = make(chan struct{}, 1)
	}
	return assembler, nil
}

func (a *DashboardAssembler) Assemble(ctx context.Context) (Dashboard, error) {
	now, ok := a.now()
	if !ok {
		return failClosedDashboard(time.Time{}), ErrInvalid
	}
	if ctx == nil {
		return failClosedDashboard(now), ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return failClosedDashboard(now), err
	}
	flight := a.dashboardFlight(now)
	select {
	case <-ctx.Done():
		return failClosedDashboard(now), ctx.Err()
	case <-flight.done:
		return cloneDashboard(flight.dashboard), flight.err
	}
}

func (a *DashboardAssembler) dashboardFlight(now time.Time) *dashboardFlight {
	a.flightMu.Lock()
	defer a.flightMu.Unlock()
	if a.flight != nil {
		return a.flight
	}
	flight := &dashboardFlight{done: make(chan struct{})}
	a.flight = flight
	go a.runDashboardFlight(flight, now)
	return flight
}

func (a *DashboardAssembler) runDashboardFlight(flight *dashboardFlight, now time.Time) {
	dashboard := failClosedDashboard(now)
	err := errDashboardDependencyUnavailable
	func() {
		defer func() {
			if recover() != nil {
				dashboard = failClosedDashboard(now)
				err = errDashboardDependencyUnavailable
			}
		}()
		dashboard, err = a.assembleOnce(context.Background(), now)
	}()

	a.flightMu.Lock()
	flight.dashboard = dashboard
	flight.err = err
	close(flight.done)
	if a.flight == flight {
		a.flight = nil
	}
	a.flightMu.Unlock()
}

func (a *DashboardAssembler) assembleOnce(ctx context.Context, now time.Time) (Dashboard, error) {
	students := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[0], func(callCtx context.Context) (StudentSummary, error) {
		if a.dependencies.Students == nil {
			return StudentSummary{}, errDashboardDependencyUnavailable
		}
		return a.dependencies.Students.ReadStudentSummary(callCtx, now)
	})
	questions := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[1], func(callCtx context.Context) (QuestionSummary, error) {
		if a.dependencies.Questions == nil {
			return QuestionSummary{}, errDashboardDependencyUnavailable
		}
		return a.dependencies.Questions.ReadQuestionSummary(callCtx, now)
	})
	ai := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[2], func(callCtx context.Context) (AISummary, error) {
		if a.dependencies.AI == nil {
			return AISummary{}, errDashboardDependencyUnavailable
		}
		return a.dependencies.AI.ReadAISummary(callCtx, now)
	})
	storage := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[3], func(callCtx context.Context) (StorageSummary, error) {
		if a.dependencies.Storage == nil {
			return StorageSummary{}, errDashboardDependencyUnavailable
		}
		return a.dependencies.Storage.ReadStorageSummary(callCtx, now)
	})
	services := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[4], func(callCtx context.Context) ([]ServiceHealth, error) {
		if a.dependencies.Services == nil {
			return nil, errDashboardDependencyUnavailable
		}
		return a.dependencies.Services.ReadServiceHealth(callCtx, now)
	})
	queues := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[5], func(callCtx context.Context) ([]QueueSummary, error) {
		if a.dependencies.Queues == nil {
			return nil, errDashboardDependencyUnavailable
		}
		return a.dependencies.Queues.ReadQueueSummaries(callCtx, now)
	})
	backup := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[6], func(callCtx context.Context) (BackupSummary, error) {
		if a.dependencies.Backup == nil {
			return BackupSummary{}, errDashboardDependencyUnavailable
		}
		return a.dependencies.Backup.ReadBackupSummary(callCtx, now)
	})
	alerts := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[7], func(callCtx context.Context) (AlertSummary, error) {
		if a.dependencies.Alerts == nil {
			return AlertSummary{}, errDashboardDependencyUnavailable
		}
		return a.dependencies.Alerts.ReadAlertSummary(callCtx, now)
	})
	audit := dashboardResultChannel(ctx, a.dependencyTimeout, a.dependencySlots[8], func(callCtx context.Context) ([]AuditSummary, error) {
		if a.dependencies.Audit == nil {
			return nil, errDashboardDependencyUnavailable
		}
		return a.dependencies.Audit.ReadRecentAudit(callCtx, now, MaxRecentAudit)
	})

	studentResult := <-students
	questionResult := <-questions
	aiResult := <-ai
	storageResult := <-storage
	serviceResult := <-services
	queueResult := <-queues
	backupResult := <-backup
	alertResult := <-alerts
	auditResult := <-audit

	dashboard := Dashboard{
		ObservedAt:     now,
		ReleaseVersion: a.dependencies.ReleaseVersion,
		Services:       unavailableServices(DataStateUnavailable),
		Queues:         unavailableQueues(DataStateUnavailable),
		RecentAudit:    make([]AuditSummary, 0),
	}
	dashboard.Students = a.studentSummary(studentResult, now)
	dashboard.Questions = a.questionSummary(questionResult, now)
	dashboard.AI = a.aiSummary(aiResult, now)
	dashboard.Storage = a.storageSummary(storageResult, now)
	dashboard.Services = a.serviceHealth(serviceResult, now)
	dashboard.Queues = a.queueSummaries(queueResult, now)
	dashboard.Backup = a.backupSummary(backupResult, now)
	dashboard.Alerts = a.alertSummary(alertResult, now)
	dashboard.RecentAuditState, dashboard.RecentAudit = recentAudit(auditResult, now)
	return dashboard, nil
}

var errDashboardDependencyUnavailable = errors.New("dashboard dependency unavailable")

type dashboardDependencyResult[T any] struct {
	value   T
	failure DataState
}

type dashboardCallResult[T any] struct {
	value T
	err   error
}

func dashboardResultChannel[T any](
	parent context.Context,
	timeout time.Duration,
	slot chan struct{},
	call func(context.Context) (T, error),
) <-chan dashboardDependencyResult[T] {
	out := make(chan dashboardDependencyResult[T], 1)
	go func() {
		callCtx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		select {
		case slot <- struct{}{}:
		case <-callCtx.Done():
			out <- dashboardDependencyResult[T]{
				failure: dashboardFailureState(parent, callCtx, callCtx.Err()),
			}
			return
		}
		called := make(chan dashboardCallResult[T], 1)
		go func() {
			var result dashboardCallResult[T]
			defer func() {
				if recover() != nil {
					result.err = errDashboardDependencyUnavailable
				}
				<-slot
				called <- result
			}()
			result.value, result.err = call(callCtx)
		}()
		select {
		case result := <-called:
			if result.err != nil {
				out <- dashboardDependencyResult[T]{
					failure: dashboardFailureState(parent, callCtx, result.err),
				}
				return
			}
			out <- dashboardDependencyResult[T]{value: result.value}
		case <-callCtx.Done():
			out <- dashboardDependencyResult[T]{
				failure: dashboardFailureState(parent, callCtx, callCtx.Err()),
			}
		}
	}()
	return out
}

func dashboardFailureState(parent, callCtx context.Context, err error) DataState {
	if parent.Err() == nil &&
		(errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(callCtx.Err(), context.DeadlineExceeded)) {
		return DataStateTimeout
	}
	return DataStateUnavailable
}

func (a *DashboardAssembler) now() (now time.Time, ok bool) {
	if a == nil || a.clock == nil || a.freshFor <= 0 ||
		a.freshFor > MaxSampleFreshFor ||
		a.dependencyTimeout <= 0 || a.dependencyTimeout > DashboardDependencyTimeout {
		return time.Time{}, false
	}
	defer func() {
		if recover() != nil {
			now = time.Time{}
			ok = false
		}
	}()
	now = a.clock()
	if !validDashboardTime(now) {
		return time.Time{}, false
	}
	return now.UTC(), true
}

func (a *DashboardAssembler) studentSummary(
	result dashboardDependencyResult[StudentSummary],
	now time.Time,
) StudentSummary {
	if result.failure != "" {
		return StudentSummary{State: result.failure}
	}
	value := result.value
	state, observedAt, ok := a.normalizeState(value.State, value.ObservedAt, now)
	if !ok || !validDashboardCount(value.Active) || !validDashboardCount(value.Disabled) ||
		terminalDataStateRequiresZero(state) && (value.Active != 0 || value.Disabled != 0) {
		return StudentSummary{State: DataStateUnavailable}
	}
	value.State, value.ObservedAt = state, observedAt
	return value
}

func (a *DashboardAssembler) questionSummary(
	result dashboardDependencyResult[QuestionSummary],
	now time.Time,
) QuestionSummary {
	if result.failure != "" {
		return QuestionSummary{State: result.failure}
	}
	value := result.value
	state, observedAt, ok := a.normalizeState(value.State, value.ObservedAt, now)
	if !ok || !validDashboardCount(value.Waiting) ||
		!validDashboardSeconds(value.OldestWaitSeconds) ||
		(value.Waiting == 0 && value.OldestWaitSeconds != 0) ||
		terminalDataStateRequiresZero(state) &&
			(value.Waiting != 0 || value.OldestWaitSeconds != 0) {
		return QuestionSummary{State: DataStateUnavailable}
	}
	value.State, value.ObservedAt = state, observedAt
	return value
}

func (a *DashboardAssembler) aiSummary(
	result dashboardDependencyResult[AISummary],
	now time.Time,
) AISummary {
	if result.failure != "" {
		return AISummary{State: result.failure}
	}
	value := result.value
	state, observedAt, ok := a.normalizeState(value.State, value.ObservedAt, now)
	if !ok || !validDashboardCount(value.Requests) ||
		!validDashboardRate(value.SuccessRatePercent) ||
		!validDashboardDurationMilliseconds(value.FirstByteLatencyMilliseconds) ||
		!validDashboardDurationMilliseconds(value.TotalLatencyMilliseconds) ||
		!validDashboardCount(value.DailyCostMicroUSD) ||
		(value.Requests == 0 &&
			(value.SuccessRatePercent != 0 ||
				value.FirstByteLatencyMilliseconds != 0 ||
				value.TotalLatencyMilliseconds != 0 ||
				value.DailyCostMicroUSD != 0)) ||
		terminalDataStateRequiresZero(state) &&
			(value.Requests != 0 ||
				value.SuccessRatePercent != 0 ||
				value.FirstByteLatencyMilliseconds != 0 ||
				value.TotalLatencyMilliseconds != 0 ||
				value.DailyCostMicroUSD != 0) {
		return AISummary{State: DataStateUnavailable}
	}
	value.State, value.ObservedAt = state, observedAt
	return value
}

func (a *DashboardAssembler) storageSummary(
	result dashboardDependencyResult[StorageSummary],
	now time.Time,
) StorageSummary {
	if result.failure != "" {
		return StorageSummary{State: result.failure}
	}
	value := result.value
	state, observedAt, ok := a.normalizeState(value.State, value.ObservedAt, now)
	if !ok || !validDashboardCount(value.UsedBytes) ||
		!validDashboardCount(value.CapacityBytes) ||
		value.UsedBytes > value.CapacityBytes ||
		!validWarningPercent(value.WarningPercent, state) ||
		terminalDataStateRequiresZero(state) &&
			(value.UsedBytes != 0 || value.CapacityBytes != 0 || value.WarningPercent != 0) {
		return StorageSummary{State: DataStateUnavailable}
	}
	value.State, value.ObservedAt = state, observedAt
	return value
}

func (a *DashboardAssembler) serviceHealth(
	result dashboardDependencyResult[[]ServiceHealth],
	now time.Time,
) []ServiceHealth {
	if result.failure != "" {
		return unavailableServices(result.failure)
	}
	if len(result.value) > len(dashboardServiceOrder) {
		return unavailableServices(DataStateUnavailable)
	}
	indexed := make(map[DashboardService]ServiceHealth, len(result.value))
	for _, source := range result.value {
		if !validDashboardService(source.Service) {
			return unavailableServices(DataStateUnavailable)
		}
		if _, exists := indexed[source.Service]; exists {
			return unavailableServices(DataStateUnavailable)
		}
		state, observedAt, ok := a.normalizeState(source.State, source.ObservedAt, now)
		if !ok || !validDashboardDurationMilliseconds(source.LatencyMilliseconds) ||
			terminalDataStateRequiresZero(state) && source.LatencyMilliseconds != 0 {
			return unavailableServices(DataStateUnavailable)
		}
		source.State, source.ObservedAt = state, observedAt
		indexed[source.Service] = source
	}
	out := make([]ServiceHealth, 0, len(dashboardServiceOrder))
	for _, service := range dashboardServiceOrder {
		item, exists := indexed[service]
		if !exists {
			item = ServiceHealth{
				Service: service, State: DataStateEmpty,
				ObservedAt: cloneDashboardTime(&now),
			}
		}
		out = append(out, item)
	}
	return out
}

func (a *DashboardAssembler) queueSummaries(
	result dashboardDependencyResult[[]QueueSummary],
	now time.Time,
) []QueueSummary {
	if result.failure != "" {
		return unavailableQueues(result.failure)
	}
	if len(result.value) > len(dashboardQueueOrder) {
		return unavailableQueues(DataStateUnavailable)
	}
	indexed := make(map[DashboardQueue]QueueSummary, len(result.value))
	for _, source := range result.value {
		if !validDashboardQueue(source.Queue) {
			return unavailableQueues(DataStateUnavailable)
		}
		if _, exists := indexed[source.Queue]; exists {
			return unavailableQueues(DataStateUnavailable)
		}
		state, observedAt, ok := a.normalizeState(source.State, source.ObservedAt, now)
		if !ok || !validDashboardCount(source.Queued) ||
			!validDashboardCount(source.Streaming) ||
			!validDashboardCount(source.Failed) ||
			!validDashboardCount(source.Expired) ||
			terminalDataStateRequiresZero(state) &&
				(source.Queued != 0 || source.Streaming != 0 ||
					source.Failed != 0 || source.Expired != 0) {
			return unavailableQueues(DataStateUnavailable)
		}
		source.State, source.ObservedAt = state, observedAt
		indexed[source.Queue] = source
	}
	out := make([]QueueSummary, 0, len(dashboardQueueOrder))
	for _, queue := range dashboardQueueOrder {
		item, exists := indexed[queue]
		if !exists {
			item = QueueSummary{
				Queue: queue, State: DataStateEmpty,
				ObservedAt: cloneDashboardTime(&now),
			}
		}
		out = append(out, item)
	}
	return out
}

func (a *DashboardAssembler) backupSummary(
	result dashboardDependencyResult[BackupSummary],
	now time.Time,
) BackupSummary {
	if result.failure != "" {
		return unavailableBackup(result.failure)
	}
	value := result.value
	state, observedAt, ok := a.normalizeState(value.State, value.ObservedAt, now)
	if !ok {
		return unavailableBackup(DataStateUnavailable)
	}
	local, localOK := normalizeBackupPoint(value.Local, now)
	remote, remoteOK := normalizeBackupPoint(value.Remote, now)
	restore, restoreOK := normalizeRestorePoint(value.Restore, now)
	if !localOK || !remoteOK || !restoreOK ||
		terminalDataStateRequiresZero(state) &&
			(local.State != RecoveryStateEmpty ||
				remote.State != RecoveryStateEmpty ||
				restore.State != RecoveryStateEmpty) {
		return unavailableBackup(DataStateUnavailable)
	}
	value.State, value.ObservedAt = state, observedAt
	value.Local, value.Remote, value.Restore = local, remote, restore
	return value
}

func (a *DashboardAssembler) alertSummary(
	result dashboardDependencyResult[AlertSummary],
	now time.Time,
) AlertSummary {
	if result.failure != "" {
		return AlertSummary{State: result.failure}
	}
	value := result.value
	state, observedAt, ok := a.normalizeState(value.State, value.ObservedAt, now)
	if !ok || !validDashboardCount(value.OpenWarning) ||
		!validDashboardCount(value.OpenCritical) ||
		terminalDataStateRequiresZero(state) &&
			(value.OpenWarning != 0 || value.OpenCritical != 0) {
		return AlertSummary{State: DataStateUnavailable}
	}
	value.State, value.ObservedAt = state, observedAt
	return value
}

func recentAudit(
	result dashboardDependencyResult[[]AuditSummary],
	now time.Time,
) (DataState, []AuditSummary) {
	if result.failure != "" {
		return result.failure, make([]AuditSummary, 0)
	}
	if len(result.value) > MaxRecentAudit {
		return DataStateUnavailable, make([]AuditSummary, 0)
	}
	out := make([]AuditSummary, len(result.value))
	for i, item := range result.value {
		if !validAuditCategory(item.Category) ||
			!validAuditOutcome(item.Outcome) ||
			!validDashboardTime(item.OccurredAt) ||
			item.OccurredAt.After(now) {
			return DataStateUnavailable, make([]AuditSummary, 0)
		}
		item.OccurredAt = item.OccurredAt.UTC()
		out[i] = item
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	if len(out) == 0 {
		return DataStateEmpty, out
	}
	return DataStateHealthy, out
}

func (a *DashboardAssembler) normalizeState(
	state DataState,
	observedAt *time.Time,
	now time.Time,
) (DataState, *time.Time, bool) {
	if !validDataState(state) {
		return "", nil, false
	}
	switch state {
	case DataStateUnavailable, DataStateTimeout:
		if observedAt != nil {
			return "", nil, false
		}
		return state, nil, true
	default:
		if observedAt == nil || !validDashboardTime(*observedAt) ||
			observedAt.After(now) {
			return "", nil, false
		}
		copy := observedAt.UTC()
		if now.Sub(copy) > a.freshFor {
			state = DataStateStale
		}
		return state, &copy, true
	}
}

func terminalDataStateRequiresZero(state DataState) bool {
	return state == DataStateUnavailable ||
		state == DataStateTimeout ||
		state == DataStateEmpty
}

func validWarningPercent(value int, state DataState) bool {
	if state == DataStateUnavailable || state == DataStateTimeout || state == DataStateEmpty {
		return value == 0
	}
	return value >= 1 && value <= 100
}

func normalizeBackupPoint(value BackupPointSummary, now time.Time) (BackupPointSummary, bool) {
	if !validRecoveryState(value.State) {
		return BackupPointSummary{}, false
	}
	if value.State == RecoveryStateEmpty {
		if value.CompletedAt != nil {
			return BackupPointSummary{}, false
		}
		return value, true
	}
	if value.CompletedAt == nil ||
		!validDashboardTime(*value.CompletedAt) ||
		value.CompletedAt.After(now) {
		return BackupPointSummary{}, false
	}
	value.CompletedAt = cloneDashboardTime(value.CompletedAt)
	return value, true
}

func normalizeRestorePoint(value RestorePointSummary, now time.Time) (RestorePointSummary, bool) {
	point, ok := normalizeBackupPoint(BackupPointSummary{
		State: value.State, CompletedAt: value.CompletedAt,
	}, now)
	if !ok || !validDashboardSeconds(value.RTOSeconds) ||
		value.State == RecoveryStateEmpty && value.RTOSeconds != 0 ||
		value.State == RecoveryStateFailed && value.RTOSeconds != 0 {
		return RestorePointSummary{}, false
	}
	value.CompletedAt = point.CompletedAt
	return value, true
}

func unavailableServices(state DataState) []ServiceHealth {
	out := make([]ServiceHealth, 0, len(dashboardServiceOrder))
	for _, service := range dashboardServiceOrder {
		out = append(out, ServiceHealth{Service: service, State: state})
	}
	return out
}

func unavailableQueues(state DataState) []QueueSummary {
	out := make([]QueueSummary, 0, len(dashboardQueueOrder))
	for _, queue := range dashboardQueueOrder {
		out = append(out, QueueSummary{Queue: queue, State: state})
	}
	return out
}

func unavailableBackup(state DataState) BackupSummary {
	return BackupSummary{
		State:   state,
		Local:   BackupPointSummary{State: RecoveryStateEmpty},
		Remote:  BackupPointSummary{State: RecoveryStateEmpty},
		Restore: RestorePointSummary{State: RecoveryStateEmpty},
	}
}

func failClosedDashboard(observedAt time.Time) Dashboard {
	return Dashboard{
		ObservedAt:       observedAt,
		Students:         StudentSummary{State: DataStateUnavailable},
		Questions:        QuestionSummary{State: DataStateUnavailable},
		AI:               AISummary{State: DataStateUnavailable},
		Storage:          StorageSummary{State: DataStateUnavailable},
		Services:         unavailableServices(DataStateUnavailable),
		Queues:           unavailableQueues(DataStateUnavailable),
		Backup:           unavailableBackup(DataStateUnavailable),
		Alerts:           AlertSummary{State: DataStateUnavailable},
		RecentAuditState: DataStateUnavailable,
		RecentAudit:      make([]AuditSummary, 0),
	}
}

func cloneDashboardTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func cloneDashboard(source Dashboard) Dashboard {
	out := source
	out.Students.ObservedAt = cloneDashboardTime(source.Students.ObservedAt)
	out.Questions.ObservedAt = cloneDashboardTime(source.Questions.ObservedAt)
	out.AI.ObservedAt = cloneDashboardTime(source.AI.ObservedAt)
	out.Storage.ObservedAt = cloneDashboardTime(source.Storage.ObservedAt)
	out.Backup.ObservedAt = cloneDashboardTime(source.Backup.ObservedAt)
	out.Backup.Local.CompletedAt = cloneDashboardTime(source.Backup.Local.CompletedAt)
	out.Backup.Remote.CompletedAt = cloneDashboardTime(source.Backup.Remote.CompletedAt)
	out.Backup.Restore.CompletedAt = cloneDashboardTime(source.Backup.Restore.CompletedAt)
	out.Alerts.ObservedAt = cloneDashboardTime(source.Alerts.ObservedAt)

	out.Services = make([]ServiceHealth, len(source.Services))
	copy(out.Services, source.Services)
	for index := range out.Services {
		out.Services[index].ObservedAt = cloneDashboardTime(source.Services[index].ObservedAt)
	}
	out.Queues = make([]QueueSummary, len(source.Queues))
	copy(out.Queues, source.Queues)
	for index := range out.Queues {
		out.Queues[index].ObservedAt = cloneDashboardTime(source.Queues[index].ObservedAt)
	}
	out.RecentAudit = make([]AuditSummary, len(source.RecentAudit))
	copy(out.RecentAudit, source.RecentAudit)
	return out
}
