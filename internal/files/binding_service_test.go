package files

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"testing"
)

type bindingStoreStub struct {
	calls int
	items []DraftBindingInput
}

func (s *bindingStoreStub) ReplaceDraftBindings(_ context.Context, _ Principal, lesson uuid.UUID, _ int64, in []DraftBindingInput) ([]DraftBinding, error) {
	s.calls++
	s.items = in
	return []DraftBinding{{LessonID: lesson, DraftBindingInput: in[0]}}, nil
}
func TestBindingValidationAndExactVersion(t *testing.T) {
	store := &bindingStoreStub{}
	svc := NewBindingService(store)
	actor := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}
	lesson := uuid.New()
	version := uuid.New()
	out, err := svc.Replace(context.Background(), actor, lesson, 7, []DraftBindingInput{{FileVersionID: version, Policy: PolicyPreview, DisplayName: " 讲义.pdf ", Description: " 第一章 ", SortPosition: 10}})
	if err != nil || len(out) != 1 || store.items[0].DisplayName != "讲义.pdf" {
		t.Fatalf("out=%+v err=%v items=%+v", out, err, store.items)
	}
	bad := [][]DraftBindingInput{{{FileVersionID: version, Policy: "bad", DisplayName: "x", SortPosition: 1}}, {{FileVersionID: version, Policy: PolicyDownload, DisplayName: "../x", SortPosition: 1}}, {{FileVersionID: version, Policy: PolicyDownload, DisplayName: "x", SortPosition: 1}, {FileVersionID: version, Policy: PolicyDownload, DisplayName: "y", SortPosition: 2}}}
	for _, in := range bad {
		if _, err := svc.Replace(context.Background(), actor, lesson, 7, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("input=%+v err=%v", in, err)
		}
	}
	student := actor
	student.User.Role = auth.RoleStudent
	if _, err := svc.Replace(context.Background(), student, lesson, 7, []DraftBindingInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("student err=%v", err)
	}
}
