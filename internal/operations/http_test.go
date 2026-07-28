package operations

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

type operationsHTTPStub struct {
	settings Settings
	page     audit.AuditPage
	filter   audit.AuditFilter
	getErr   error
	putErr   error
	auditErr error
	puts     int
	lastPut  Settings
}

type operationsAuditReader struct {
	calls int
	page  audit.AuditPage
}

func (r *operationsAuditReader) ListFiltered(_ context.Context, _ audit.AuditFilter) (audit.AuditPage, error) {
	r.calls++
	return r.page, nil
}

func (s *operationsHTTPStub) GetSettings(context.Context, Principal) (Settings, error) {
	return s.settings, s.getErr
}

func (s *operationsHTTPStub) UpdateSettings(_ context.Context, _ Principal, settings Settings) (Settings, error) {
	s.puts++
	s.lastPut = settings
	if s.putErr != nil {
		return Settings{}, s.putErr
	}
	settings.Version++
	settings.UpdatedAt = time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	return settings, nil
}

func (s *operationsHTTPStub) ListAudit(_ context.Context, _ Principal, filter audit.AuditFilter) (audit.AuditPage, error) {
	s.filter = filter
	return s.page, s.auditErr
}

func TestOperationsAuditServiceRequiresActiveAdminContext(t *testing.T) {
	reader := &operationsAuditReader{page: audit.AuditPage{Items: []audit.Record{}}}
	service := NewService(&fakeStore{settings: validSettings()}, reader)
	filter := audit.AuditFilter{Limit: 20}
	if _, err := service.ListAudit(context.Background(), operationsAdmin(uuid.New()), filter); err != nil {
		t.Fatal(err)
	}
	student := operationsAdmin(uuid.New())
	student.User.Role = auth.RoleStudent
	if _, err := service.ListAudit(context.Background(), student, filter); !errors.Is(err, ErrForbidden) {
		t.Fatalf("student error=%v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls=%d", reader.calls)
	}
}

func TestOperationsHTTPSettingsAreTypedStrictAndConflictSafe(t *testing.T) {
	stub := &operationsHTTPStub{settings: validSettings()}
	stub.settings.UpdatedAt = time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	handler := NewAdminHandler(stub, nil).Routes()

	get := operationsHTTPRequest(http.MethodGet, "/settings", "", auth.RoleAdmin)
	getResult := httptest.NewRecorder()
	handler.ServeHTTP(getResult, get)
	if getResult.Code != http.StatusOK ||
		getResult.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("get status=%d headers=%v body=%s", getResult.Code, getResult.Header(), getResult.Body.String())
	}
	body := getResult.Body.String()
	for _, field := range []string{
		`"version":1`, `"siteName":"HappyLearn"`, `"siteAnnouncement":""`,
		`"softDeleteRetentionDays":30`, `"auditRetentionDays":365`,
		`"operationalSampleRetentionDays":7`, `"backupHour":3`,
		`"backupMinute":0`, `"backupTimezone":"Asia/Shanghai"`,
		`"diskWarningPercent":75`, `"diskCriticalPercent":90`,
		`"aiErrorWarningPercent":10`, `"aiErrorCriticalPercent":25`,
		`"processingQueueWarning":20`, `"processingQueueCritical":100`,
		`"updatedAt":"2026-07-28T01:00:00Z"`,
	} {
		if !strings.Contains(body, field) {
			t.Fatalf("missing %s in %s", field, body)
		}
	}
	if strings.Contains(body, "updatedBy") {
		t.Fatalf("internal updater leaked: %s", body)
	}
	queryResult := httptest.NewRecorder()
	handler.ServeHTTP(queryResult, operationsHTTPRequest(
		http.MethodGet, "/settings?unknown=value", "", auth.RoleAdmin,
	))
	if queryResult.Code != http.StatusBadRequest {
		t.Fatalf("settings query status=%d body=%s", queryResult.Code, queryResult.Body.String())
	}
	assertOperationsErrorEnvelope(t, queryResult, "settings_invalid")

	valid := `{
		"version":1,
		"siteName":"HappyLearn",
		"siteAnnouncement":"",
		"softDeleteRetentionDays":30,
		"auditRetentionDays":365,
		"operationalSampleRetentionDays":7,
		"backupHour":3,
		"backupMinute":0,
		"backupTimezone":"Asia/Shanghai",
		"diskWarningPercent":75,
		"diskCriticalPercent":90,
		"aiErrorWarningPercent":10,
		"aiErrorCriticalPercent":25,
		"processingQueueWarning":20,
		"processingQueueCritical":100,
		"updatedAt":"2026-07-28T01:00:00Z"
	}`
	put := operationsHTTPRequest(http.MethodPut, "/settings", valid, auth.RoleAdmin)
	putResult := httptest.NewRecorder()
	handler.ServeHTTP(putResult, put)
	if putResult.Code != http.StatusOK || stub.puts != 1 ||
		!strings.Contains(putResult.Body.String(), `"version":2`) {
		t.Fatalf("put status=%d puts=%d body=%s", putResult.Code, stub.puts, putResult.Body.String())
	}

	for name, value := range map[string]string{
		"unknown field": strings.Replace(valid, `"version":1`, `"version":1,"credential":"secret"`, 1),
		"missing value": strings.Replace(valid, `"backupMinute":0,`, "", 1),
		"trailing JSON": valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			before := stub.puts
			request := operationsHTTPRequest(http.MethodPut, "/settings", value, auth.RoleAdmin)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusBadRequest || stub.puts != before {
				t.Fatalf("status=%d puts=%d body=%s", result.Code, stub.puts, result.Body.String())
			}
			assertOperationsErrorEnvelope(t, result, "settings_invalid")
		})
	}

	stub.putErr = ErrConflict
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, operationsHTTPRequest(http.MethodPut, "/settings", valid, auth.RoleAdmin))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	assertOperationsErrorEnvelope(t, conflict, "settings_conflict")
}

func TestOperationsHTTPSettingsPUTEnforcesStrictJSONBoundary(t *testing.T) {
	valid := validOperationsSettingsJSON()
	withoutUpdatedAt := strings.Replace(
		valid, `,"updatedAt":"2026-07-28T01:00:00Z"`, "", 1,
	)
	invalidUTF8 := strings.Replace(valid, "HappyLearn", "Happy\xffLearn", 1)
	tooLarge := valid + strings.Repeat(" ", 65<<10)

	for _, tc := range []struct {
		name         string
		body         string
		contentTypes []string
		wantStatus   int
		wantCode     string
	}{
		{"all exact contract fields", valid, []string{"application/json"}, http.StatusOK, ""},
		{"valid utf8 charset", valid, []string{"application/json; charset=UTF-8"}, http.StatusOK, ""},
		{"updatedAt omitted", withoutUpdatedAt, []string{"application/json"}, http.StatusOK, ""},
		{"missing content type", valid, nil, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"duplicate content type", valid, []string{"application/json", "application/json"}, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"wrong content type", valid, []string{"text/plain"}, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"wrong charset", valid, []string{"application/json; charset=utf-16"}, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"extra media parameter", valid, []string{"application/json; charset=utf-8; profile=settings"}, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"duplicate version", strings.Replace(valid, `"version":1`, `"version":1,"version":2`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"duplicate setting", strings.Replace(valid, `"siteName":"HappyLearn"`, `"siteName":"HappyLearn","siteName":"Other"`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"version case alias", strings.Replace(valid, `"version":1`, `"version":1,"Version":2`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"siteName case alias", strings.Replace(valid, `"siteName":"HappyLearn"`, `"siteName":"HappyLearn","SiteName":"Other"`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"only Version alias", strings.Replace(valid, `"version":1`, `"Version":1`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"only SiteName alias", strings.Replace(valid, `"siteName":"HappyLearn"`, `"SiteName":"HappyLearn"`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"duplicate updatedAt", strings.Replace(valid, `"updatedAt":"2026-07-28T01:00:00Z"`, `"updatedAt":"2026-07-28T01:00:00Z","updatedAt":"2026-07-29T01:00:00Z"`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"null updatedAt", strings.Replace(valid, `"updatedAt":"2026-07-28T01:00:00Z"`, `"updatedAt":null`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"zero updatedAt", strings.Replace(valid, `"updatedAt":"2026-07-28T01:00:00Z"`, `"updatedAt":"0001-01-01T00:00:00Z"`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"invalid updatedAt", strings.Replace(valid, `"updatedAt":"2026-07-28T01:00:00Z"`, `"updatedAt":"not-a-time"`, 1), []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"invalid utf8", invalidUTF8, []string{"application/json"}, http.StatusBadRequest, "settings_invalid"},
		{"too large", tooLarge, []string{"application/json"}, http.StatusRequestEntityTooLarge, "request_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &operationsHTTPStub{}
			handler := NewAdminHandler(stub, nil).Routes()
			request := operationsHTTPRequest(http.MethodPut, "/settings", tc.body, auth.RoleAdmin)
			request.Header.Del("Content-Type")
			for _, value := range tc.contentTypes {
				request.Header.Add("Content-Type", value)
			}
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d puts=%d body=%s", result.Code, tc.wantStatus, stub.puts, result.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if stub.puts != 1 || !stub.lastPut.UpdatedAt.IsZero() {
					t.Fatalf("puts=%d lastPut=%#v", stub.puts, stub.lastPut)
				}
				return
			}
			if stub.puts != 0 {
				t.Fatalf("invalid request reached service: puts=%d", stub.puts)
			}
			assertOperationsErrorEnvelope(t, result, tc.wantCode)
		})
	}
}

func TestOperationsHTTPAuditIsAdminOnlyStrictAndRedacted(t *testing.T) {
	actorID := uuid.MustParse("60000000-0000-4000-8000-000000000003")
	stub := &operationsHTTPStub{page: audit.AuditPage{
		Items: []audit.Record{{
			ID: 42,
			Event: audit.Event{
				ActorUserID: actorID,
				Action:      "operations.settings_rejected",
				TargetType:  "system_settings",
				TargetID:    "private-target.pdf",
				Metadata: map[string]any{
					"status": "succeeded", "reason": "retention", "version": "2",
					"count": "1", "provider_id": uuid.NewString(),
					"model_id": uuid.NewString(), "file_purpose": "ai_attachment",
					"ip": "192.0.2.1", "request_payload": "private",
					"credential": "secret", "object_key": "private/key",
					"filename": "private.pdf", "prompt": "private prompt",
					"response": "private response",
				},
				RequestID: "private-request-id",
				IP:        []byte{192, 0, 2, 1},
			},
			OccurredAt: time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC),
		}},
		NextBeforeID: 41,
	}}
	handler := NewAdminHandler(stub, nil).Routes()
	path := "/audit?action=operations.settings_rejected&targetType=system_settings&outcome=rejected" +
		"&actorId=" + actorID.String() +
		"&from=2026-07-28T00%3A00%3A00Z&to=2026-07-29T00%3A00%3A00Z&beforeId=50&limit=20"
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, operationsHTTPRequest(http.MethodGet, path, "", auth.RoleAdmin))
	if result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	if stub.filter.Action != "operations.settings_rejected" ||
		stub.filter.TargetType != "system_settings" ||
		stub.filter.Outcome != "rejected" || stub.filter.ActorID != actorID ||
		stub.filter.BeforeID != 50 || stub.filter.Limit != 20 ||
		stub.filter.From.IsZero() || stub.filter.To.IsZero() {
		t.Fatalf("filter=%#v", stub.filter)
	}
	body := result.Body.String()
	for _, field := range []string{
		`"id":42`, `"actorId":"` + actorID.String() + `"`,
		`"action":"operations.settings_rejected"`,
		`"targetType":"system_settings"`,
		`"status":"succeeded"`, `"reason":"retention"`, `"version":"2"`,
		`"count":"1"`, `"provider_id":`, `"model_id":`, `"file_purpose":`,
		`"occurredAt":"2026-07-28T03:00:00Z"`, `"nextBeforeId":41`,
	} {
		if !strings.Contains(body, field) {
			t.Fatalf("missing %s in %s", field, body)
		}
	}
	for _, forbidden := range []string{
		"192.0.2.1", "private-request-id", "request_payload", "private",
		"credential", "object_key", "filename", "prompt", "response",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("leaked %q in %s", forbidden, body)
		}
	}

	student := httptest.NewRecorder()
	handler.ServeHTTP(student, operationsHTTPRequest(http.MethodGet, "/audit", "", auth.RoleStudent))
	if student.Code != http.StatusForbidden {
		t.Fatalf("student status=%d body=%s", student.Code, student.Body.String())
	}
	assertOperationsErrorEnvelope(t, student, "forbidden")

	for _, invalid := range []string{
		"/audit?",
		"/audit?unknown=value",
		"/audit?action=",
		"/audit?outcome=unknown",
		"/audit?limit=01",
		"/audit?beforeId=0",
		"/audit?actorId=00000000-0000-0000-0000-000000000000",
		"/audit?actorId=60000000000040008000000000000003",
		"/audit?actorId=60000000-0000-4000-8000-000000000003&actorId=60000000-0000-4000-8000-000000000003",
		"/audit?from=0001-01-01T00%3A00%3A00Z",
		"/audit?to=0001-01-01T00%3A00%3A00Z",
		"/audit?from=2026-07-29T00%3A00%3A00Z&to=2026-07-28T00%3A00%3A00Z",
	} {
		t.Run(invalid, func(t *testing.T) {
			invalidResult := httptest.NewRecorder()
			handler.ServeHTTP(invalidResult, operationsHTTPRequest(http.MethodGet, invalid, "", auth.RoleAdmin))
			if invalidResult.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", invalidResult.Code, invalidResult.Body.String())
			}
			assertOperationsErrorEnvelope(t, invalidResult, "invalid_request")
		})
	}
}

func TestPublicAuditMetadataRejectsSecretsHiddenUnderEveryAllowedKey(t *testing.T) {
	hidden := "private prompt credential private.pdf bucket/private/object"
	metadata := map[string]any{}
	for key := range publicAuditMetadata {
		metadata[key] = hidden
	}
	view := auditRecordView(audit.Record{Event: audit.Event{Metadata: metadata}})
	if len(view.Metadata) != 0 {
		t.Fatalf("unsafe metadata survived: %#v", view.Metadata)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for key := range publicAuditMetadata {
		if strings.Contains(body, `"`+key+`"`) {
			t.Fatalf("unsafe key %q survived in %s", key, body)
		}
	}
	for _, secret := range []string{"prompt", "credential", "private.pdf", "bucket/private/object"} {
		if strings.Contains(body, secret) {
			t.Fatalf("unsafe value %q survived in %s", secret, body)
		}
	}
}

func TestPublicAuditMetadataAllowsOnlyCanonicalPerKeyValues(t *testing.T) {
	providerID := uuid.MustParse("70000000-0000-4000-8000-000000000001")
	modelID := uuid.MustParse("7a000000-0000-4000-8000-000000000002")
	legal := map[string]any{
		"status": "succeeded", "reason": "retention", "version": "2",
		"count": float64(0), "provider_id": providerID.String(),
		"model_id": modelID.String(), "file_purpose": "ai_attachment",
	}
	view := auditRecordView(audit.Record{Event: audit.Event{Metadata: legal}})
	if len(view.Metadata) != len(publicAuditMetadata) {
		t.Fatalf("legal metadata dropped: %#v", view.Metadata)
	}
	for key, want := range legal {
		if got := view.Metadata[key]; got != want {
			t.Fatalf("%s=%#v want=%#v", key, got, want)
		}
	}
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"version", float64(2)},
		{"version", json.Number("2")},
		{"count", "0"},
		{"count", json.Number("0")},
	} {
		t.Run("valid_"+tc.key, func(t *testing.T) {
			got := auditRecordView(audit.Record{Event: audit.Event{
				Metadata: map[string]any{tc.key: tc.value},
			}})
			if value, ok := got.Metadata[tc.key]; !ok || value != tc.value {
				t.Fatalf("canonical %s=%#v dropped or changed: %#v", tc.key, tc.value, got.Metadata)
			}
		})
	}

	for _, tc := range []struct {
		key   string
		value any
	}{
		{"status", "unknown"},
		{"reason", "unknown"},
		{"version", "0"},
		{"version", "01"},
		{"version", "-1"},
		{"version", "1.5"},
		{"version", float64(0)},
		{"version", float64(1.5)},
		{"version", math.NaN()},
		{"version", float64(9007199254740992)},
		{"count", "-1"},
		{"count", "01"},
		{"count", "1.5"},
		{"count", float64(-1)},
		{"count", math.Copysign(0, -1)},
		{"count", float64(1.5)},
		{"count", math.NaN()},
		{"count", float64(1_000_000_001)},
		{"provider_id", uuid.Nil.String()},
		{"provider_id", "70000000000040008000000000000001"},
		{"model_id", strings.ToUpper(modelID.String())},
		{"file_purpose", "bucket/private.pdf"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			got := auditRecordView(audit.Record{Event: audit.Event{
				Metadata: map[string]any{tc.key: tc.value},
			}})
			if _, ok := got.Metadata[tc.key]; ok {
				t.Fatalf("unsafe %s=%#v survived", tc.key, tc.value)
			}
		})
	}
}

func TestOperationsHTTPMethodAndInternalErrorsUseUniformEnvelope(t *testing.T) {
	stub := &operationsHTTPStub{getErr: errors.New("secret database detail")}
	handler := NewAdminHandler(stub, nil).Routes()
	internal := httptest.NewRecorder()
	handler.ServeHTTP(internal, operationsHTTPRequest(http.MethodGet, "/settings", "", auth.RoleAdmin))
	if internal.Code != http.StatusInternalServerError ||
		strings.Contains(internal.Body.String(), "secret") {
		t.Fatalf("internal status=%d body=%s", internal.Code, internal.Body.String())
	}
	assertOperationsErrorEnvelope(t, internal, "internal_error")

	method := httptest.NewRecorder()
	handler.ServeHTTP(method, operationsHTTPRequest(http.MethodPost, "/settings", `{}`, auth.RoleAdmin))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d body=%s", method.Code, method.Body.String())
	}
	assertOperationsErrorEnvelope(t, method, "method_not_allowed")
}

func operationsHTTPRequest(method, target, body string, role auth.Role) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.RemoteAddr = "192.0.2.90:443"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("X-Request-ID", "operations-http-request")
	return request.WithContext(auth.ContextWithUser(request.Context(), auth.User{
		ID:   uuid.MustParse("60000000-0000-4000-8000-000000000010"),
		Role: role, Status: auth.StatusActive,
	}))
}

func validOperationsSettingsJSON() string {
	return `{"version":1,"siteName":"HappyLearn","siteAnnouncement":"","softDeleteRetentionDays":30,"auditRetentionDays":365,"operationalSampleRetentionDays":7,"backupHour":3,"backupMinute":0,"backupTimezone":"Asia/Shanghai","diskWarningPercent":75,"diskCriticalPercent":90,"aiErrorWarningPercent":10,"aiErrorCriticalPercent":25,"processingQueueWarning":20,"processingQueueCritical":100,"updatedAt":"2026-07-28T01:00:00Z"}`
}

func assertOperationsErrorEnvelope(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	body := response.Body.String()
	if !strings.Contains(body, `"error":{`) ||
		!strings.Contains(body, `"code":"`+code+`"`) ||
		!strings.Contains(body, `"message":`) ||
		!strings.Contains(body, `"requestId":`) ||
		response.Header().Get("Cache-Control") != "no-store, private" ||
		response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("headers=%v body=%s", response.Header(), body)
	}
}
