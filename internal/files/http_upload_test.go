package files

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

func TestUploadHTTPStrictStreamingPart(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store, objects := newFakeUploadStore(), newFakeObjects()
	actor := uploadAdmin()
	payload := []byte("streamed")
	session := store.seed(actor.User.ID, int64(len(payload)), digestOf(payload), now.Add(time.Hour))
	h := httpx.RequestID(NewUploadHandler(NewUploadService(store, objects, func() time.Time { return now })).Routes())

	request := func(contentType string, body []byte) *http.Request {
		r := httptest.NewRequest(http.MethodPut, "/"+session.ID.String()+"/parts/1", bytes.NewReader(body))
		r = r.WithContext(auth.ContextWithUser(r.Context(), actor.User))
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("X-Part-SHA256", digestOf(payload))
		return r
	}

	badType := request("multipart/form-data; boundary=x", payload)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, badType)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("multipart status=%d body=%s", w.Code, w.Body.String())
	}

	missingLength := request("application/octet-stream", payload)
	missingLength.Header.Del("Content-Length")
	missingLength.ContentLength = -1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, missingLength)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing length status=%d body=%s", w.Code, w.Body.String())
	}

	valid := request("application/octet-stream", payload)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, valid)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), digestOf(payload)) {
		t.Fatalf("valid status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUploadHTTPAdminJSONRoutesAndOpaqueResponse(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store, objects := newFakeUploadStore(), newFakeObjects()
	actor := uploadAdmin()
	h := httpx.RequestID(NewUploadHandler(NewUploadService(store, objects, func() time.Time { return now })).Routes())
	body := `{"displayName":"lesson.pdf","declaredMime":"application/pdf","expectedSize":3,"expectedSha256":"` + digestOf([]byte("pdf")) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r = r.WithContext(auth.ContextWithUser(r.Context(), actor.User))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	response := w.Body.String()
	if strings.Contains(response, "objectKey") || strings.Contains(response, "minio") || strings.Contains(response, "originals/") {
		t.Fatalf("response exposed storage internals: %s", response)
	}

	student := actor.User
	student.Role = auth.RoleStudent
	r = httptest.NewRequest(http.MethodGet, "/"+uuid.NewString(), nil)
	r = r.WithContext(auth.ContextWithUser(context.Background(), student))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("student status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUploadHTTPSourceDoesNotBufferBodies(t *testing.T) {
	source, err := os.ReadFile("http_upload.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(source, []byte("io.ReadAll")) || bytes.Contains(source, []byte("multipart.NewReader")) || bytes.Contains(source, []byte("ParseMultipartForm")) {
		t.Fatal("upload HTTP handler contains whole-body buffering or multipart/form-data code")
	}
}
func TestUploadHTTPAdmissionCoversStoreAndRejectsThird(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store := newFakeUploadStore()
	objects := &blockingObjects{fakeObjects: newFakeObjects(), entered: make(chan struct{}, 2), release: make(chan struct{})}
	actor := uploadAdmin()
	firstPayload := bytes.Repeat([]byte{0}, int(UploadPartSize))
	finalPayload := []byte("x")
	session := store.seed(actor.User.ID, UploadPartSize+1, digestOf([]byte("whole")), now.Add(time.Hour))
	h := httpx.RequestID(NewUploadHandler(NewUploadService(store, objects, func() time.Time { return now })).Routes())
	request := func(number int, payload []byte) *http.Request {
		r := httptest.NewRequest(http.MethodPut, "/"+session.ID.String()+"/parts/"+strconv.Itoa(number), bytes.NewReader(payload))
		r = r.WithContext(auth.ContextWithUser(r.Context(), actor.User))
		r.Header.Set("Content-Type", "application/octet-stream")
		r.Header.Set("X-Part-SHA256", digestOf(payload))
		return r
	}
	results := make(chan int, 2)
	go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, request(1, firstPayload)); results <- w.Code }()
	go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, request(2, finalPayload)); results <- w.Code }()
	<-objects.entered
	<-objects.entered
	third := httptest.NewRecorder()
	h.ServeHTTP(third, request(1, firstPayload))
	if third.Code != http.StatusTooManyRequests {
		t.Fatalf("third status=%d body=%s", third.Code, third.Body.String())
	}
	if got := store.admitCalls.Load(); got != 2 {
		t.Fatalf("admit calls=%d", got)
	}
	if got := store.getCalls.Load(); got != 2 {
		t.Fatalf("store queries before/inside admission=%d", got)
	}
	close(objects.release)
	if code := <-results; code != http.StatusOK {
		t.Fatalf("first status=%d", code)
	}
	if code := <-results; code != http.StatusOK {
		t.Fatalf("second status=%d", code)
	}

	before := store.getCalls.Load()
	oversized := request(1, nil)
	oversized.ContentLength = UploadPartSize + 1
	oversized.Header.Set("Content-Length", strconv.FormatInt(UploadPartSize+1, 10))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, oversized)
	if w.Code != http.StatusBadRequest || store.getCalls.Load() != before {
		t.Fatalf("oversized status=%d storeBefore=%d storeAfter=%d", w.Code, before, store.getCalls.Load())
	}
}
