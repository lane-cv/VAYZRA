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
	svc := NewStudentService(&fakeStudentStore{lesson: StudentLesson{Revision: Revision{ID: uuid.New(), LessonID: lessonID}}, getErr: ErrNotFound}, fixedTeachingClock)

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

	if _, _, err := svc.Browse(context.Background(), disabled, BrowseInput{}); !errors.Is(err, ErrForbidden) {
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
	lesson         StudentLesson
	getErr         error
	progressCalled bool
}

func (s *fakeStudentStore) BrowseStudent(context.Context, BrowseInput) ([]StudentCatalogNode, CatalogCursor, error) {
	return nil, CatalogCursor{}, nil
}
func (s *fakeStudentStore) Recent(context.Context, uuid.UUID, int) ([]RecentLesson, error) {
	return nil, nil
}
func (s *fakeStudentStore) Search(context.Context, SearchInput) ([]SearchResult, SearchCursor, error) {
	return nil, SearchCursor{}, nil
}
func (s *fakeStudentStore) GetLesson(context.Context, uuid.UUID, uuid.UUID) (StudentLesson, error) {
	return s.lesson, s.getErr
}
func (s *fakeStudentStore) GetPosition(context.Context, uuid.UUID, uuid.UUID) (LessonProgress, error) {
	return LessonProgress{}, s.getErr
}
func (s *fakeStudentStore) UpdateProgress(context.Context, uuid.UUID, ProgressInput) error {
	s.progressCalled = true
	return nil
}

func studentTeachingPrincipal(id uuid.UUID) Principal {
	return Principal{User: auth.User{ID: id, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "student-request", IP: net.ParseIP("192.0.2.44")}
}

var _ = time.Minute
