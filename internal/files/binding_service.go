package files

import (
	"context"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"strings"
	"unicode"
	"unicode/utf8"
)

type BindingService struct{ store BindingStore }

func NewBindingService(store BindingStore) *BindingService { return &BindingService{store: store} }
func (s *BindingService) Replace(ctx context.Context, actor Principal, lessonID uuid.UUID, expected int64, in []DraftBindingInput) ([]DraftBinding, error) {
	if actor.User.Role != auth.RoleAdmin || actor.User.Status != auth.StatusActive {
		return nil, ErrForbidden
	}
	if lessonID == uuid.Nil || expected < 1 || len(in) > 100 {
		return nil, ErrInvalid
	}
	versions := map[uuid.UUID]bool{}
	positions := map[int64]bool{}
	for i := range in {
		in[i].DisplayName = strings.TrimSpace(in[i].DisplayName)
		in[i].Description = strings.TrimSpace(in[i].Description)
		if in[i].FileVersionID == uuid.Nil || (in[i].Policy != PolicyPreview && in[i].Policy != PolicyDownload) || in[i].SortPosition < 0 || versions[in[i].FileVersionID] || positions[in[i].SortPosition] || !safeBindingText(in[i].DisplayName, 255, false) || !safeBindingText(in[i].Description, 500, true) {
			return nil, ErrInvalid
		}
		versions[in[i].FileVersionID] = true
		positions[in[i].SortPosition] = true
	}
	return s.store.ReplaceDraftBindings(ctx, actor, lessonID, expected, in)
}
func safeBindingText(v string, max int, optional bool) bool {
	if !utf8.ValidString(v) || (v == "" && !optional) || utf8.RuneCountInString(v) > max {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}
