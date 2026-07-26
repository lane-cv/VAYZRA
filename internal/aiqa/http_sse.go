package aiqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"happylearn.local/app/internal/platform/httpx"
)

type RunStreamState struct {
	Status       RunStatus
	LastSequence int64
}

type StudentEventStore interface {
	RunStreamState(context.Context, Principal, uuid.UUID) (RunStreamState, error)
	ListRunEvents(context.Context, Principal, uuid.UUID, int64, int) ([]RunEvent, error)
}

type EventWaiter interface {
	Wait(context.Context, time.Duration) error
}

type timerEventWaiter struct{}

func (timerEventWaiter) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type StreamEventDTO struct {
	Sequence  int64     `json:"sequence"`
	Kind      string    `json:"kind"`
	Delta     string    `json:"delta,omitempty"`
	Status    RunStatus `json:"status,omitempty"`
	ErrorCode string    `json:"errorCode,omitempty"`
}

type streamConnectionKey struct {
	sessionID, runID uuid.UUID
}
type streamConnections struct {
	mu     sync.Mutex
	active map[streamConnectionKey]struct{}
}

func newStreamConnections() *streamConnections {
	return &streamConnections{active: make(map[streamConnectionKey]struct{})}
}
func (c *streamConnections) acquire(key streamConnectionKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.active[key]; exists {
		return false
	}
	c.active[key] = struct{}{}
	return true
}
func (c *streamConnections) release(key streamConnectionKey) {
	c.mu.Lock()
	delete(c.active, key)
	c.mu.Unlock()
}

func (h *StudentHandler) eventsStream(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		studentAIError(w, r, ErrNotFound)
		return
	}
	runID, err := studentAICanonicalUUID(chi.URLParam(r, "runId"))
	if err != nil {
		studentAIError(w, r, ErrNotFound)
		return
	}
	after, err := streamAfterSequence(r)
	if err != nil {
		studentAIInvalid(w, r)
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	sessionID, ok := h.sessionID(r.Context())
	if !ok {
		studentAIError(w, r, ErrNotFound)
		return
	}
	actor.SessionID = sessionID
	state, err := h.events.RunStreamState(r.Context(), actor, runID)
	if err != nil {
		studentAIError(w, r, err)
		return
	}
	if after > state.LastSequence {
		studentAIInvalid(w, r)
		return
	}
	key := streamConnectionKey{sessionID: sessionID, runID: runID}
	if !h.connections.acquire(key) {
		httpx.Error(w, r, http.StatusConflict, "AI_STREAM_BUSY", "该运行已有活动订阅")
		return
	}
	defer h.connections.release(key)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	lastWrite := time.Now()
	for {
		// Recheck both the active authenticated session and current run ownership
		// before every database poll. A disconnected client only cancels this
		// subscription context; it never calls the run cancellation service.
		state, err = h.events.RunStreamState(r.Context(), actor, runID)
		if err != nil {
			return
		}
		events, err := h.events.ListRunEvents(r.Context(), actor, runID, after, 128)
		if err != nil {
			return
		}
		for _, event := range events {
			if event.Sequence <= after || event.Sequence > state.LastSequence {
				return
			}
			dto := streamEventView(event, state.Status)
			raw, marshalErr := json.Marshal(dto)
			if marshalErr != nil {
				return
			}
			if err = setBoundedWriteDeadline(controller, h.writeTimeout); err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", event.Sequence, raw); err != nil {
				return
			}
			after = event.Sequence
			lastWrite = time.Now()
		}
		if len(events) > 0 && flusher != nil {
			flusher.Flush()
		}
		if terminalRunStatus(state.Status) && after >= state.LastSequence {
			_ = controller.SetWriteDeadline(time.Time{})
			return
		}
		if time.Since(lastWrite) >= h.heartbeatInterval {
			if err = setBoundedWriteDeadline(controller, h.writeTimeout); err != nil {
				return
			}
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			lastWrite = time.Now()
		}
		if err = h.waiter.Wait(r.Context(), h.pollInterval); err != nil {
			_ = controller.SetWriteDeadline(time.Time{})
			return
		}
	}
}

func streamAfterSequence(r *http.Request) (int64, error) {
	headers := r.Header.Values("Last-Event-ID")
	if len(headers) > 1 {
		return 0, ErrInvalidInput
	}
	query, err := studentAIQuery(r, "afterSequence")
	if err != nil {
		return 0, err
	}
	rawQuery, queryPresent := "", false
	if values, exists := query["afterSequence"]; exists {
		queryPresent = true
		rawQuery = values[0]
	}
	if len(headers) == 1 && queryPresent {
		return 0, ErrInvalidInput
	}
	raw := ""
	if len(headers) == 1 {
		raw = headers[0]
	} else if queryPresent {
		raw = rawQuery
	}
	if raw == "" {
		if len(headers) == 1 || queryPresent {
			return 0, ErrInvalidInput
		}
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || strconv.FormatInt(value, 10) != raw {
		return 0, ErrInvalidInput
	}
	return value, nil
}

func streamEventView(event RunEvent, _ RunStatus) StreamEventDTO {
	dto := StreamEventDTO{Sequence: event.Sequence}
	switch event.Kind {
	case "delta":
		dto.Kind, dto.Delta = "delta", event.Delta
	case "failed":
		dto.Kind, dto.Status, dto.ErrorCode = "error", RunFailed, event.ErrorCode
	case "cancelled":
		dto.Kind, dto.Status, dto.ErrorCode = "status", RunCancelled, event.ErrorCode
	case "completed":
		dto.Kind, dto.Status = "status", RunSucceeded
	case "usage":
		// Usage checkpoints are persisted runner facts. Expose only a safe status
		// marker, never their raw payload or a premature terminal status.
		dto.Kind, dto.Status = "status", RunStreaming
	default:
		dto.Kind = "error"
		dto.ErrorCode = "RUN_FAILED"
	}
	return dto
}

func terminalRunStatus(status RunStatus) bool {
	return status == RunSucceeded || status == RunFailed || status == RunCancelled
}

func setBoundedWriteDeadline(controller *http.ResponseController, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	err := controller.SetWriteDeadline(time.Now().Add(timeout))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *PostgresRuntimeStore) RunStreamState(ctx context.Context, actor Principal, runID uuid.UUID) (RunStreamState, error) {
	if s == nil || s.pool == nil || actor.User.ID == uuid.Nil || actor.SessionID == uuid.Nil || runID == uuid.Nil {
		return RunStreamState{}, ErrNotFound
	}
	var out RunStreamState
	err := s.pool.QueryRow(ctx, `
SELECT r.status,r.last_sequence
FROM ai_runs r
JOIN ai_threads t ON t.id=r.thread_id AND t.student_id=$1
JOIN users u ON u.id=t.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
JOIN sessions sess ON sess.id=$3 AND sess.user_id=u.id AND sess.revoked_at IS NULL
 AND sess.idle_expires_at>now() AND sess.absolute_expires_at>now()
WHERE r.id=$2 AND r.student_id=$1`, actor.User.ID, runID, actor.SessionID).Scan(&out.Status, &out.LastSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunStreamState{}, ErrNotFound
	}
	return out, err
}

func (s *PostgresRuntimeStore) ListRunEvents(ctx context.Context, actor Principal, runID uuid.UUID, after int64, limit int) ([]RunEvent, error) {
	if s == nil || s.pool == nil || actor.User.ID == uuid.Nil || actor.SessionID == uuid.Nil || runID == uuid.Nil || after < 0 {
		return nil, ErrNotFound
	}
	if limit < 1 || limit > 128 {
		limit = 128
	}
	rows, err := s.pool.Query(ctx, `
SELECT e.sequence,e.kind,e.payload_text,coalesce(e.error_code,''),e.created_at
FROM ai_run_events e
JOIN ai_runs r ON r.id=e.run_id AND r.student_id=$1
JOIN ai_threads t ON t.id=r.thread_id AND t.student_id=$1
JOIN users u ON u.id=t.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
JOIN sessions sess ON sess.id=$3 AND sess.user_id=u.id AND sess.revoked_at IS NULL
 AND sess.idle_expires_at>now() AND sess.absolute_expires_at>now()
WHERE e.run_id=$2 AND e.sequence>$4
ORDER BY e.sequence LIMIT $5`, actor.User.ID, runID, actor.SessionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RunEvent, 0)
	for rows.Next() {
		var event RunEvent
		if err = rows.Scan(&event.Sequence, &event.Kind, &event.Delta, &event.ErrorCode, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

var _ StudentEventStore = (*PostgresRuntimeStore)(nil)
