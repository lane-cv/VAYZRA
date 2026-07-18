package teaching

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestStudentLessonReadDoesNotRevealUnauthorizedLesson(t *testing.T) {
	studentA, studentB, lessonID := uuid.New(), uuid.New(), uuid.New()
	svc := NewStudentService(&fakeStudentStore{lesson: Revision{ID: uuid.New(), LessonID: lessonID}, getErr: ErrNotFound}, fixedTeachingClock)

	_, err := svc.GetLesson(context.Background(), studentTeachingPrincipal(studentA), lessonID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want not found for another student's lesson", err)
	}
	_ = studentB
}

func TestStudentServiceRejectsDisabledStudentAndInvalidProgress(t *testing.T) {
	studentID := uuid.New()
	store := &fakeStudentStore{}
	svc := NewStudentService(store, fixedTeachingClock)
	disabled := studentTeachingPrincipal(studentID)
	disabled.User.Status = auth.StatusDisabled

	if _, err := svc.Browse(context.Background(), disabled, BrowseInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("browse error = %v, want forbidden", err)
	}
	err := svc.UpdateProgress(context.Background(), studentTeachingPrincipal(studentID), ProgressInput{
		RevisionID: uuid.New(), Anchor: string(make([]byte, 161)), ScrollRatio: 1.2, ObservedAt: fixedTeachingClock(),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("progress error = %v, want invalid", err)
	}
	if store.progressCalled {
		t.Fatal("invalid progress was sent to store")
	}
}

type fakeStudentStore struct {
	lesson         Revision
	getErr         error
	progressCalled bool
}

func (s *fakeStudentStore) Browse(context.Context, BrowseInput) ([]CatalogNode, error) {
	return nil, nil
}
func (s *fakeStudentStore) Search(context.Context, SearchInput) ([]Revision, SearchCursor, error) {
	return nil, SearchCursor{}, nil
}
func (s *fakeStudentStore) GetLesson(context.Context, uuid.UUID, uuid.UUID) (Revision, error) {
	return s.lesson, s.getErr
}
func (s *fakeStudentStore) UpdateProgress(context.Context, uuid.UUID, ProgressInput) error {
	s.progressCalled = true
	return nil
}

func studentTeachingPrincipal(id uuid.UUID) Principal {
	return Principal{User: auth.User{ID: id, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "student-request", IP: net.ParseIP("192.0.2.44")}
}

var _ = time.Minute
