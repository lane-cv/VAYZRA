package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/qanda"
)

type appStudentQuestions struct{ lists int }

func (*appStudentQuestions) CreateThread(context.Context, qanda.Principal, qanda.CreateThreadInput) (qanda.Thread, qanda.Message, error) {
	return qanda.Thread{}, qanda.Message{}, nil
}
func (s *appStudentQuestions) ListStudentThreads(context.Context, qanda.Principal, qanda.Status, qanda.ThreadCursor) ([]qanda.Thread, qanda.ThreadCursor, error) {
	s.lists++
	return []qanda.Thread{}, qanda.ThreadCursor{}, nil
}
func (*appStudentQuestions) GetStudentThread(context.Context, qanda.Principal, uuid.UUID) (qanda.ThreadDetail, error) {
	return qanda.ThreadDetail{}, nil
}
func (*appStudentQuestions) ListStudentMessages(context.Context, qanda.Principal, uuid.UUID, qanda.MessageCursor) ([]qanda.Message, qanda.MessageCursor, error) {
	return []qanda.Message{}, qanda.MessageCursor{}, nil
}
func (*appStudentQuestions) AddStudentMessage(context.Context, qanda.Principal, qanda.AddMessageInput) (qanda.Thread, qanda.Message, error) {
	return qanda.Thread{}, qanda.Message{}, nil
}

func TestQARoutesMountStudentQuestionsOnlyForAuthenticatedStudents(t *testing.T) {
	svc := &appStudentQuestions{}
	h := New(Dependencies{Auth: &appStudentAuth{}, StudentQuestions: svc})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/student/questions", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || svc.lists != 1 || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d lists=%d cache=%q body=%s", w.Code, svc.lists, w.Header().Get("Cache-Control"), w.Body.String())
	}

	adminSvc := &appStudentQuestions{}
	adminHandler := New(Dependencies{Auth: &appAdminAuth{}, StudentQuestions: adminSvc})
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/student/questions", nil)
	adminRequest.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	adminResult := httptest.NewRecorder()
	adminHandler.ServeHTTP(adminResult, adminRequest)
	if adminResult.Code != http.StatusForbidden || adminSvc.lists != 0 || !strings.Contains(adminResult.Body.String(), `"requestId":`) {
		t.Fatalf("status=%d lists=%d body=%s", adminResult.Code, adminSvc.lists, adminResult.Body.String())
	}
}

func TestQARoutesRemainOptional(t *testing.T) {
	h := New(Dependencies{Auth: &appStudentAuth{}})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/student/questions", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

type appAdminQuestions struct{ lists int }

func (s *appAdminQuestions) ListAdminThreads(context.Context, qanda.Principal, qanda.AdminThreadFilter, qanda.ThreadCursor) ([]qanda.Thread, qanda.ThreadCursor, error) {
	s.lists++
	return []qanda.Thread{}, qanda.ThreadCursor{}, nil
}
func (*appAdminQuestions) GetAdminThread(context.Context, qanda.Principal, uuid.UUID) (qanda.AdminThreadDetail, error) {
	return qanda.AdminThreadDetail{}, nil
}
func (*appAdminQuestions) AddAdminMessage(context.Context, qanda.Principal, qanda.AddAdminMessageInput) (qanda.Thread, qanda.Message, error) {
	return qanda.Thread{}, qanda.Message{}, nil
}
func (*appAdminQuestions) ChangeStatus(context.Context, qanda.Principal, qanda.ChangeStatusInput) (qanda.Thread, error) {
	return qanda.Thread{}, nil
}
func (*appAdminQuestions) AddTeacherNote(context.Context, qanda.Principal, qanda.AddTeacherNoteInput) (qanda.TeacherNote, error) {
	return qanda.TeacherNote{}, nil
}

func TestQARoutesMountAdminQuestionsOnlyForAuthenticatedAdmin(t *testing.T) {
	svc := &appAdminQuestions{}
	h := New(Dependencies{Auth: &appAdminAuth{}, AdminQuestions: svc})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/questions", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || svc.lists != 1 {
		t.Fatalf("status=%d lists=%d body=%s", w.Code, svc.lists, w.Body.String())
	}
}
