package aiqa

import (
	"context"
	"errors"
	"io"

	"github.com/google/uuid"
)

const (
	MaxAIAttachments       = 20
	MaxAttachmentTextBytes = 2 << 20
)

var ErrAttachmentNotReady = errors.New("ai attachment not ready")

type AttachmentInput struct {
	FileVersionID uuid.UUID
	SortPosition  int
}

type AttachmentMetadata struct {
	FileVersionID uuid.UUID
	DisplayName   string
	DetectedMIME  string
	Modality      Modality
	Size          int64
}

type AttachmentContext struct {
	FileVersionID uuid.UUID
	DisplayName   string
	DetectedMIME  string
	Kind          Modality
	Text          string
	OpenImage     func(context.Context) (io.ReadCloser, error)
	Size          int64
}

type AttachmentContextStore interface {
	ValidateForAI(context.Context, uuid.UUID, uuid.UUID, []AttachmentInput) ([]AttachmentMetadata, error)
	LoadAIText(context.Context, uuid.UUID, uuid.UUID) (string, error)
	OpenAIImage(context.Context, uuid.UUID, uuid.UUID) (io.ReadCloser, string, int64, error)
}

func validateAttachmentInputs(inputs []AttachmentInput) error {
	if len(inputs) > MaxAIAttachments {
		return ErrInvalidInput
	}
	ids := make(map[uuid.UUID]struct{}, len(inputs))
	positions := make(map[int]struct{}, len(inputs))
	for _, input := range inputs {
		if input.FileVersionID == uuid.Nil || input.SortPosition < 0 || input.SortPosition >= MaxAIAttachments {
			return ErrInvalidInput
		}
		if _, exists := ids[input.FileVersionID]; exists {
			return ErrInvalidInput
		}
		if _, exists := positions[input.SortPosition]; exists {
			return ErrInvalidInput
		}
		ids[input.FileVersionID] = struct{}{}
		positions[input.SortPosition] = struct{}{}
	}
	return nil
}
