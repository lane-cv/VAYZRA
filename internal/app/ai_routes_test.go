package app

import (
	"context"
	"github.com/google/uuid"
	"happylearn.local/app/internal/aiqa"
	"happylearn.local/app/internal/platform/redisx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type appAI struct {
	testErr   error
	testCalls int
}

type appStudentAI struct{}

type appAISummaries struct{}

func (*appAISummaries) ListQuestionSummaries(context.Context, aiqa.Principal, aiqa.SummaryFilter) ([]aiqa.QuestionSummary, aiqa.SummaryCursor, error) {
	return []aiqa.QuestionSummary{}, aiqa.SummaryCursor{}, nil
}

type appAIUsage struct{}

func (*appAIUsage) UsageSummary(context.Context, aiqa.Principal, aiqa.UsageFilter) (aiqa.UsageSummary, error) {
	return aiqa.UsageSummary{}, nil
}
func (*appAIUsage) UsageRuns(context.Context, aiqa.Principal, aiqa.UsageFilter) ([]aiqa.UsageRun, aiqa.UsageCursor, error) {
	return []aiqa.UsageRun{}, aiqa.UsageCursor{}, nil
}

func (*appStudentAI) CreateThread(context.Context, aiqa.Principal, aiqa.CreateThreadInput) (aiqa.ThreadDetail, aiqa.Run, error) {
	return aiqa.ThreadDetail{}, aiqa.Run{}, aiqa.ErrNotFound
}
func (*appStudentAI) ListThreads(context.Context, aiqa.Principal, aiqa.ThreadCursor) ([]aiqa.Thread, aiqa.ThreadCursor, error) {
	return []aiqa.Thread{}, aiqa.ThreadCursor{}, nil
}
func (*appStudentAI) GetThread(context.Context, aiqa.Principal, uuid.UUID, aiqa.MessageCursor) (aiqa.ThreadDetail, error) {
	return aiqa.ThreadDetail{}, aiqa.ErrNotFound
}
func (*appStudentAI) AddMessage(context.Context, aiqa.Principal, aiqa.AddMessageInput) (aiqa.ThreadDetail, aiqa.Run, error) {
	return aiqa.ThreadDetail{}, aiqa.Run{}, aiqa.ErrNotFound
}
func (*appStudentAI) CancelRun(context.Context, aiqa.Principal, uuid.UUID) (aiqa.Run, error) {
	return aiqa.Run{}, aiqa.ErrNotFound
}
func (*appStudentAI) RetryRun(context.Context, aiqa.Principal, uuid.UUID, string) (aiqa.Run, error) {
	return aiqa.Run{}, aiqa.ErrNotFound
}

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
func (s *appAI) TestProvider(context.Context, aiqa.Principal, uuid.UUID) (aiqa.ConnectivityResult, error) {
	s.testCalls++
	return aiqa.ConnectivityResult{OK: s.testErr == nil, Protocol: aiqa.ProtocolResponses}, s.testErr
}

func TestAIRoutes(t *testing.T) {
	h := New(Dependencies{Auth: &appAdminAuth{}, AdminAI: &appAI{}})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ai/providers", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAIRoutesStudentAndAdminWriteSurfacesStayIsolated(t *testing.T) {
	t.Run("student reaches only student AI", func(t *testing.T) {
		h := New(Dependencies{Auth: &appStudentAuth{}, StudentAI: &appStudentAI{}})
		r := httptest.NewRequest(http.MethodGet, "/api/v1/student/ai/threads", nil)
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	})
	t.Run("admin cannot create student AI thread", func(t *testing.T) {
		h := New(Dependencies{Auth: &appAdminAuth{}, StudentAI: &appStudentAI{}, AdminAI: &appAI{}})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/student/ai/threads", strings.NewReader(`{"title":"x","subject":"math","body":"x","attachments":[]}`))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	})
	t.Run("student cannot reach admin AI", func(t *testing.T) {
		h := New(Dependencies{Auth: &appStudentAuth{}, StudentAI: &appStudentAI{}, AdminAI: &appAI{}})
		r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ai/providers", nil)
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	})
}

func TestAIRoutesUnifiedStudentSummaryAndAdminUsageReads(t *testing.T) {
	t.Run("student unified summaries", func(t *testing.T) {
		h := New(Dependencies{Auth: &appStudentAuth{}, StudentAISummaries: &appAISummaries{}})
		r := httptest.NewRequest(http.MethodGet, "/api/v1/student/question-summaries", nil)
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("student cannot reach usage", func(t *testing.T) {
		h := New(Dependencies{Auth: &appStudentAuth{}, AdminAIUsage: &appAIUsage{}})
		r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ai/usage/summary", nil)
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("admin usage", func(t *testing.T) {
		h := New(Dependencies{Auth: &appAdminAuth{}, AdminAI: &appAI{}, AdminAIUsage: &appAIUsage{}})
		r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ai/usage/runs", nil)
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

type appProviderTestLimiter struct {
	decision redisx.ResourceDecision
	calls    int
}

func (l *appProviderTestLimiter) AllowProviderTest(context.Context, uuid.UUID) (redisx.ResourceDecision, error) {
	l.calls++
	return l.decision, nil
}

func allowMountedProviderTests() *appProviderTestLimiter {
	return &appProviderTestLimiter{decision: redisx.ResourceDecision{Allowed: true}}
}

func TestMountedAIProviderTestAuthRoleMissingCSRFLimiterAndNotFound(t *testing.T) {
	providerID := uuid.NewString()
	request := func(withSession, withCSRF bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/ai/providers/"+providerID+"/test", nil)
		r.Header.Set("Origin", "https://learn.example.com")
		if withSession {
			r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		}
		if withCSRF {
			r.Header.Set("X-CSRF-Token", "ai-provider-test-csrf")
			r.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "ai-provider-test-csrf"})
		}
		return r
	}

	t.Run("unauthenticated", func(t *testing.T) {
		service := &appAI{}
		h := New(Dependencies{Auth: &appFakeAuth{}, AdminAI: service, ProviderTestLimiter: allowMountedProviderTests(), PublicOrigin: "https://learn.example.com"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(false, true))
		if w.Code != http.StatusUnauthorized || service.testCalls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", w.Code, service.testCalls, w.Body.String())
		}
	})
	t.Run("non-admin", func(t *testing.T) {
		service := &appAI{}
		h := New(Dependencies{Auth: &appStudentAuth{}, AdminAI: service, ProviderTestLimiter: allowMountedProviderTests(), PublicOrigin: "https://learn.example.com"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(true, true))
		if w.Code != http.StatusForbidden || service.testCalls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", w.Code, service.testCalls, w.Body.String())
		}
	})
	t.Run("missing provider", func(t *testing.T) {
		service := &appAI{testErr: aiqa.ErrNotFound}
		h := New(Dependencies{Auth: &appAdminAuth{}, AdminAI: service, ProviderTestLimiter: allowMountedProviderTests(), PublicOrigin: "https://learn.example.com"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(true, true))
		if w.Code != http.StatusNotFound || service.testCalls != 1 {
			t.Fatalf("status=%d calls=%d body=%s", w.Code, service.testCalls, w.Body.String())
		}
	})
	t.Run("csrf", func(t *testing.T) {
		service := &appAI{}
		h := New(Dependencies{Auth: &appAdminAuth{}, AdminAI: service, ProviderTestLimiter: allowMountedProviderTests(), PublicOrigin: "https://learn.example.com"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(true, false))
		if w.Code != http.StatusForbidden || service.testCalls != 0 || !strings.Contains(w.Body.String(), `"csrf_invalid"`) {
			t.Fatalf("status=%d calls=%d body=%s", w.Code, service.testCalls, w.Body.String())
		}
	})
	t.Run("paid probe rate limit", func(t *testing.T) {
		service := &appAI{}
		limiter := &appProviderTestLimiter{decision: redisx.ResourceDecision{RetryAfter: 2 * time.Second}}
		h := New(Dependencies{Auth: &appAdminAuth{}, AdminAI: service, ProviderTestLimiter: limiter, PublicOrigin: "https://learn.example.com"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(true, true))
		if w.Code != http.StatusTooManyRequests || service.testCalls != 0 || limiter.calls != 1 {
			t.Fatalf("status=%d service=%d limiter=%d body=%s", w.Code, service.testCalls, limiter.calls, w.Body.String())
		}
	})
}
