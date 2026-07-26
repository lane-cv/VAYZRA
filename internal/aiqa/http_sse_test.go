package aiqa

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type streamStoreStub struct {
	mu          sync.Mutex
	state       RunStreamState
	events      []RunEvent
	stateCalls  int
	principals  []Principal
	eventsAfter []int64
	eventsUntil []int64
	onList      func()
	err         error
}

func (s *streamStoreStub) RunStreamState(_ context.Context, principal Principal, _ uuid.UUID) (RunStreamState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateCalls++
	s.principals = append(s.principals, principal)
	return s.state, s.err
}
func (s *streamStoreStub) ListRunEvents(_ context.Context, _ Principal, _ uuid.UUID, after, through int64, _ int) ([]RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventsAfter = append(s.eventsAfter, after)
	s.eventsUntil = append(s.eventsUntil, through)
	if s.onList != nil {
		s.onList()
	}
	out := make([]RunEvent, 0)
	for _, event := range s.events {
		if event.Sequence > after && event.Sequence <= through {
			out = append(out, event)
		}
	}
	return out, s.err
}

type oneWait struct{ calls int }

func (w *oneWait) Wait(ctx context.Context, _ time.Duration) error {
	w.calls++
	return context.Canceled
}

type continueOnceWait struct{ calls int }

func (w *continueOnceWait) Wait(context.Context, time.Duration) error {
	w.calls++
	if w.calls == 1 {
		return nil
	}
	return context.Canceled
}

type revokeSessionWait struct {
	store *streamStoreStub
	once  sync.Once
}

func (w *revokeSessionWait) Wait(context.Context, time.Duration) error {
	w.once.Do(func() {
		w.store.mu.Lock()
		w.store.events = append(w.store.events, RunEvent{Sequence: 2, Kind: "delta", Delta: "must-not-emit"})
		w.store.state = RunStreamState{Status: RunStreaming, LastSequence: 2}
		w.store.err = ErrNotFound
		w.store.mu.Unlock()
	})
	return nil
}

type delayedTerminalWait struct {
	store *streamStoreStub
	once  sync.Once
}

type blockingEventWait struct {
	started chan struct{}
	once    sync.Once
}

type heartbeatThenTerminalWait struct {
	store *streamStoreStub
	calls int
}

func (w *heartbeatThenTerminalWait) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Millisecond):
	}
	w.calls++
	if w.calls == 2 {
		w.store.mu.Lock()
		w.store.state = RunStreamState{Status: RunSucceeded, LastSequence: 1}
		w.store.events = []RunEvent{{Sequence: 1, Kind: "completed"}}
		w.store.mu.Unlock()
	}
	return nil
}

func (w *blockingEventWait) Wait(ctx context.Context, _ time.Duration) error {
	w.once.Do(func() { close(w.started) })
	<-ctx.Done()
	return ctx.Err()
}

func (w *delayedTerminalWait) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Millisecond):
	}
	w.once.Do(func() {
		w.store.mu.Lock()
		w.store.state = RunStreamState{Status: RunSucceeded, LastSequence: 1}
		w.store.events = []RunEvent{{Sequence: 1, Kind: "completed"}}
		w.store.mu.Unlock()
	})
	return nil
}

func TestSSEReplaysPersistedEventsAndClosesOnTerminal(t *testing.T) {
	store := &streamStoreStub{
		state: RunStreamState{Status: RunSucceeded, LastSequence: 4},
		events: []RunEvent{
			{Sequence: 1, Kind: "delta", Delta: "你"},
			{Sequence: 2, Kind: "delta", Delta: "好"},
			{Sequence: 3, Kind: "usage", Delta: `{"private":"checkpoint"}`},
			{Sequence: 4, Kind: "completed"},
		},
	}
	h := NewStudentHandler(&studentHTTPStub{}, store)
	h.sessionID = func(context.Context) (uuid.UUID, bool) {
		return uuid.MustParse("20000000-0000-4000-8000-000000000001"), true
	}
	r := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers=%v", w.Header())
	}
	body := w.Body.String()
	for _, want := range []string{"id: 1\n", "id: 2\n", "id: 3\n", "id: 4\n", `"sequence":1`, `"kind":"delta"`, `"sequence":3,"kind":"status","status":"streaming"`, `"status":"succeeded"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
	if strings.Contains(body, "provider") || strings.Contains(body, "model") || strings.Contains(body, "checkpoint") {
		t.Fatalf("snapshot leak: %s", body)
	}
}

func TestSSEConcurrentAppendAfterStateSnapshotContinuesOnNextPoll(t *testing.T) {
	store := &streamStoreStub{
		state:  RunStreamState{Status: RunStreaming, LastSequence: 1},
		events: []RunEvent{{Sequence: 1, Kind: "delta", Delta: "a"}},
	}
	var appendOnce sync.Once
	store.onList = func() {
		appendOnce.Do(func() {
			store.events = append(store.events, RunEvent{Sequence: 2, Kind: "completed"})
			store.state = RunStreamState{Status: RunSucceeded, LastSequence: 2}
		})
	}
	h := NewStudentHandler(&studentHTTPStub{}, store)
	h.waiter = &continueOnceWait{}
	h.sessionID = func(context.Context) (uuid.UUID, bool) { return uuid.New(), true }
	r := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	result := httptest.NewRecorder()
	h.Routes().ServeHTTP(result, r)
	body := result.Body.String()
	if !strings.Contains(body, "id: 1\n") || !strings.Contains(body, "id: 2\n") || !strings.Contains(body, `"status":"succeeded"`) {
		t.Fatalf("stream closed on concurrent append: %q", body)
	}
	if len(store.eventsUntil) < 2 || store.eventsUntil[0] != 1 || store.eventsUntil[1] != 2 {
		t.Fatalf("snapshot bounds=%v", store.eventsUntil)
	}
}

func TestSSELastEventIDAndAfterSequenceAreStrict(t *testing.T) {
	tests := []struct {
		name, header, query string
		wantStatus          int
		wantAfter           int64
	}{
		{"header replay", "2", "", 200, 2},
		{"query replay", "", "?afterSequence=2", 200, 2},
		{"negative", "-1", "", 400, 0},
		{"noncanonical", "02", "", 400, 0},
		{"malformed", "two", "", 400, 0},
		{"empty header", " ", "", 400, 0},
		{"empty query", "", "?afterSequence=", 400, 0},
		{"future", "4", "", 400, 0},
		{"ambiguous", "2", "?afterSequence=2", 400, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &streamStoreStub{state: RunStreamState{Status: RunSucceeded, LastSequence: 3}, events: []RunEvent{{Sequence: 3, Kind: "completed"}}}
			h := NewStudentHandler(&studentHTTPStub{}, store)
			h.sessionID = func(context.Context) (uuid.UUID, bool) { return uuid.New(), true }
			r := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events"+tc.query, nil)
			if tc.header != "" {
				r.Header.Set("Last-Event-ID", tc.header)
			}
			r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.wantStatus == 200 && (len(store.eventsAfter) == 0 || store.eventsAfter[0] != tc.wantAfter) {
				t.Fatalf("after=%v", store.eventsAfter)
			}
		})
	}
}

func TestSSEDuplicateHeaderAndOwnershipRecheck(t *testing.T) {
	store := &streamStoreStub{state: RunStreamState{Status: RunStreaming, LastSequence: 0}}
	waiter := &oneWait{}
	h := NewStudentHandler(&studentHTTPStub{}, store)
	h.waiter = waiter
	h.heartbeatInterval = time.Hour
	h.sessionID = func(context.Context) (uuid.UUID, bool) { return uuid.New(), true }

	r := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	r.Header.Add("Last-Event-ID", "0")
	r.Header.Add("Last-Event-ID", "0")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || store.stateCalls != 0 {
		t.Fatalf("duplicate status=%d calls=%d", w.Code, store.stateCalls)
	}

	r = httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if store.stateCalls < 2 {
		t.Fatalf("ownership was not rechecked: calls=%d", store.stateCalls)
	}
	if waiter.calls != 1 {
		t.Fatalf("wait calls=%d", waiter.calls)
	}
}

func TestSSEDisconnectReturnsWithoutCancellingRun(t *testing.T) {
	store := &streamStoreStub{state: RunStreamState{Status: RunStreaming}}
	waiter := &blockingEventWait{started: make(chan struct{})}
	service := &studentHTTPStub{}
	h := NewStudentHandler(service, store)
	h.waiter = waiter
	h.sessionID = func(context.Context) (uuid.UUID, bool) { return uuid.New(), true }
	requestCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	request = request.WithContext(auth.ContextWithUser(requestCtx, auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	done := make(chan struct{})
	go func() {
		h.Routes().ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	<-waiter.started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream handler did not return after disconnect")
	}
	if service.cancelled != 0 {
		t.Fatalf("disconnect cancelled run %d times", service.cancelled)
	}
}

func TestSSESessionRevocationBetweenPollsClosesBeforeLaterEvents(t *testing.T) {
	store := &streamStoreStub{
		state:  RunStreamState{Status: RunStreaming, LastSequence: 1},
		events: []RunEvent{{Sequence: 1, Kind: "delta", Delta: "owned"}},
	}
	h := NewStudentHandler(&studentHTTPStub{}, store)
	h.waiter = &revokeSessionWait{store: store}
	sessionID := uuid.MustParse("20000000-0000-4000-8000-000000000099")
	h.sessionID = func(context.Context) (uuid.UUID, bool) { return sessionID, true }
	r := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	result := httptest.NewRecorder()
	h.Routes().ServeHTTP(result, r)
	body := result.Body.String()
	if !strings.Contains(body, "id: 1\n") || strings.Contains(body, "id: 2\n") || strings.Contains(body, "must-not-emit") {
		t.Fatalf("revoked stream body=%q", body)
	}
	if store.stateCalls < 3 {
		t.Fatalf("authorization was not rechecked after revocation: %d", store.stateCalls)
	}
	for _, principal := range store.principals {
		if principal.User.ID != studentHTTPUserID || principal.SessionID != sessionID {
			t.Fatalf("principal recheck=%+v", principal)
		}
	}
}

func TestSSERefreshesWriteDeadlineBeyondOrdinaryServerTimeout(t *testing.T) {
	store := &streamStoreStub{state: RunStreamState{Status: RunStreaming}}
	h := NewStudentHandler(&studentHTTPStub{}, store)
	h.waiter = &delayedTerminalWait{store: store}
	h.pollInterval = time.Millisecond
	h.heartbeatInterval = time.Hour
	h.writeTimeout = 100 * time.Millisecond
	h.sessionID = func(context.Context) (uuid.UUID, bool) { return uuid.New(), true }
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive})
		h.Routes().ServeHTTP(w, r.WithContext(ctx))
	})
	server := httptest.NewUnstartedServer(wrapped)
	server.Config.WriteTimeout = 15 * time.Millisecond
	server.Start()
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/runs/" + studentHTTPRunID.String() + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body strings.Builder
	if _, err = io.Copy(&body, response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), `"status":"succeeded"`) {
		t.Fatalf("status=%d body=%q", response.StatusCode, body.String())
	}
}

func TestSSEAllowsOnlyOneConnectionPerSessionAndRun(t *testing.T) {
	store := &streamStoreStub{state: RunStreamState{Status: RunStreaming}}
	waiter := &blockingEventWait{started: make(chan struct{})}
	h := NewStudentHandler(&studentHTTPStub{}, store)
	h.waiter = waiter
	sessionID := uuid.New()
	h.sessionID = func(context.Context) (uuid.UUID, bool) { return sessionID, true }
	userContext := func(ctx context.Context) context.Context {
		return auth.ContextWithUser(ctx, auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive})
	}
	firstCtx, cancel := context.WithCancel(context.Background())
	first := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil).WithContext(userContext(firstCtx))
	firstDone := make(chan struct{})
	go func() {
		h.Routes().ServeHTTP(httptest.NewRecorder(), first)
		close(firstDone)
	}()
	<-waiter.started

	second := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	second = second.WithContext(userContext(second.Context()))
	result := httptest.NewRecorder()
	h.Routes().ServeHTTP(result, second)
	if result.Code != http.StatusConflict || !strings.Contains(result.Body.String(), `"code":"AI_STREAM_BUSY"`) || !strings.Contains(result.Body.String(), `"requestId"`) {
		t.Fatalf("status=%d body=%s", result.Code, result.Body)
	}
	cancel()
	<-firstDone
}

func TestSSEHeartbeatCommentsNeverAdvancePersistedSequence(t *testing.T) {
	store := &streamStoreStub{state: RunStreamState{Status: RunStreaming}}
	h := NewStudentHandler(&studentHTTPStub{}, store)
	h.waiter = &heartbeatThenTerminalWait{store: store}
	h.heartbeatInterval = time.Millisecond
	h.sessionID = func(context.Context) (uuid.UUID, bool) { return uuid.New(), true }
	r := httptest.NewRequest(http.MethodGet, "/runs/"+studentHTTPRunID.String()+"/events", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	result := httptest.NewRecorder()
	h.Routes().ServeHTTP(result, r)
	body := result.Body.String()
	if !strings.Contains(body, ": heartbeat\n\n") || strings.Count(body, "id: ") != 1 || !strings.Contains(body, "id: 1\n") {
		t.Fatalf("body=%q", body)
	}
}
