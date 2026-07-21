package processing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRetryScheduleAndPermanentFailures(t *testing.T) {
	now := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	for _, tc := range []struct {
		attempts  int
		permanent bool
		wantDelay time.Duration
	}{{1, false, time.Minute}, {2, false, 5 * time.Minute}, {3, false, 30 * time.Minute}, {4, true, 0}} {
		failure := classifyFailure(&ProcessingError{Category: "storage_unavailable"}, tc.attempts, now)
		if failure.Permanent != tc.permanent || (!failure.Permanent && failure.RetryAt.Sub(now) != tc.wantDelay) {
			t.Fatalf("attempt=%d failure=%+v", tc.attempts, failure)
		}
	}
	failure := classifyFailure(&ProcessingError{Category: "malware", Permanent: true, Rejected: true}, 1, now)
	if !failure.Permanent || !failure.Rejected || failure.Category != "malware" || !failure.RetryAt.IsZero() {
		t.Fatalf("permanent failure=%+v", failure)
	}
}

func TestWorkerNeverProcessesTwoJobsConcurrently(t *testing.T) {
	store := &sequenceStore{jobs: []Job{{ID: uuid.New(), FileVersionID: uuid.New(), Kind: KindProcessFile, Attempts: 1}, {ID: uuid.New(), FileVersionID: uuid.New(), Kind: KindProcessFile, Attempts: 1}}}
	var active, maximum atomic.Int32
	processor := processorFunc(func(context.Context, Job) (Result, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		return Result{DetectedMIME: "application/pdf", ScanResult: "clean"}, nil
	})
	w := Worker{Store: store, Processor: processor, Owner: "one", Now: time.Now}
	for range 2 {
		if worked, err := w.RunOne(context.Background()); err != nil || !worked {
			t.Fatalf("worked=%t err=%v", worked, err)
		}
	}
	if maximum.Load() != 1 || store.completed != 2 {
		t.Fatalf("maximum=%d completed=%d", maximum.Load(), store.completed)
	}
}

func TestWorkerCancellationDoesNotCompleteOrFail(t *testing.T) {
	job := Job{ID: uuid.New(), FileVersionID: uuid.New(), Kind: KindProcessFile, Attempts: 1}
	store := &sequenceStore{jobs: []Job{job}}
	started := make(chan struct{})
	processor := processorFunc(func(ctx context.Context, _ Job) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (&Worker{Store: store, Processor: processor, Owner: "owner", HeartbeatInterval: time.Hour}).RunOne(ctx)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err != nil || store.completed != 0 || len(store.failures) != 0 {
		t.Fatalf("err=%v completed=%d failures=%d", err, store.completed, len(store.failures))
	}
}

func TestWorkerLostHeartbeatCancelsAndCannotWrite(t *testing.T) {
	job := Job{ID: uuid.New(), FileVersionID: uuid.New(), Kind: KindProcessFile, Attempts: 1}
	store := &sequenceStore{jobs: []Job{job}, heartbeatErr: ErrLeaseLost}
	processor := processorFunc(func(ctx context.Context, _ Job) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	_, err := (&Worker{Store: store, Processor: processor, Owner: "owner", HeartbeatInterval: time.Millisecond}).RunOne(context.Background())
	if !errors.Is(err, ErrLeaseLost) || store.completed != 0 || len(store.failures) != 0 {
		t.Fatalf("err=%v completed=%d failures=%d", err, store.completed, len(store.failures))
	}
}

func TestWorkerUsesPerKindDeadlineAndBoundedPolling(t *testing.T) {
	now := time.Date(2026, 7, 21, 2, 0, 0, 0, time.UTC)
	job := Job{ID: uuid.New(), FileVersionID: uuid.New(), Kind: KindProcessFile, Attempts: 1}
	store := &sequenceStore{jobs: []Job{job}}
	processor := processorFunc(func(ctx context.Context, _ Job) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	worker := &Worker{Store: store, Processor: processor, Owner: "deadline-owner", Now: func() time.Time { return now }, HeartbeatInterval: time.Hour, Deadlines: map[string]time.Duration{KindProcessFile: 5 * time.Millisecond}}
	if worked, err := worker.RunOne(context.Background()); err != nil || !worked {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
	if len(store.failures) != 1 || store.failures[0].Permanent || store.failures[0].RetryAt.Sub(now) != time.Minute {
		t.Fatalf("failures=%+v", store.failures)
	}
	worker.IdleWait = 10 * time.Second
	if worker.idleWait() != DefaultIdleWait || DefaultIdleWait != 2*time.Second || DefaultLeaseDuration != 30*time.Second || DefaultHeartbeatInterval != 10*time.Second {
		t.Fatalf("idle=%s lease=%s heartbeat=%s", worker.idleWait(), DefaultLeaseDuration, DefaultHeartbeatInterval)
	}
}

type processorFunc func(context.Context, Job) (Result, error)

func (f processorFunc) Process(ctx context.Context, job Job) (Result, error) { return f(ctx, job) }

type sequenceStore struct {
	mu           sync.Mutex
	jobs         []Job
	completed    int
	failures     []Failure
	heartbeatErr error
}

func (s *sequenceStore) LeaseNext(_ context.Context, owner string, now time.Time, lease time.Duration) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		return Job{}, ErrNoJob
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	job.LeaseOwner, job.LeaseUntil = owner, now.Add(lease)
	return job, nil
}
func (s *sequenceStore) Heartbeat(context.Context, uuid.UUID, string, time.Time) error {
	return s.heartbeatErr
}
func (s *sequenceStore) Complete(context.Context, Job, Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed++
	return nil
}
func (s *sequenceStore) Fail(_ context.Context, _ Job, failure Failure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, failure)
	return nil
}
