package teaching

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type boundaryAdminService struct {
	*fakeAdminHTTPService
	archives, publishes, withdraws int
}

func (s *boundaryAdminService) ArchiveLesson(context.Context, Principal, uuid.UUID) error {
	s.archives++
	return nil
}
func (s *boundaryAdminService) Publish(context.Context, Principal, PublishInput) (Revision, error) {
	s.publishes++
	return Revision{}, nil
}
func (s *boundaryAdminService) Withdraw(context.Context, Principal, uuid.UUID) error {
	s.withdraws++
	return nil
}

func TestAdminArchiveBoundaryAndOversizedEmptyBodyOperations(t *testing.T) {
	svc := &boundaryAdminService{fakeAdminHTTPService: &fakeAdminHTTPService{}}
	h := NewAdminHandler(svc).Routes()
	id := uuid.NewString()
	admin := auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}
	r := httptest.NewRequest(http.MethodPost, "/lessons/"+id+"/archive", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), admin))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || svc.archives != 1 {
		t.Fatalf("archive status=%d calls=%d", w.Code, svc.archives)
	}
	r = httptest.NewRequest(http.MethodPost, "/lessons/"+id+"/archive", strings.NewReader("x"))
	r = r.WithContext(auth.ContextWithUser(r.Context(), admin))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || svc.archives != 1 {
		t.Fatalf("body status=%d calls=%d", w.Code, svc.archives)
	}
	r = httptest.NewRequest(http.MethodPost, "/lessons/"+id+"/archive", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleStudent, Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || svc.archives != 1 {
		t.Fatalf("forbidden status=%d calls=%d", w.Code, svc.archives)
	}
	for _, endpoint := range []string{"publish", "withdraw"} {
		r = httptest.NewRequest(http.MethodPost, "/lessons/"+id+"/"+endpoint, strings.NewReader(strings.Repeat("x", 2048)))
		r.Header.Set("If-Match", "1")
		r = r.WithContext(auth.ContextWithUser(r.Context(), admin))
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s status=%d", endpoint, w.Code)
		}
	}
	if svc.publishes != 0 || svc.withdraws != 0 {
		t.Fatalf("oversized dispatched publish=%d withdraw=%d", svc.publishes, svc.withdraws)
	}
}
