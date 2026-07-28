package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type runnerStore struct {
	mu                         sync.Mutex
	events                     []OutboxEvent
	deliverErr                 error
	afterClaim                 func()
	claimed, delivered, failed int
	lastCategory               string
	lastPermanent              bool
}

func (s *runnerStore) Claim(context.Context, string) ([]OutboxEvent, error) {
	s.mu.Lock()
	s.claimed++
	events := append([]OutboxEvent(nil), s.events...)
	afterClaim := s.afterClaim
	s.mu.Unlock()
	if afterClaim != nil {
		afterClaim()
	}
	return events, nil
}

type notificationClaimGate struct {
	mu      sync.Mutex
	allowed bool
	err     error
	calls   int
}

func (g *notificationClaimGate) ClaimsAllowed(context.Context) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.allowed, g.err
}

func TestOperationalGateBlocksNewNotificationClaimsButLetsClaimedBatchSettle(t *testing.T) {
	event := OutboxEvent{ID: uuid.New(), Kind: "lesson.published", Payload: json.RawMessage(`{}`)}
	for _, tc := range []struct {
		name string
		gate *notificationClaimGate
		log  string
	}{
		{name: "maintenance", gate: &notificationClaimGate{allowed: false}},
		{name: "gate error", gate: &notificationClaimGate{err: errors.New("secret database detail")}, log: "operational_gate_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &runnerStore{events: []OutboxEvent{event}}
			var categories []string
			runner := Runner{Store: store, Owner: "worker", ClaimGate: tc.gate, LogCategory: func(category string) {
				categories = append(categories, category)
			}}
			if err := runner.ProcessBatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if store.claimed != 0 || store.delivered != 0 || tc.gate.calls != 1 {
				t.Fatalf("claimed=%d delivered=%d gate_calls=%d", store.claimed, store.delivered, tc.gate.calls)
			}
			if got := strings.Join(categories, ","); got != tc.log {
				t.Fatalf("log=%q want=%q", got, tc.log)
			}
		})
	}

	gate := &notificationClaimGate{allowed: true}
	store := &runnerStore{events: []OutboxEvent{event}, afterClaim: func() {
		gate.mu.Lock()
		gate.allowed = false
		gate.mu.Unlock()
	}}
	runner := Runner{Store: store, Owner: "worker", ClaimGate: gate}
	if err := runner.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.claimed != 1 || store.delivered != 1 || gate.calls != 1 {
		t.Fatalf("claimed=%d delivered=%d gate_calls=%d", store.claimed, store.delivered, gate.calls)
	}
}
func (s *runnerStore) DeliverLessonPublication(context.Context, OutboxEvent, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered++
	return s.deliverErr
}
func (s *runnerStore) Complete(context.Context, uuid.UUID, string) error { return nil }
func (s *runnerStore) Fail(_ context.Context, _ uuid.UUID, _ string, category string, permanent bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed++
	s.lastCategory, s.lastPermanent = category, permanent
	return nil
}

func TestRunnerCategorizesPermanentAndTransientFailures(t *testing.T) {
	event := OutboxEvent{ID: uuid.New(), Kind: "lesson.published", Payload: json.RawMessage(`{}`)}
	for _, tc := range []struct {
		name      string
		err       error
		category  string
		permanent bool
	}{
		{"malformed", permanentOutboxError("payload_invalid"), "payload_invalid", true},
		{"database", errors.New("secret database detail"), "delivery_failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &runnerStore{events: []OutboxEvent{event}, deliverErr: tc.err}
			r := Runner{Store: store, Owner: "worker", BatchTimeout: time.Second}
			if err := r.ProcessBatch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if store.failed != 1 || store.lastCategory != tc.category || store.lastPermanent != tc.permanent {
				t.Fatalf("store=%+v", store)
			}
		})
	}
}

func TestStartOutboxRunnerStopsPromptlyWhileTimerIsWaiting(t *testing.T) {
	store := &runnerStore{}
	stop := StartOutboxRunner(Runner{Store: store, Owner: "worker", PollInterval: time.Hour, BatchTimeout: time.Second, ShutdownTimeout: time.Second})
	deadline := time.Now().Add(time.Second)
	for {
		store.mu.Lock()
		claimed := store.claimed
		store.mu.Unlock()
		if claimed > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner did not start")
		}
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	stop()
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("shutdown took %s", elapsed)
	}
}
