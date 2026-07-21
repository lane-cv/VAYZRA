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

type conflictBindingHTTPService struct{}

func (conflictBindingHTTPService) Replace(context.Context, Principal, uuid.UUID, int64, []DraftBindingInput) ([]DraftBinding, error) {
	return nil, ErrDraftConflict
}

func TestBindingHTTPUsesDraftConflictCode(t *testing.T) {
	lessonID := uuid.New()
	versionID := uuid.New()
	req := httptest.NewRequest(http.MethodPut, "/"+lessonID.String()+"/files", strings.NewReader(`{"expectedVersion":1,"files":[{"fileVersionId":"`+versionID.String()+`","policy":"preview","displayName":"x","description":"","sortPosition":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.1:1234"
	req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	httpx.RequestID(NewBindingHandler(conflictBindingHTTPService{}, nil).Routes()).ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"draft_conflict"`) || strings.Contains(w.Body.String(), "upload_conflict") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
