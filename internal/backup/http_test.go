package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/httpx"
)

type backupHTTPService struct {
	createCalls int
	createKey   string
	createRun   Run
	createErr   error
	listCalls   int
	listFilter  Filter
	listPage    Page
	listErr     error
	getCalls    int
	getID       uuid.UUID
	getDetail   RunDetail
	getErr      error
}

func (s *backupHTTPService) RequestManual(_ context.Context, _ operations.Principal, key string) (Run, error) {
	s.createCalls++
	s.createKey = key
	return s.createRun, s.createErr
}

func (s *backupHTTPService) List(_ context.Context, _ operations.Principal, filter Filter) (Page, error) {
	s.listCalls++
	s.listFilter = filter
	return s.listPage, s.listErr
}

func (s *backupHTTPService) Get(_ context.Context, _ operations.Principal, id uuid.UUID) (RunDetail, error) {
	s.getCalls++
	s.getID = id
	return s.getDetail, s.getErr
}

func backupRequest(handler http.Handler, method, target string, user auth.User) (*httptest.ResponseRecorder, *http.Request) {
	request := httptest.NewRequest(method, target, nil)
	request = request.WithContext(auth.ContextWithUser(request.Context(), user))
	result := httptest.NewRecorder()
	httpx.RequestID(handler).ServeHTTP(result, request)
	return result, request
}

func activeAdmin() auth.User {
	return auth.User{
		ID:   uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		Role: auth.RoleAdmin, Status: auth.StatusActive,
	}
}

func TestBackupAdminCreateRequiresExactIdempotencyHeaderAndReturns202(t *testing.T) {
	runID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	at := time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC)
	service := &backupHTTPService{createRun: Run{
		ID: runID, Trigger: TriggerManual, State: StateQueued,
		RequestedBy: activeAdmin().ID, RequestedAt: at,
	}}
	handler := NewAdminHandler(service, nil).Routes()

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Idempotency-Key", "manual:key-123")
	request = request.WithContext(auth.ContextWithUser(request.Context(), activeAdmin()))
	result := httptest.NewRecorder()
	httpx.RequestID(handler).ServeHTTP(result, request)
	if result.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	if service.createCalls != 1 || service.createKey != "manual:key-123" {
		t.Fatalf("calls=%d key=%q", service.createCalls, service.createKey)
	}
	if got := result.Header().Get("Cache-Control"); got != "no-store, private" {
		t.Fatalf("cache-control=%q", got)
	}
	var payload struct {
		Data struct {
			ID          string    `json:"id"`
			Trigger     string    `json:"trigger"`
			State       string    `json:"state"`
			RequestedAt time.Time `json:"requestedAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ID != runID.String() || payload.Data.Trigger != "manual" ||
		payload.Data.State != "queued" || !payload.Data.RequestedAt.Equal(at) {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestBackupAdminCreateRejectsMissingDuplicateMalformedHeaderBodyAndQuery(t *testing.T) {
	valid128 := strings.Repeat("a", 128)
	valid8 := strings.Repeat("a", 8)
	invalid := []struct {
		name        string
		target      string
		body        string
		headers     []string
		contentType string
		wantCode    string
		wantStatus  int
	}{
		{name: "missing", target: "/", wantCode: "invalid_idempotency_key", wantStatus: http.StatusBadRequest},
		{name: "short", target: "/", headers: []string{"1234567"}, wantCode: "invalid_idempotency_key", wantStatus: http.StatusBadRequest},
		{name: "long", target: "/", headers: []string{strings.Repeat("a", 129)}, wantCode: "invalid_idempotency_key", wantStatus: http.StatusBadRequest},
		{name: "space", target: "/", headers: []string{"1234 678"}, wantCode: "invalid_idempotency_key", wantStatus: http.StatusBadRequest},
		{name: "comma", target: "/", headers: []string{"1234,678"}, wantCode: "invalid_idempotency_key", wantStatus: http.StatusBadRequest},
		{name: "duplicate", target: "/", headers: []string{"12345678", "abcdefgh"}, wantCode: "invalid_idempotency_key", wantStatus: http.StatusBadRequest},
		{name: "body", target: "/", body: `{"unexpected":true}`, headers: []string{valid8}, contentType: "application/json", wantCode: "invalid_request", wantStatus: http.StatusBadRequest},
		{name: "query", target: "/?force=true", headers: []string{valid128}, wantCode: "invalid_request", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			service := &backupHTTPService{}
			handler := NewAdminHandler(service, nil).Routes()
			request := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(tc.body))
			for _, header := range tc.headers {
				request.Header.Add("Idempotency-Key", header)
			}
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			request = request.WithContext(auth.ContextWithUser(request.Context(), activeAdmin()))
			result := httptest.NewRecorder()
			httpx.RequestID(handler).ServeHTTP(result, request)
			if result.Code != tc.wantStatus || !strings.Contains(result.Body.String(), `"code":"`+tc.wantCode+`"`) {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
			if service.createCalls != 0 {
				t.Fatalf("create calls=%d", service.createCalls)
			}
		})
	}
}

func TestBackupAdminCreateAcceptsExactEmptyJSONObjectUsedByWebClient(t *testing.T) {
	service := &backupHTTPService{createRun: Run{
		ID: uuid.New(), Trigger: TriggerManual, State: StateQueued,
		RequestedAt: time.Now().UTC(),
	}}
	handler := NewAdminHandler(service, nil).Routes()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "manual:key-123")
	request = request.WithContext(auth.ContextWithUser(request.Context(), activeAdmin()))
	result := httptest.NewRecorder()
	httpx.RequestID(handler).ServeHTTP(result, request)
	if result.Code != http.StatusAccepted || service.createCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", result.Code, service.createCalls, result.Body.String())
	}
}

func TestBackupAdminCreateAcceptsZeroLengthBodyThatIsNotNoBody(t *testing.T) {
	service := &backupHTTPService{createRun: Run{
		ID: uuid.New(), Trigger: TriggerManual, State: StateQueued,
		RequestedAt: time.Now().UTC(),
	}}
	handler := NewAdminHandler(service, nil).Routes()
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = io.NopCloser(strings.NewReader(""))
	request.Header.Set("Idempotency-Key", "manual:key-123")
	request = request.WithContext(auth.ContextWithUser(request.Context(), activeAdmin()))
	result := httptest.NewRecorder()
	httpx.RequestID(handler).ServeHTTP(result, request)
	if result.Code != http.StatusAccepted || service.createCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", result.Code, service.createCalls, result.Body.String())
	}
}

func TestBackupAdminCreateRejectsEveryOtherNonEmptyBody(t *testing.T) {
	for _, tc := range []struct {
		name         string
		body         string
		contentTypes []string
		wantStatus   int
		wantCode     string
	}{
		{
			name: "whitespace without content type", body: " \n\t",
			wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type",
		},
		{
			name: "whitespace JSON", body: " \n\t", contentTypes: []string{"application/json"},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name: "BOM", body: "\ufeff", contentTypes: []string{"application/json"},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name: "string", body: `"value"`, contentTypes: []string{"application/json"},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name: "null", body: `null`, contentTypes: []string{"application/json"},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name: "array", body: `[]`, contentTypes: []string{"application/json"},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name: "multiple values", body: `{} {}`, contentTypes: []string{"application/json"},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name: "trailing token", body: `{} true`, contentTypes: []string{"application/json"},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name: "duplicate content type", body: `{}`,
			contentTypes: []string{"application/json", "application/json"},
			wantStatus:   http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &backupHTTPService{}
			handler := NewAdminHandler(service, nil).Routes()
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			request.Header.Set("Idempotency-Key", "manual:key-123")
			for _, value := range tc.contentTypes {
				request.Header.Add("Content-Type", value)
			}
			request = request.WithContext(auth.ContextWithUser(request.Context(), activeAdmin()))
			result := httptest.NewRecorder()
			httpx.RequestID(handler).ServeHTTP(result, request)
			if result.Code != tc.wantStatus ||
				!strings.Contains(result.Body.String(), `"code":"`+tc.wantCode+`"`) {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
			if service.createCalls != 0 {
				t.Fatalf("create calls=%d", service.createCalls)
			}
		})
	}
}

func TestBackupAdminListUsesStableRequestedAtAndIDCursor(t *testing.T) {
	cursorAt := time.Date(2026, 7, 28, 3, 0, 0, 123, time.UTC)
	cursorID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	service := &backupHTTPService{listPage: Page{
		Items: []RunSummary{{
			ID:      uuid.MustParse("33333333-3333-4333-8333-333333333333"),
			Trigger: TriggerScheduled, State: StateSucceeded,
			RequestedAt: time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC),
		}},
		Next: Cursor{RequestedAt: cursorAt, ID: cursorID},
	}}
	handler := NewAdminHandler(service, nil).Routes()
	target := "/?beforeRequestedAt=" +
		"2026-07-28T03%3A00%3A00.000000123Z&beforeId=" + cursorID.String() + "&limit=20"
	result, _ := backupRequest(handler, http.MethodGet, target, activeAdmin())
	if result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	if service.listCalls != 1 || service.listFilter.Limit != 20 ||
		service.listFilter.Before.ID != cursorID ||
		!service.listFilter.Before.RequestedAt.Equal(cursorAt) {
		t.Fatalf("filter=%+v", service.listFilter)
	}
	if !strings.Contains(result.Body.String(), `"nextBeforeRequestedAt":"2026-07-28T03:00:00.000000123Z"`) ||
		!strings.Contains(result.Body.String(), `"nextBeforeId":"`+cursorID.String()+`"`) {
		t.Fatalf("body=%s", result.Body.String())
	}
}

func TestBackupAdminListRejectsPartialNoncanonicalAndUnexpectedCursor(t *testing.T) {
	id := "22222222-2222-4222-8222-222222222222"
	targets := []string{
		"/?",
		"/?beforeId=" + id,
		"/?beforeRequestedAt=2026-07-28T03%3A00%3A00Z",
		"/?beforeRequestedAt=2026-07-28T03%3A00%3A00%2B00%3A00&beforeId=" + id,
		"/?beforeRequestedAt=2026-07-28T03%3A00%3A00Z&beforeId=22222222-2222-4222-8222-22222222222A",
		"/?beforeRequestedAt=2026-07-28T03%3A00%3A00Z&beforeId=" + id + "&limit=0",
		"/?beforeRequestedAt=2026-07-28T03%3A00%3A00Z&beforeId=" + id + "&limit=101",
		"/?state=failed",
	}
	for _, target := range targets {
		service := &backupHTTPService{}
		handler := NewAdminHandler(service, nil).Routes()
		result, _ := backupRequest(handler, http.MethodGet, target, activeAdmin())
		if result.Code != http.StatusBadRequest ||
			!strings.Contains(result.Body.String(), `"code":"invalid_request"`) ||
			service.listCalls != 0 {
			t.Errorf("target=%q status=%d calls=%d body=%s", target, result.Code, service.listCalls, result.Body.String())
		}
	}
}

func TestBackupAdminDetailIsSafeAndContainsArtifactsAndRestoreEvidence(t *testing.T) {
	runID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	verificationID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	at := time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC)
	service := &backupHTTPService{getDetail: RunDetail{
		Run: Run{
			ID: runID, IdempotencyKey: "private-idempotency-key",
			Trigger: TriggerManual, State: StateSucceeded,
			EncryptionKeyID: "private-key-id", LocalSnapshotID: "opaque-local-secret",
			RemoteSnapshotID: "opaque-remote-secret", ManifestSHA256: []byte("01234567890123456789012345678901"),
			ErrorTraceID: "private-internal-trace", RequestedAt: at,
		},
		Artifacts: []Artifact{{
			BackupRunID: runID, Kind: ArtifactManifest, Repository: RepositoryLocal,
			SnapshotID: "/private/repository/object-name", SHA256: []byte("01234567890123456789012345678901"),
			SizeBytes: 42, VerifiedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour),
		}},
		RestoreVerifications: []RestoreVerification{{
			ID: verificationID, BackupRunID: runID, State: RestoreSucceeded,
			DatabaseRowCounts:  map[string]int64{"users": 3, "secret_table": 99},
			CheckedObjectCount: 5, MissingObjectCount: 0, UnexpectedObjectCount: 0,
			SessionRevocationVerified: true, RTOSeconds: int64Pointer(90),
			ReportSHA256: []byte("01234567890123456789012345678901"),
			ErrorTraceID: "private-restore-trace",
		}},
	}}
	handler := NewAdminHandler(service, nil).Routes()
	result, _ := backupRequest(handler, http.MethodGet, "/"+runID.String(), activeAdmin())
	if result.Code != http.StatusOK || service.getID != runID {
		t.Fatalf("status=%d id=%s body=%s", result.Code, service.getID, result.Body.String())
	}
	body := result.Body.String()
	for _, forbidden := range []string{
		"idempotency", "private-key-id", "opaque-local-secret", "opaque-remote-secret",
		"/private/repository", "object-name", "private-internal-trace", "private-restore-trace",
		"sha256", "secret_table",
	} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Errorf("response leaked %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{
		`"artifacts"`, `"kind":"manifest"`, `"repository":"local"`,
		`"sizeBytes":42`, `"restoreVerifications"`, `"state":"succeeded"`,
		`"databaseRowCounts":{"users":3}`, `"sessionRevocationVerified":true`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("response missing %q: %s", required, body)
		}
	}
}

func TestBackupAdminRoutesAreAdminOnlyAndNoStore(t *testing.T) {
	for _, tc := range []struct {
		name       string
		user       *auth.User
		wantStatus int
	}{
		{name: "unauthenticated", wantStatus: http.StatusUnauthorized},
		{name: "student", user: func() *auth.User {
			user := activeAdmin()
			user.Role = auth.RoleStudent
			return &user
		}(), wantStatus: http.StatusForbidden},
		{name: "disabled admin", user: func() *auth.User {
			user := activeAdmin()
			user.Status = auth.StatusDisabled
			return &user
		}(), wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &backupHTTPService{}
			handler := NewAdminHandler(service, nil).Routes()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.user != nil {
				request = request.WithContext(auth.ContextWithUser(request.Context(), *tc.user))
			}
			result := httptest.NewRecorder()
			httpx.RequestID(handler).ServeHTTP(result, request)
			if result.Code != tc.wantStatus || result.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatalf("status=%d cache=%q body=%s", result.Code, result.Header().Get("Cache-Control"), result.Body.String())
			}
			if service.listCalls != 0 {
				t.Fatalf("list calls=%d", service.listCalls)
			}
		})
	}
}

func TestBackupAdminStableErrorMappingIncludesRequestID(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "queued", err: ErrAlreadyQueued, wantStatus: http.StatusConflict, wantCode: "backup_already_queued"},
		{name: "unavailable", err: ErrUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "backup_unavailable"},
		{name: "not found", err: ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "opaque storage error", err: errors.New("postgres password=secret /private/repository"), wantStatus: http.StatusServiceUnavailable, wantCode: "backup_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &backupHTTPService{createErr: tc.err}
			handler := NewAdminHandler(service, nil).Routes()
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			request.Header.Set("Idempotency-Key", "manual:key-123")
			request = request.WithContext(auth.ContextWithUser(request.Context(), activeAdmin()))
			result := httptest.NewRecorder()
			httpx.RequestID(handler).ServeHTTP(result, request)
			body := result.Body.String()
			if result.Code != tc.wantStatus ||
				!strings.Contains(body, `"code":"`+tc.wantCode+`"`) ||
				!strings.Contains(body, `"requestId":"`) ||
				strings.Contains(body, "password") || strings.Contains(body, "/private") {
				t.Fatalf("status=%d body=%s", result.Code, body)
			}
		})
	}
}

func int64Pointer(value int64) *int64 { return &value }
