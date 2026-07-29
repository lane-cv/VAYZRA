package operations

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type dashboardSourceStub struct {
	students  func(context.Context, time.Time) (StudentSummary, error)
	questions func(context.Context, time.Time) (QuestionSummary, error)
	ai        func(context.Context, time.Time) (AISummary, error)
	storage   func(context.Context, time.Time) (StorageSummary, error)
	services  func(context.Context, time.Time) ([]ServiceHealth, error)
	queues    func(context.Context, time.Time) ([]QueueSummary, error)
	backup    func(context.Context, time.Time) (BackupSummary, error)
	alerts    func(context.Context, time.Time) (AlertSummary, error)
	audit     func(context.Context, time.Time, int) ([]AuditSummary, error)
}

func (s *dashboardSourceStub) ReadStudentSummary(ctx context.Context, now time.Time) (StudentSummary, error) {
	return s.students(ctx, now)
}

func (s *dashboardSourceStub) ReadQuestionSummary(ctx context.Context, now time.Time) (QuestionSummary, error) {
	return s.questions(ctx, now)
}

func (s *dashboardSourceStub) ReadAISummary(ctx context.Context, now time.Time) (AISummary, error) {
	return s.ai(ctx, now)
}

func (s *dashboardSourceStub) ReadStorageSummary(ctx context.Context, now time.Time) (StorageSummary, error) {
	return s.storage(ctx, now)
}

func (s *dashboardSourceStub) ReadServiceHealth(ctx context.Context, now time.Time) ([]ServiceHealth, error) {
	return s.services(ctx, now)
}

func (s *dashboardSourceStub) ReadQueueSummaries(ctx context.Context, now time.Time) ([]QueueSummary, error) {
	return s.queues(ctx, now)
}

func (s *dashboardSourceStub) ReadBackupSummary(ctx context.Context, now time.Time) (BackupSummary, error) {
	return s.backup(ctx, now)
}

func (s *dashboardSourceStub) ReadAlertSummary(ctx context.Context, now time.Time) (AlertSummary, error) {
	return s.alerts(ctx, now)
}

func (s *dashboardSourceStub) ReadRecentAudit(ctx context.Context, now time.Time, limit int) ([]AuditSummary, error) {
	return s.audit(ctx, now, limit)
}

func TestDashboardAssemblerCollectsAllDependenciesConcurrentlyWithBoundedContexts(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	started := make(chan struct{}, 9)
	release := make(chan struct{})
	var deadlineMu sync.Mutex
	var remaining []time.Duration
	wait := func(ctx context.Context, gotNow time.Time) error {
		if gotNow != now {
			t.Errorf("collector now=%s want=%s", gotNow, now)
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("collector context has no deadline")
		} else {
			deadlineMu.Lock()
			remaining = append(remaining, time.Until(deadline))
			deadlineMu.Unlock()
		}
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	at := timePointer(now)
	source := &dashboardSourceStub{
		students: func(ctx context.Context, gotNow time.Time) (StudentSummary, error) {
			return StudentSummary{State: DataStateHealthy, ObservedAt: at, Active: 3, Disabled: 1}, wait(ctx, gotNow)
		},
		questions: func(ctx context.Context, gotNow time.Time) (QuestionSummary, error) {
			return QuestionSummary{State: DataStateHealthy, ObservedAt: at, Waiting: 2, OldestWaitSeconds: 15}, wait(ctx, gotNow)
		},
		ai: func(ctx context.Context, gotNow time.Time) (AISummary, error) {
			return AISummary{
				State: DataStateHealthy, ObservedAt: at, Requests: 4,
				SuccessRatePercent: 75, FirstByteLatencyMilliseconds: 50,
				TotalLatencyMilliseconds: 250, DailyCostMicroUSD: 100,
			}, wait(ctx, gotNow)
		},
		storage: func(ctx context.Context, gotNow time.Time) (StorageSummary, error) {
			return StorageSummary{
				State: DataStateHealthy, ObservedAt: at,
				UsedBytes: 100, CapacityBytes: 1000, WarningPercent: 75,
			}, wait(ctx, gotNow)
		},
		services: func(ctx context.Context, gotNow time.Time) ([]ServiceHealth, error) {
			return []ServiceHealth{
				{Service: ServiceWorker, State: DataStateHealthy, ObservedAt: at, LatencyMilliseconds: 5},
				{Service: ServiceObjectStore, State: DataStateHealthy, ObservedAt: at, LatencyMilliseconds: 4},
				{Service: ServiceRedis, State: DataStateHealthy, ObservedAt: at, LatencyMilliseconds: 3},
				{Service: ServicePostgres, State: DataStateHealthy, ObservedAt: at, LatencyMilliseconds: 2},
				{Service: ServiceCaddy, State: DataStateHealthy, ObservedAt: at, LatencyMilliseconds: 1},
				{Service: ServiceApp, State: DataStateHealthy, ObservedAt: at, LatencyMilliseconds: 1},
			}, wait(ctx, gotNow)
		},
		queues: func(ctx context.Context, gotNow time.Time) ([]QueueSummary, error) {
			return []QueueSummary{
				{Queue: QueueOutbox, State: DataStateHealthy, ObservedAt: at},
				{Queue: QueueAI, State: DataStateDegraded, ObservedAt: at, Queued: 2, Streaming: 1},
				{Queue: QueueProcessing, State: DataStateHealthy, ObservedAt: at, Queued: 1},
			}, wait(ctx, gotNow)
		},
		backup: func(ctx context.Context, gotNow time.Time) (BackupSummary, error) {
			return BackupSummary{
				State: DataStateHealthy, ObservedAt: at,
				Local:  BackupPointSummary{State: RecoveryStateSucceeded, CompletedAt: at},
				Remote: BackupPointSummary{State: RecoveryStateEmpty},
				Restore: RestorePointSummary{
					State: RecoveryStateSucceeded, CompletedAt: at, RTOSeconds: 30,
				},
			}, wait(ctx, gotNow)
		},
		alerts: func(ctx context.Context, gotNow time.Time) (AlertSummary, error) {
			return AlertSummary{
				State: DataStateHealthy, ObservedAt: at, OpenWarning: 2, OpenCritical: 1,
			}, wait(ctx, gotNow)
		},
		audit: func(ctx context.Context, gotNow time.Time, limit int) ([]AuditSummary, error) {
			if limit != MaxRecentAudit {
				t.Errorf("audit limit=%d want=%d", limit, MaxRecentAudit)
			}
			return []AuditSummary{{
				Category:   AuditCategoryOperations,
				Outcome:    AuditOutcomeSucceeded,
				OccurredAt: now,
			}}, wait(ctx, gotNow)
		},
	}
	assembler, err := NewDashboardAssembler(
		func() time.Time { return now },
		time.Minute,
		DashboardDependencies{
			Students: source, Questions: source, AI: source, Storage: source,
			Services: source, Queues: source, Backup: source, Alerts: source,
			Audit: source,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan Dashboard, 1)
	resultErr := make(chan error, 1)
	go func() {
		dashboard, assembleErr := assembler.Assemble(context.Background())
		result <- dashboard
		resultErr <- assembleErr
	}()
	for i := 0; i < 9; i++ {
		select {
		case <-started:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("only %d collectors started before release; collection is serial", i)
		}
	}
	close(release)
	dashboard := <-result
	if err = <-resultErr; err != nil {
		t.Fatal(err)
	}
	if dashboard.ObservedAt != now ||
		dashboard.Students.Active != 3 ||
		dashboard.Questions.Waiting != 2 ||
		dashboard.AI.Requests != 4 ||
		dashboard.Storage.UsedBytes != 100 ||
		dashboard.Backup.Restore.RTOSeconds != 30 ||
		dashboard.Alerts.OpenCritical != 1 ||
		dashboard.RecentAuditState != DataStateHealthy ||
		len(dashboard.RecentAudit) != 1 {
		t.Fatalf("unexpected dashboard: %#v", dashboard)
	}
	wantServices := DashboardServiceOrder()
	if len(dashboard.Services) != len(wantServices) {
		t.Fatalf("services=%#v", dashboard.Services)
	}
	for i := range wantServices {
		if dashboard.Services[i].Service != wantServices[i] {
			t.Fatalf("services[%d]=%q want=%q", i, dashboard.Services[i].Service, wantServices[i])
		}
	}
	wantQueues := DashboardQueueOrder()
	if len(dashboard.Queues) != len(wantQueues) {
		t.Fatalf("queues=%#v", dashboard.Queues)
	}
	for i := range wantQueues {
		if dashboard.Queues[i].Queue != wantQueues[i] {
			t.Fatalf("queues[%d]=%q want=%q", i, dashboard.Queues[i].Queue, wantQueues[i])
		}
	}
	deadlineMu.Lock()
	defer deadlineMu.Unlock()
	if len(remaining) != 9 {
		t.Fatalf("deadlines=%d", len(remaining))
	}
	for _, duration := range remaining {
		if duration <= 1500*time.Millisecond || duration > DashboardDependencyTimeout+100*time.Millisecond {
			t.Fatalf("dependency deadline remaining=%s", duration)
		}
	}
}

func TestDashboardAssemblerPreservesGoodSectionsAndMarksFailureTimeoutStaleAndEmpty(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
	staleAt := timePointer(now.Add(-2 * time.Minute))
	freshAt := timePointer(now)
	source := validDashboardSource(now)
	source.students = func(context.Context, time.Time) (StudentSummary, error) {
		return StudentSummary{
			State: DataStateHealthy, ObservedAt: staleAt, Active: 99,
		}, nil
	}
	source.questions = func(context.Context, time.Time) (QuestionSummary, error) {
		return QuestionSummary{}, errors.New("database contains private path /student")
	}
	source.ai = func(ctx context.Context, _ time.Time) (AISummary, error) {
		<-ctx.Done()
		return AISummary{}, ctx.Err()
	}
	source.audit = func(context.Context, time.Time, int) ([]AuditSummary, error) {
		return []AuditSummary{}, nil
	}
	assembler := mustDashboardAssembler(t, now, source)
	assembler.dependencyTimeout = 20 * time.Millisecond

	dashboard, err := assembler.Assemble(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Students.State != DataStateStale ||
		dashboard.Students.Active != 99 ||
		dashboard.Students.ObservedAt == nil ||
		*dashboard.Students.ObservedAt != *staleAt {
		t.Fatalf("students=%#v", dashboard.Students)
	}
	if dashboard.Questions.State != DataStateUnavailable ||
		dashboard.Questions.ObservedAt != nil ||
		dashboard.Questions.Waiting != 0 {
		t.Fatalf("questions=%#v", dashboard.Questions)
	}
	if dashboard.AI.State != DataStateTimeout ||
		dashboard.AI.ObservedAt != nil ||
		dashboard.AI.Requests != 0 {
		t.Fatalf("ai=%#v", dashboard.AI)
	}
	if dashboard.Storage.State != DataStateHealthy ||
		dashboard.Storage.ObservedAt == nil ||
		*dashboard.Storage.ObservedAt != *freshAt {
		t.Fatalf("good storage lost: %#v", dashboard.Storage)
	}
	if dashboard.RecentAuditState != DataStateEmpty || dashboard.RecentAudit == nil ||
		len(dashboard.RecentAudit) != 0 {
		t.Fatalf("audit state=%q items=%#v", dashboard.RecentAuditState, dashboard.RecentAudit)
	}
}

func TestDashboardAssemblerBoundsDependenciesThatIgnoreContext(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 30, 0, 0, time.UTC)
	source := validDashboardSource(now)
	release := make(chan struct{})
	firstReturned := make(chan struct{})
	var calls atomic.Int32
	source.ai = func(context.Context, time.Time) (AISummary, error) {
		call := calls.Add(1)
		if call == 1 {
			<-release
			close(firstReturned)
		}
		return AISummary{
			State: DataStateHealthy, ObservedAt: timePointer(now), Requests: 1,
			SuccessRatePercent: 100,
		}, nil
	}
	assembler := mustDashboardAssembler(t, now, source)
	assembler.dependencyTimeout = 20 * time.Millisecond

	first, err := assembler.Assemble(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.AI.State != DataStateTimeout || calls.Load() != 1 {
		t.Fatalf("first AI=%#v calls=%d", first.AI, calls.Load())
	}
	second, err := assembler.Assemble(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.AI.State != DataStateTimeout || calls.Load() != 1 {
		t.Fatalf("ignored-context dependency multiplied: AI=%#v calls=%d", second.AI, calls.Load())
	}

	close(release)
	select {
	case <-firstReturned:
	case <-time.After(time.Second):
		t.Fatal("ignored-context fixture did not release")
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 1 && time.Now().Before(deadline) {
		third, thirdErr := assembler.Assemble(context.Background())
		if thirdErr != nil {
			t.Fatal(thirdErr)
		}
		if third.AI.State == DataStateHealthy {
			break
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("released dependency did not become callable: calls=%d", calls.Load())
	}
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func newObservedDoneContext(parent context.Context) *observedDoneContext {
	return &observedDoneContext{
		Context:  parent,
		observed: make(chan struct{}),
	}
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

type dashboardAssembleResult struct {
	dashboard Dashboard
	err       error
}

func TestDashboardAssemblerSingleFlightKeepsWaiterCancellationIndependentAndCopiesResults(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 45, 0, 0, time.UTC)
	source := validDashboardSource(now)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	started := make(chan int, 64)
	var calls [9]atomic.Int32
	source.backup = func(context.Context, time.Time) (BackupSummary, error) {
		at := timePointer(now)
		return BackupSummary{
			State:      DataStateHealthy,
			ObservedAt: at,
			Local: BackupPointSummary{
				State: RecoveryStateSucceeded, CompletedAt: at,
			},
			Remote: BackupPointSummary{
				State: RecoveryStateSucceeded, CompletedAt: at,
			},
			Restore: RestorePointSummary{
				State: RecoveryStateSucceeded, CompletedAt: at, RTOSeconds: 20,
			},
		}, nil
	}
	probe := func(ctx context.Context, dependency int) error {
		calls[dependency].Add(1)
		started <- dependency
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	originalStudents := source.students
	source.students = func(ctx context.Context, gotNow time.Time) (StudentSummary, error) {
		if err := probe(ctx, 0); err != nil {
			return StudentSummary{}, err
		}
		return originalStudents(ctx, gotNow)
	}
	originalQuestions := source.questions
	source.questions = func(ctx context.Context, gotNow time.Time) (QuestionSummary, error) {
		if err := probe(ctx, 1); err != nil {
			return QuestionSummary{}, err
		}
		return originalQuestions(ctx, gotNow)
	}
	originalAI := source.ai
	source.ai = func(ctx context.Context, gotNow time.Time) (AISummary, error) {
		if err := probe(ctx, 2); err != nil {
			return AISummary{}, err
		}
		return originalAI(ctx, gotNow)
	}
	originalStorage := source.storage
	source.storage = func(ctx context.Context, gotNow time.Time) (StorageSummary, error) {
		if err := probe(ctx, 3); err != nil {
			return StorageSummary{}, err
		}
		return originalStorage(ctx, gotNow)
	}
	originalServices := source.services
	source.services = func(ctx context.Context, gotNow time.Time) ([]ServiceHealth, error) {
		if err := probe(ctx, 4); err != nil {
			return nil, err
		}
		return originalServices(ctx, gotNow)
	}
	originalQueues := source.queues
	source.queues = func(ctx context.Context, gotNow time.Time) ([]QueueSummary, error) {
		if err := probe(ctx, 5); err != nil {
			return nil, err
		}
		return originalQueues(ctx, gotNow)
	}
	originalBackup := source.backup
	source.backup = func(ctx context.Context, gotNow time.Time) (BackupSummary, error) {
		if err := probe(ctx, 6); err != nil {
			return BackupSummary{}, err
		}
		return originalBackup(ctx, gotNow)
	}
	originalAlerts := source.alerts
	source.alerts = func(ctx context.Context, gotNow time.Time) (AlertSummary, error) {
		if err := probe(ctx, 7); err != nil {
			return AlertSummary{}, err
		}
		return originalAlerts(ctx, gotNow)
	}
	originalAudit := source.audit
	source.audit = func(ctx context.Context, gotNow time.Time, limit int) ([]AuditSummary, error) {
		if err := probe(ctx, 8); err != nil {
			return nil, err
		}
		return originalAudit(ctx, gotNow, limit)
	}
	assembler := mustDashboardAssembler(t, now, source)

	startAssemble := func(ctx context.Context) <-chan dashboardAssembleResult {
		result := make(chan dashboardAssembleResult, 1)
		go func() {
			dashboard, err := assembler.Assemble(ctx)
			result <- dashboardAssembleResult{dashboard: dashboard, err: err}
		}()
		return result
	}

	leaderBase, cancelLeader := context.WithCancel(context.Background())
	leaderContext := newObservedDoneContext(leaderBase)
	leaderResult := startAssemble(leaderContext)
	seen := make(map[int]bool, 9)
	for len(seen) < 9 {
		select {
		case dependency := <-started:
			seen[dependency] = true
		case <-time.After(time.Second):
			t.Fatalf("only dependencies %v started", seen)
		}
	}
	select {
	case <-leaderContext.observed:
	case <-time.After(time.Second):
		t.Fatal("leader did not begin waiting")
	}

	firstBase, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstContext := newObservedDoneContext(firstBase)
	firstResult := startAssemble(firstContext)
	secondBase, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	secondContext := newObservedDoneContext(secondBase)
	secondResult := startAssemble(secondContext)
	cancelledBase, cancelWaiter := context.WithCancel(context.Background())
	cancelledContext := newObservedDoneContext(cancelledBase)
	cancelledResult := startAssemble(cancelledContext)
	for name, waiter := range map[string]*observedDoneContext{
		"first": firstContext, "second": secondContext, "cancelled": cancelledContext,
	} {
		select {
		case <-waiter.observed:
		case <-time.After(time.Second):
			t.Fatalf("%s waiter did not begin waiting", name)
		}
	}

	cancelWaiter()
	cancelLeader()
	if result := <-cancelledResult; !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancelled waiter error=%v dashboard=%#v", result.err, result.dashboard)
	}
	if result := <-leaderResult; !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancelled leader error=%v dashboard=%#v", result.err, result.dashboard)
	}

	select {
	case dependency := <-started:
		t.Errorf("shared probe %d restarted after waiter cancellation", dependency)
	case <-time.After(50 * time.Millisecond):
	}
	releaseAll()
	first := <-firstResult
	second := <-secondResult
	if first.err != nil || second.err != nil {
		t.Fatalf("successful waiters errors: first=%v second=%v", first.err, second.err)
	}
	for dependency := range calls {
		if got := calls[dependency].Load(); got != 1 {
			t.Fatalf("dependency %d calls=%d want=1", dependency, got)
		}
	}

	firstDashboard := first.dashboard
	secondDashboard := second.dashboard
	for name, pair := range map[string][2]*time.Time{
		"students":      {firstDashboard.Students.ObservedAt, secondDashboard.Students.ObservedAt},
		"questions":     {firstDashboard.Questions.ObservedAt, secondDashboard.Questions.ObservedAt},
		"ai":            {firstDashboard.AI.ObservedAt, secondDashboard.AI.ObservedAt},
		"storage":       {firstDashboard.Storage.ObservedAt, secondDashboard.Storage.ObservedAt},
		"service":       {firstDashboard.Services[0].ObservedAt, secondDashboard.Services[0].ObservedAt},
		"queue":         {firstDashboard.Queues[0].ObservedAt, secondDashboard.Queues[0].ObservedAt},
		"backup":        {firstDashboard.Backup.ObservedAt, secondDashboard.Backup.ObservedAt},
		"local backup":  {firstDashboard.Backup.Local.CompletedAt, secondDashboard.Backup.Local.CompletedAt},
		"remote backup": {firstDashboard.Backup.Remote.CompletedAt, secondDashboard.Backup.Remote.CompletedAt},
		"restore":       {firstDashboard.Backup.Restore.CompletedAt, secondDashboard.Backup.Restore.CompletedAt},
		"alerts":        {firstDashboard.Alerts.ObservedAt, secondDashboard.Alerts.ObservedAt},
	} {
		if pair[0] == nil || pair[1] == nil || pair[0] == pair[1] {
			t.Fatalf("%s time pointers are not independently copied: %p %p", name, pair[0], pair[1])
		}
	}
	if &firstDashboard.Services[0] == &secondDashboard.Services[0] ||
		&firstDashboard.Queues[0] == &secondDashboard.Queues[0] ||
		&firstDashboard.RecentAudit[0] == &secondDashboard.RecentAudit[0] {
		t.Fatal("shared dashboard slices alias callers")
	}
	originalStudentObservedAt := *secondDashboard.Students.ObservedAt
	originalServiceObservedAt := *secondDashboard.Services[0].ObservedAt
	originalQueueObservedAt := *secondDashboard.Queues[0].ObservedAt
	originalBackupAt := *secondDashboard.Backup.Local.CompletedAt
	firstDashboard.Services[0].State = DataStateUnavailable
	firstDashboard.Queues[0].Queued = 999
	firstDashboard.RecentAudit[0].Category = AuditCategoryBackup
	*firstDashboard.Students.ObservedAt = firstDashboard.Students.ObservedAt.Add(time.Hour)
	*firstDashboard.Services[0].ObservedAt = firstDashboard.Services[0].ObservedAt.Add(time.Hour)
	*firstDashboard.Queues[0].ObservedAt = firstDashboard.Queues[0].ObservedAt.Add(time.Hour)
	*firstDashboard.Backup.Local.CompletedAt = firstDashboard.Backup.Local.CompletedAt.Add(time.Hour)
	if secondDashboard.Services[0].State != DataStateHealthy ||
		secondDashboard.Queues[0].Queued != 0 ||
		secondDashboard.RecentAudit[0].Category != AuditCategoryOperations ||
		*secondDashboard.Students.ObservedAt != originalStudentObservedAt ||
		*secondDashboard.Services[0].ObservedAt != originalServiceObservedAt ||
		*secondDashboard.Queues[0].ObservedAt != originalQueueObservedAt ||
		*secondDashboard.Backup.Local.CompletedAt != originalBackupAt {
		t.Fatalf("shared dashboard result aliases callers: first=%#v second=%#v", firstDashboard, secondDashboard)
	}
}

func TestDashboardAssemblerRejectsSensitiveUnknownDuplicateAndOutOfBoundsInput(t *testing.T) {
	now := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	at := timePointer(now)
	source := validDashboardSource(now)
	source.students = func(context.Context, time.Time) (StudentSummary, error) {
		return StudentSummary{
			State: DataStateHealthy, ObservedAt: at, Active: -1,
		}, nil
	}
	source.ai = func(context.Context, time.Time) (AISummary, error) {
		return AISummary{
			State: DataStateHealthy, ObservedAt: at,
			Requests: 1, SuccessRatePercent: math.Inf(1),
		}, nil
	}
	source.services = func(context.Context, time.Time) ([]ServiceHealth, error) {
		return []ServiceHealth{{
			Service: "postgres/student-secret", State: DataStateHealthy, ObservedAt: at,
		}}, nil
	}
	source.queues = func(context.Context, time.Time) ([]QueueSummary, error) {
		return []QueueSummary{
			{Queue: QueueAI, State: DataStateHealthy, ObservedAt: at},
			{Queue: QueueAI, State: DataStateHealthy, ObservedAt: at},
		}, nil
	}
	source.audit = func(context.Context, time.Time, int) ([]AuditSummary, error) {
		items := make([]AuditSummary, MaxRecentAudit+1)
		for i := range items {
			items[i] = AuditSummary{
				Category:   "operations/student-secret",
				Outcome:    AuditOutcomeSucceeded,
				OccurredAt: now,
			}
		}
		return items, nil
	}
	assembler := mustDashboardAssembler(t, now, source)
	dashboard, err := assembler.Assemble(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Students.State != DataStateUnavailable || dashboard.Students.Active != 0 {
		t.Fatalf("invalid students leaked: %#v", dashboard.Students)
	}
	if dashboard.AI.State != DataStateUnavailable || dashboard.AI.SuccessRatePercent != 0 {
		t.Fatalf("invalid AI leaked: %#v", dashboard.AI)
	}
	for _, service := range dashboard.Services {
		if service.State != DataStateUnavailable || service.Service == "postgres/student-secret" {
			t.Fatalf("invalid services leaked: %#v", dashboard.Services)
		}
	}
	for _, queue := range dashboard.Queues {
		if queue.State != DataStateUnavailable {
			t.Fatalf("duplicate queues were accepted: %#v", dashboard.Queues)
		}
	}
	if dashboard.RecentAuditState != DataStateUnavailable ||
		len(dashboard.RecentAudit) != 0 {
		t.Fatalf("invalid audit leaked: state=%q items=%#v", dashboard.RecentAuditState, dashboard.RecentAudit)
	}
}

func TestDashboardAssemblerDefensivelyCopiesSlicesAndTimes(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	at := now
	services := []ServiceHealth{
		{Service: ServiceApp, State: DataStateHealthy, ObservedAt: &at},
		{Service: ServiceCaddy, State: DataStateHealthy, ObservedAt: &at},
		{Service: ServicePostgres, State: DataStateHealthy, ObservedAt: &at},
		{Service: ServiceRedis, State: DataStateHealthy, ObservedAt: &at},
		{Service: ServiceObjectStore, State: DataStateHealthy, ObservedAt: &at},
		{Service: ServiceWorker, State: DataStateHealthy, ObservedAt: &at},
	}
	auditItems := []AuditSummary{{
		Category:   AuditCategoryOperations,
		Outcome:    AuditOutcomeSucceeded,
		OccurredAt: now,
	}}
	source := validDashboardSource(now)
	source.services = func(context.Context, time.Time) ([]ServiceHealth, error) {
		return services, nil
	}
	source.audit = func(context.Context, time.Time, int) ([]AuditSummary, error) {
		return auditItems, nil
	}
	assembler := mustDashboardAssembler(t, now, source)
	dashboard, err := assembler.Assemble(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	services[0].State = DataStateUnavailable
	at = at.Add(time.Hour)
	auditItems[0].Category = AuditCategoryBackup
	if dashboard.Services[0].State != DataStateHealthy ||
		dashboard.Services[0].ObservedAt == nil ||
		*dashboard.Services[0].ObservedAt != now ||
		dashboard.RecentAudit[0].Category != AuditCategoryOperations {
		t.Fatalf("dashboard aliases dependency memory: %#v %#v", dashboard.Services, dashboard.RecentAudit)
	}
	serviceOrder := DashboardServiceOrder()
	serviceOrder[0] = "student-secret"
	if DashboardServiceOrder()[0] != ServiceApp {
		t.Fatal("service order aliases package storage")
	}
}

func TestDashboardAssemblerNilDependenciesAndCancelledContextFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	assembler, err := NewDashboardAssembler(
		func() time.Time { return now }, time.Minute, DashboardDependencies{},
	)
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := assembler.Assemble(nil)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil context error=%v", err)
	}
	assertDashboardFailClosed(t, dashboard)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dashboard, err = assembler.Assemble(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	assertDashboardFailClosed(t, dashboard)

	dashboard, err = assembler.Assemble(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertDashboardFailClosed(t, dashboard)
}

func validDashboardSource(now time.Time) *dashboardSourceStub {
	at := timePointer(now)
	return &dashboardSourceStub{
		students: func(context.Context, time.Time) (StudentSummary, error) {
			return StudentSummary{State: DataStateHealthy, ObservedAt: at}, nil
		},
		questions: func(context.Context, time.Time) (QuestionSummary, error) {
			return QuestionSummary{State: DataStateHealthy, ObservedAt: at}, nil
		},
		ai: func(context.Context, time.Time) (AISummary, error) {
			return AISummary{State: DataStateHealthy, ObservedAt: at}, nil
		},
		storage: func(context.Context, time.Time) (StorageSummary, error) {
			return StorageSummary{
				State: DataStateHealthy, ObservedAt: at,
				CapacityBytes: 1, WarningPercent: 75,
			}, nil
		},
		services: func(context.Context, time.Time) ([]ServiceHealth, error) {
			return []ServiceHealth{
				{Service: ServiceApp, State: DataStateHealthy, ObservedAt: at},
				{Service: ServiceCaddy, State: DataStateHealthy, ObservedAt: at},
				{Service: ServicePostgres, State: DataStateHealthy, ObservedAt: at},
				{Service: ServiceRedis, State: DataStateHealthy, ObservedAt: at},
				{Service: ServiceObjectStore, State: DataStateHealthy, ObservedAt: at},
				{Service: ServiceWorker, State: DataStateHealthy, ObservedAt: at},
			}, nil
		},
		queues: func(context.Context, time.Time) ([]QueueSummary, error) {
			return []QueueSummary{
				{Queue: QueueProcessing, State: DataStateHealthy, ObservedAt: at},
				{Queue: QueueAI, State: DataStateHealthy, ObservedAt: at},
				{Queue: QueueOutbox, State: DataStateHealthy, ObservedAt: at},
			}, nil
		},
		backup: func(context.Context, time.Time) (BackupSummary, error) {
			return BackupSummary{
				State: DataStateEmpty, ObservedAt: at,
				Local:   BackupPointSummary{State: RecoveryStateEmpty},
				Remote:  BackupPointSummary{State: RecoveryStateEmpty},
				Restore: RestorePointSummary{State: RecoveryStateEmpty},
			}, nil
		},
		alerts: func(context.Context, time.Time) (AlertSummary, error) {
			return AlertSummary{State: DataStateHealthy, ObservedAt: at}, nil
		},
		audit: func(context.Context, time.Time, int) ([]AuditSummary, error) {
			return []AuditSummary{{
				Category:   AuditCategoryOperations,
				Outcome:    AuditOutcomeSucceeded,
				OccurredAt: now,
			}}, nil
		},
	}
}

func mustDashboardAssembler(t *testing.T, now time.Time, source *dashboardSourceStub) *DashboardAssembler {
	t.Helper()
	assembler, err := NewDashboardAssembler(
		func() time.Time { return now }, time.Minute,
		DashboardDependencies{
			Students: source, Questions: source, AI: source, Storage: source,
			Services: source, Queues: source, Backup: source, Alerts: source,
			Audit: source,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return assembler
}

func assertDashboardFailClosed(t *testing.T, dashboard Dashboard) {
	t.Helper()
	for name, state := range map[string]DataState{
		"students":  dashboard.Students.State,
		"questions": dashboard.Questions.State,
		"ai":        dashboard.AI.State,
		"storage":   dashboard.Storage.State,
		"backup":    dashboard.Backup.State,
		"alerts":    dashboard.Alerts.State,
		"audit":     dashboard.RecentAuditState,
	} {
		if state != DataStateUnavailable {
			t.Fatalf("%s state=%q dashboard=%#v", name, state, dashboard)
		}
	}
	if len(dashboard.Services) != len(DashboardServiceOrder()) ||
		len(dashboard.Queues) != len(DashboardQueueOrder()) {
		t.Fatalf("fail-closed arrays are not fixed: services=%#v queues=%#v", dashboard.Services, dashboard.Queues)
	}
	for _, item := range dashboard.Services {
		if item.State != DataStateUnavailable {
			t.Fatalf("service=%#v", item)
		}
	}
	for _, item := range dashboard.Queues {
		if item.State != DataStateUnavailable {
			t.Fatalf("queue=%#v", item)
		}
	}
	if len(dashboard.RecentAudit) != 0 {
		t.Fatalf("recent audit=%#v", dashboard.RecentAudit)
	}
}
