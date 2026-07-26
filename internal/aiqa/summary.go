package aiqa

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

type QuestionSummary struct {
	ID            uuid.UUID
	Channel       string
	Title         string
	RawStatus     string
	LastMessageAt time.Time
	CreatedAt     time.Time
}

type SummaryCursor struct {
	LastMessageAt time.Time
	Channel       string
	ID            uuid.UUID
}

type SummaryFilter struct {
	Channel string
	Search  string
	Cursor  SummaryCursor
	Limit   int
}

type SummaryStore interface {
	ListQuestionSummaries(context.Context, uuid.UUID, SummaryFilter) ([]QuestionSummary, SummaryCursor, error)
}

type SummaryService interface {
	ListQuestionSummaries(context.Context, Principal, SummaryFilter) ([]QuestionSummary, SummaryCursor, error)
}

type summaryService struct{ store SummaryStore }

func NewSummaryService(store SummaryStore) SummaryService { return &summaryService{store: store} }

func (s *summaryService) ListQuestionSummaries(ctx context.Context, p Principal, filter SummaryFilter) ([]QuestionSummary, SummaryCursor, error) {
	if !studentOK(p) {
		return nil, SummaryCursor{}, ErrNotFound
	}
	filter.Channel = strings.TrimSpace(filter.Channel)
	filter.Search = strings.TrimSpace(filter.Search)
	if err := validateSummaryFilter(filter); err != nil {
		return nil, SummaryCursor{}, err
	}
	return s.store.ListQuestionSummaries(ctx, p.User.ID, filter)
}

func validateSummaryFilter(filter SummaryFilter) error {
	if filter.Channel != "" && filter.Channel != "ai" && filter.Channel != "teacher" {
		return ErrInvalidInput
	}
	if filter.Limit < 1 || filter.Limit > maxStudentAIPage || len([]rune(filter.Search)) > 160 {
		return ErrInvalidInput
	}
	c := filter.Cursor
	empty := c.LastMessageAt.IsZero() && c.Channel == "" && c.ID == uuid.Nil
	complete := !c.LastMessageAt.IsZero() && (c.Channel == "ai" || c.Channel == "teacher") && c.ID != uuid.Nil
	if !empty && !complete {
		return ErrInvalidInput
	}
	return nil
}

type summaryCursorWire struct {
	LastMessageAt string `json:"lastMessageAt"`
	Channel       string `json:"channel"`
	ID            string `json:"id"`
}

func encodeSummaryCursor(v SummaryCursor) string {
	if v.ID == uuid.Nil || v.LastMessageAt.IsZero() || v.Channel == "" {
		return ""
	}
	return encodeAICursor(summaryCursorWire{
		LastMessageAt: v.LastMessageAt.UTC().Format(time.RFC3339Nano),
		Channel:       v.Channel,
		ID:            v.ID.String(),
	})
}

func decodeSummaryCursor(raw string, now time.Time) (SummaryCursor, error) {
	if raw == "" {
		return SummaryCursor{}, nil
	}
	var wire summaryCursorWire
	if err := decodeAICursor(raw, &wire); err != nil || wire.Channel != "ai" && wire.Channel != "teacher" {
		return SummaryCursor{}, ErrInvalidInput
	}
	at, id, err := aiCursorParts(wire.LastMessageAt, wire.ID, now)
	if err != nil {
		return SummaryCursor{}, ErrInvalidInput
	}
	out := SummaryCursor{LastMessageAt: at, Channel: wire.Channel, ID: id}
	if encodeSummaryCursor(out) != raw {
		return SummaryCursor{}, ErrInvalidInput
	}
	return out, nil
}
