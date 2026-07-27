package aiqa

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/redisx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type httpConfigService struct {
	created        CreateProviderInput
	err            error
	emptyProviders bool
	testResult     ConnectivityResult
	testErr        error
	testCalls      int
}

func (s *httpConfigService) ListProviders(context.Context, Principal) ([]ProviderView, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.emptyProviders {
		return nil, nil
	}
	return []ProviderView{{ID: uuid.New(), Name: "P", BaseURL: "https://api.example.test", HasKey: true}}, nil
}

func TestAdminConfigHTTPEmptyCollectionsAreArrays(t *testing.T) {
	h := NewAdminConfigHandler(&httpConfigService{emptyProviders: true}, nil).Routes()
	for _, path := range []string{"/providers", "/providers/" + uuid.NewString() + "/models", "/prompts"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "192.0.2.1:1234"
		r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":[]`) {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestAdminConfigHTTPMapsUnknownServiceFailureToInternalError(t *testing.T) {
	h := NewAdminConfigHandler(&httpConfigService{err: errors.New("database secret detail")}, nil).Routes()
	r := httptest.NewRequest(http.MethodGet, "/providers", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"code":"internal_error"`) || strings.Contains(w.Body.String(), "database secret detail") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminConfigHTTPRejectsRoleAndStrictBoundaries(t *testing.T) {
	h := NewAdminConfigHandler(&httpConfigService{}, nil).Routes()
	cases := []struct {
		method, path, body string
		role               auth.Role
		headers            []string
		want               int
	}{
		{http.MethodGet, "/providers", "", auth.RoleStudent, nil, 403},
		{http.MethodGet, "/providers/not-a-uuid/models", "", auth.RoleAdmin, nil, 400},
		{http.MethodPost, "/providers", `{"name":"P","baseUrl":"https://api.example.test","apiKey":"x","protocolMode":"responses"}`, auth.RoleAdmin, []string{"application/json", "application/json"}, 400},
		{http.MethodPost, "/providers", `{"name":"P","baseUrl":"https://api.example.test","apiKey":"x","protocolMode":"responses"}`, auth.RoleAdmin, []string{"application/json"}, 400},
		{http.MethodPut, "/prompts/not-valid", `{"body":"x","expectedVersion":1}`, auth.RoleAdmin, []string{"application/json"}, 400},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		for _, v := range tc.headers {
			r.Header.Add("Content-Type", v)
		}
		r.RemoteAddr = "192.0.2.1:1234"
		r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: tc.role, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
func (s *httpConfigService) CreateProvider(_ context.Context, _ Principal, in CreateProviderInput) (ProviderView, error) {
	s.created = in
	return ProviderView{ID: uuid.New(), Name: in.Name, BaseURL: in.BaseURL, HasKey: true}, nil
}
func (*httpConfigService) UpdateProvider(context.Context, Principal, UpdateProviderInput) (ProviderView, error) {
	return ProviderView{}, nil
}
func (*httpConfigService) ActivateProvider(context.Context, Principal, uuid.UUID, int64) (ProviderView, error) {
	return ProviderView{}, nil
}
func (*httpConfigService) ListModels(context.Context, Principal, uuid.UUID) ([]ModelView, error) {
	return nil, nil
}
func (*httpConfigService) PutModel(context.Context, Principal, PutModelInput) (ModelView, error) {
	return ModelView{}, nil
}
func (*httpConfigService) ListPrompts(context.Context, Principal) ([]PromptView, error) {
	return nil, nil
}
func (*httpConfigService) PutPrompt(context.Context, Principal, PutPromptInput) (PromptView, error) {
	return PromptView{}, nil
}
func (*httpConfigService) GetLimits(context.Context, Principal) (LimitViews, error) {
	return LimitViews{}, nil
}
func (*httpConfigService) PutGlobalLimits(context.Context, Principal, PutLimitsInput) (LimitView, error) {
	return LimitView{}, nil
}
func (*httpConfigService) PutStudentLimits(context.Context, Principal, uuid.UUID, PutLimitsInput) (LimitView, error) {
	return LimitView{}, nil
}
func (s *httpConfigService) TestProvider(context.Context, Principal, uuid.UUID) (ConnectivityResult, error) {
	s.testCalls++
	return s.testResult, s.testErr
}

func TestProviderTestHTTPSuccessAndFailureAreSanitized(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name   string
		result ConnectivityResult
		err    error
		status int
		code   string
	}{
		{"success", ConnectivityResult{OK: true, Protocol: ProtocolResponses, LatencyMS: 12}, nil, http.StatusOK, ""},
		{"failed", ConnectivityResult{Protocol: ProtocolResponses, LatencyMS: 15, ErrorCategory: "auth"}, ErrProviderUnavailable, http.StatusServiceUnavailable, "PROVIDER_UNAVAILABLE"},
		{"busy", ConnectivityResult{Protocol: ProtocolResponses, ErrorCategory: "busy"}, ErrProviderTestBusy, http.StatusConflict, "PROVIDER_UNAVAILABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewAdminConfigHandlerWithConfig(&httpConfigService{testResult: tc.result, testErr: tc.err}, AdminConfigHTTPConfig{ProviderTestLimiter: allowProviderTests()}).Routes()
			r := httptest.NewRequest(http.MethodPost, "/providers/"+id.String()+"/test", nil)
			r.RemoteAddr = "192.0.2.1:1234"
			r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			var decoded map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode response: %v body=%s", err, body)
			}
			if len(decoded) != 4 {
				t.Fatalf("response keys=%v", decoded)
			}
			for _, key := range []string{"ok", "protocol", "latencyMs", "errorCategory"} {
				if _, ok := decoded[key]; !ok {
					t.Fatalf("missing %q: %v", key, decoded)
				}
			}
			if decoded["ok"] != tc.result.OK || decoded["protocol"] != string(tc.result.Protocol) ||
				decoded["latencyMs"] != float64(tc.result.LatencyMS) || decoded["errorCategory"] != tc.result.ErrorCategory {
				t.Fatalf("response=%v want=%#v", decoded, tc.result)
			}
			if tc.code != "" && w.Header().Get("X-Error-Code") != tc.code {
				t.Fatalf("error code header=%q body=%s", w.Header().Get("X-Error-Code"), body)
			}
			for _, secret := range []string{"Authorization", "apiKey", "encrypted", "raw-upstream", "requestBody"} {
				if strings.Contains(body, secret) {
					t.Fatalf("response leaked %q: %s", secret, body)
				}
			}
		})
	}
}

type fakeProviderTestLimiter struct {
	decision redisx.ResourceDecision
	err      error
	calls    int
}

func allowProviderTests() *fakeProviderTestLimiter {
	return &fakeProviderTestLimiter{decision: redisx.ResourceDecision{Allowed: true}}
}

func (l *fakeProviderTestLimiter) AllowProviderTest(context.Context, uuid.UUID) (redisx.ResourceDecision, error) {
	l.calls++
	return l.decision, l.err
}

func TestProviderTestHTTPRateLimitRejectsBeforeService(t *testing.T) {
	service := &httpConfigService{}
	limiter := &fakeProviderTestLimiter{decision: redisx.ResourceDecision{RetryAfter: 3 * time.Second}}
	h := NewAdminConfigHandlerWithConfig(service, AdminConfigHTTPConfig{ProviderTestLimiter: limiter}).Routes()
	r := httptest.NewRequest(http.MethodPost, "/providers/"+uuid.NewString()+"/test", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3" || !strings.Contains(w.Body.String(), `"code":"rate_limited"`) {
		t.Fatalf("status=%d retry=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	if limiter.calls != 1 || service.testCalls != 0 {
		t.Fatalf("limiter calls=%d service calls=%d", limiter.calls, service.testCalls)
	}
}

func TestProviderTestHTTPFailsClosedWithoutLimiter(t *testing.T) {
	service := &httpConfigService{testResult: ConnectivityResult{OK: true, Protocol: ProtocolResponses}}
	h := NewAdminConfigHandler(service, nil).Routes()
	r := httptest.NewRequest(http.MethodPost, "/providers/"+uuid.NewString()+"/test", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"PROVIDER_UNAVAILABLE"`) || service.testCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, service.testCalls, w.Body.String())
	}
}

func TestProviderTestHTTPMapsMissingAndNonAdmin(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		name   string
		role   auth.Role
		err    error
		status int
	}{
		{"missing", auth.RoleAdmin, ErrNotFound, http.StatusNotFound},
		{"non-admin", auth.RoleStudent, nil, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &httpConfigService{testErr: tc.err}
			h := NewAdminConfigHandlerWithConfig(service, AdminConfigHTTPConfig{ProviderTestLimiter: allowProviderTests()}).Routes()
			r := httptest.NewRequest(http.MethodPost, "/providers/"+id+"/test", nil)
			r.RemoteAddr = "192.0.2.1:1234"
			r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: tc.role, Status: auth.StatusActive}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
func TestAdminConfigHTTPStrictCreateAndRedaction(t *testing.T) {
	svc := &httpConfigService{}
	h := NewAdminConfigHandler(svc, nil).Routes()
	r := httptest.NewRequest(http.MethodPost, "/providers", strings.NewReader(`{"name":"P","baseUrl":"https://api.example.test","apiKey":"test-secret","protocolMode":"responses","ignored":true}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	r.RemoteAddr = "192.0.2.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/providers", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	r.RemoteAddr = "192.0.2.1:1234"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"hasKey":true`) || strings.Contains(w.Body.String(), "encryptedApiKey") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestAdminConfigHTTPDispatchesAllRoutes(t *testing.T) {
	h := NewAdminConfigHandler(&httpConfigService{}, nil).Routes()
	id := uuid.NewString()
	cases := []struct {
		method, path, body string
		want               int
	}{{"GET", "/providers", "", 200}, {"POST", "/providers", `{"name":"p","baseUrl":"https://p.test","apiKey":"secret","protocolMode":"responses"}`, 201}, {"PUT", "/providers/" + id, `{"name":"p","baseUrl":"https://p.test","protocolMode":"responses","expectedVersion":1}`, 200}, {"PUT", "/active-provider", `{"providerId":"` + id + `","expectedVersion":1}`, 200}, {"GET", "/providers/" + id + "/models", "", 200}, {"PUT", "/providers/" + id + "/models/" + uuid.NewString(), `{"upstreamModelId":"m","modality":"text","contextTokens":10,"maxOutputTokens":5,"imageQuotaTokens":1,"inputPriceMicroUsd":0,"outputPriceMicroUsd":0,"connectTimeoutMs":5000,"responseHeaderTimeoutMs":30000,"idleStreamTimeoutMs":30000,"totalTimeoutMs":120000,"enabled":true,"expectedVersion":0}`, 200}, {"GET", "/prompts", "", 200}, {"PUT", "/prompts/math", `{"body":"p","expectedVersion":0}`, 200}, {"GET", "/limits", "", 200}, {"PUT", "/limits/global", `{"dailyRequests":{"mode":"disabled"},"monthlyRequests":{"mode":"disabled"},"dailyTokens":{"mode":"disabled"},"monthlyTokens":{"mode":"disabled"},"expectedVersion":1}`, 200}, {"PUT", "/limits/students/" + id, `{"dailyRequests":{"mode":"inherit"},"monthlyRequests":{"mode":"inherit"},"dailyTokens":{"mode":"inherit"},"monthlyTokens":{"mode":"inherit"},"expectedVersion":0}`, 200}}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if tc.method == http.MethodPost {
			r.Header.Set("Idempotency-Key", "1234567890abcdef")
		}
		r.RemoteAddr = "192.0.2.1:1234"
		r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s=%d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
