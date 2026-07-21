package files

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

func TestFileCenterHTTPRoutesAndStableLifecycleErrors(t *testing.T) {
	fileID, lessonID, versionID := uuid.New(), uuid.New(), uuid.New()
	service := &fileCenterHTTPStub{deleteErr: ErrFileInUse, rollbackErr: ErrFileVersionExpired}
	handler := httpx.RequestID(NewFileCenterHandler(service, nil).Routes())

	request := func(method, target, body string) *http.Request {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.1:1234"
		req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request(http.MethodGet, "/?q=%20Newton%20&state=failed&reference=unreferenced&limit=25", ""))
	if w.Code != http.StatusOK || service.filter.Name != "Newton" || service.cursor.Limit != 25 {
		t.Fatalf("status=%d filter=%+v cursor=%+v body=%s", w.Code, service.filter, service.cursor, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request(http.MethodDelete, "/"+fileID.String(), ""))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"file_in_use"`) {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	body := `{"lessonId":"` + lessonID.String() + `","fileVersionId":"` + versionID.String() + `"}`
	handler.ServeHTTP(w, request(http.MethodPost, "/"+fileID.String()+"/rollback", body))
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), `"code":"file_version_expired"`) {
		t.Fatalf("rollback status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFileCenterHTTPRequiresAdmin(t *testing.T) {
	handler := NewFileCenterHandler(&fileCenterHTTPStub{}, nil).Routes()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

type fileCenterHTTPStub struct {
	filter      FileFilter
	cursor      Cursor
	deleteErr   error
	rollbackErr error
}

func (s *fileCenterHTTPStub) List(_ context.Context, _ Principal, filter FileFilter, cursor Cursor) (FilePage, error) {
	s.filter, s.cursor = filter, cursor
	return FilePage{Items: []FileListItem{}}, nil
}
func (*fileCenterHTTPStub) Detail(context.Context, Principal, uuid.UUID) (FileDetail, error) {
	return FileDetail{}, nil
}
func (*fileCenterHTTPStub) Retry(context.Context, Principal, uuid.UUID) error { return nil }
func (*fileCenterHTTPStub) Replace(context.Context, Principal, uuid.UUID, uuid.UUID) error {
	return nil
}
func (s *fileCenterHTTPStub) RollbackDraftBinding(context.Context, Principal, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return s.rollbackErr
}
func (s *fileCenterHTTPStub) RequestDelete(context.Context, Principal, uuid.UUID) error {
	return s.deleteErr
}
