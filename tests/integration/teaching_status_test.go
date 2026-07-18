package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/httpx"
	"happylearn.local/app/internal/teaching"
	"happylearn.local/app/tests/integration"
)

func TestAdminLessonStatusTransitionsAndWithdrawHidesStudentLesson(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "lesson_status_admin", "admin")
	studentID := insertTeachingUser(t, pool, "lesson_status_student", "student")
	path := insertFullCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, path.chapterID, adminID)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'all')`, lessonID); err != nil {
		t.Fatal(err)
	}

	store := teaching.NewPostgresStore(pool)
	service := teaching.NewService(store, nil, time.Now)
	adminActor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "lesson-status", IP: net.ParseIP("192.0.2.81")}
	adminHTTP := httpx.RequestID(teaching.NewAdminHandler(service).Routes())
	adminUser := auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}
	assertAdminLessonStatus(t, adminHTTP, adminUser, lessonID, "draft", false)

	revision, err := service.Publish(ctx, adminActor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertAdminLessonStatus(t, adminHTTP, adminUser, lessonID, "published", true)

	studentService := teaching.NewStudentService(store, time.Now)
	studentActor := teaching.Principal{User: auth.User{ID: studentID, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "lesson-status-student", IP: net.ParseIP("192.0.2.82")}
	if _, err := studentService.GetLesson(ctx, studentActor, lessonID); err != nil {
		t.Fatalf("published lesson not visible to student: %v", err)
	}
	if err := service.Withdraw(ctx, adminActor, lessonID); err != nil {
		t.Fatal(err)
	}
	assertAdminLessonStatus(t, adminHTTP, adminUser, lessonID, "withdrawn", false)
	if _, err := studentService.GetLesson(ctx, studentActor, lessonID); !errors.Is(err, teaching.ErrNotFound) {
		t.Fatalf("withdrawn student lesson error = %v, want not found", err)
	}
	history, _, err := store.ListAdminRevisions(ctx, lessonID, 20, teaching.RevisionCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != revision.ID {
		t.Fatalf("withdrawal changed immutable history: %#v", history)
	}

	if err := service.ArchiveCatalog(ctx, adminActor, teaching.CatalogArchiveInput{Kind: teaching.CatalogGrade, ID: path.gradeID, Archived: true}); err != nil {
		t.Fatal(err)
	}
	assertAdminLessonStatus(t, adminHTTP, adminUser, lessonID, "archived", false)
	history, _, err = store.ListAdminRevisions(ctx, lessonID, 20, teaching.RevisionCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != revision.ID {
		t.Fatalf("ancestor archive changed immutable history: %#v", history)
	}
}

func assertAdminLessonStatus(t *testing.T, handler http.Handler, admin auth.User, lessonID uuid.UUID, want string, wantPublished bool) {
	t.Helper()
	catalogRequest := httptest.NewRequest(http.MethodGet, "/catalog?kind=lesson&includeArchived=true&limit=200", nil)
	catalogRequest = catalogRequest.WithContext(auth.ContextWithUser(catalogRequest.Context(), admin))
	catalogResponse := httptest.NewRecorder()
	handler.ServeHTTP(catalogResponse, catalogRequest)
	if catalogResponse.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogResponse.Code, catalogResponse.Body.String())
	}
	var catalog struct {
		Data []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			Published bool   `json:"published"`
		} `json:"data"`
	}
	if err := json.Unmarshal(catalogResponse.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range catalog.Data {
		if item.ID == lessonID.String() {
			found = true
			if item.Status != want || item.Published != wantPublished {
				t.Fatalf("catalog lesson status=%q published=%t, want %q/%t", item.Status, item.Published, want, wantPublished)
			}
		}
	}
	if !found {
		t.Fatalf("lesson %s absent from admin catalog: %s", lessonID, catalogResponse.Body.String())
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/lessons/"+lessonID.String(), nil)
	detailRequest = detailRequest.WithContext(auth.ContextWithUser(detailRequest.Context(), admin))
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.Status != want {
		t.Fatalf("detail lesson status=%q, want %q; body=%s", detail.Data.Status, want, detailResponse.Body.String())
	}
}
