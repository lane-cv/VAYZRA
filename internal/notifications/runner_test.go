package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type runnerStore struct {
	mu                         sync.Mutex
	events                     []OutboxEvent
	deliverErr                 error
	claimed, delivered, failed int
	lastCategory               string
	lastPermanent              bool
}

func (s *runnerStore) Claim(context.Context, string) ([]OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed++
	return append([]OutboxEvent(nil), s.events...), nil
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
