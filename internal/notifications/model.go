package notifications

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidInput = errors.New("invalid notification input")
	ErrNotFound     = errors.New("notification not found")
)

type Kind string

const (
	KindQACreated       Kind = "qa_created"
	KindQAReplied       Kind = "qa_replied"
	KindQAFollowedUp    Kind = "qa_followed_up"
	KindQAStatusChanged Kind = "qa_status_changed"
	KindLessonPublished Kind = "lesson_published"
)

type Notification struct {
	ID         uuid.UUID  `json:"id"`
	Kind       Kind       `json:"kind"`
	Title      string     `json:"title"`
	Summary    string     `json:"summary"`
	TargetType string     `json:"targetType"`
	TargetID   uuid.UUID  `json:"targetId"`
	TargetPath string     `json:"targetPath"`
	ReadAt     *time.Time `json:"readAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
	Limit     int
}
