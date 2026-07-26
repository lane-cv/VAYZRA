package app

import (
	"context"
	"github.com/google/uuid"
	"happylearn.local/app/internal/aiqa"
	"net/http"
	"net/http/httptest"
	"testing"
)

type appAI struct{}

func (appAI) ListProviders(context.Context, aiqa.Principal) ([]aiqa.ProviderView, error) {
	return []aiqa.ProviderView{}, nil
}
func (appAI) CreateProvider(context.Context, aiqa.Principal, aiqa.CreateProviderInput) (aiqa.ProviderView, error) {
	return aiqa.ProviderView{}, nil
}
func (appAI) UpdateProvider(context.Context, aiqa.Principal, aiqa.UpdateProviderInput) (aiqa.ProviderView, error) {
	return aiqa.ProviderView{}, nil
}
func (appAI) ActivateProvider(context.Context, aiqa.Principal, uuid.UUID, int64) (aiqa.ProviderView, error) {
	return aiqa.ProviderView{}, nil
}
func (appAI) ListModels(context.Context, aiqa.Principal, uuid.UUID) ([]aiqa.ModelView, error) {
	return nil, nil
}
func (appAI) PutModel(context.Context, aiqa.Principal, aiqa.PutModelInput) (aiqa.ModelView, error) {
	return aiqa.ModelView{}, nil
}
func (appAI) ListPrompts(context.Context, aiqa.Principal) ([]aiqa.PromptView, error) { return nil, nil }
func (appAI) PutPrompt(context.Context, aiqa.Principal, aiqa.PutPromptInput) (aiqa.PromptView, error) {
	return aiqa.PromptView{}, nil
}
func (appAI) GetLimits(context.Context, aiqa.Principal) (aiqa.LimitViews, error) {
	return aiqa.LimitViews{}, nil
}
func (appAI) PutGlobalLimits(context.Context, aiqa.Principal, aiqa.PutLimitsInput) (aiqa.LimitView, error) {
	return aiqa.LimitView{}, nil
}
func (appAI) PutStudentLimits(context.Context, aiqa.Principal, uuid.UUID, aiqa.PutLimitsInput) (aiqa.LimitView, error) {
	return aiqa.LimitView{}, nil
}
func (appAI) TestProvider(context.Context, aiqa.Principal, uuid.UUID) (aiqa.ConnectivityResult, error) {
	return aiqa.ConnectivityResult{}, nil
}

func TestAIRoutes(t *testing.T) {
	h := New(Dependencies{Auth: &appAdminAuth{}, AdminAI: appAI{}})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ai/providers", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
