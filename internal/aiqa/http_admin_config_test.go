package aiqa

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type httpConfigService struct {
	created CreateProviderInput
	err     error
}

func (s *httpConfigService) ListProviders(context.Context, Principal) ([]ProviderView, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []ProviderView{{ID: uuid.New(), Name: "P", BaseURL: "https://api.example.test", HasKey: true}}, nil
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
	}{{"GET", "/providers", "", 200}, {"POST", "/providers", `{"name":"p","baseUrl":"https://p.test","apiKey":"secret","protocolMode":"responses"}`, 201}, {"PUT", "/providers/" + id, `{"name":"p","baseUrl":"https://p.test","protocolMode":"responses","expectedVersion":1}`, 200}, {"PUT", "/active-provider", `{"providerId":"` + id + `","expectedVersion":1}`, 200}, {"GET", "/providers/" + id + "/models", "", 200}, {"PUT", "/providers/" + id + "/models/" + uuid.NewString(), `{"upstreamModelId":"m","modality":"text","contextTokens":10,"maxOutputTokens":5,"imageQuotaTokens":1,"inputPriceMicroUsd":0,"outputPriceMicroUsd":0,"enabled":true,"expectedVersion":0}`, 200}, {"GET", "/prompts", "", 200}, {"PUT", "/prompts/math", `{"body":"p","expectedVersion":0}`, 200}, {"GET", "/limits", "", 200}, {"PUT", "/limits/global", `{"dailyRequests":{"mode":"disabled"},"monthlyRequests":{"mode":"disabled"},"dailyTokens":{"mode":"disabled"},"monthlyTokens":{"mode":"disabled"},"expectedVersion":1}`, 200}, {"PUT", "/limits/students/" + id, `{"dailyRequests":{"mode":"inherit"},"monthlyRequests":{"mode":"inherit"},"dailyTokens":{"mode":"inherit"},"monthlyTokens":{"mode":"inherit"},"expectedVersion":0}`, 200}}
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
