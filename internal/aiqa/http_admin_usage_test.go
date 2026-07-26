package aiqa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type usageHTTPStub struct {
	filter UsageFilter
}

func (s *usageHTTPStub) UsageSummary(context.Context, Principal, UsageFilter) (UsageSummary, error) {
	return UsageSummary{Requests: 1, Succeeded: 1, CostMicroUSD: 9007199254740993}, nil
}
func (s *usageHTTPStub) UsageRuns(_ context.Context, _ Principal, filter UsageFilter) ([]UsageRun, UsageCursor, error) {
	s.filter = filter
	return []UsageRun{{ID: uuid.New(), StudentDisplayName: "学生", ModelLabel: "runtime-model", Status: RunSucceeded, CostMicroUSD: 9007199254740993, UsageSource: "upstream", CreatedAt: time.Now().UTC()}}, UsageCursor{}, nil
}

func TestAdminUsageHTTPPrecisionAndPrivateDTO(t *testing.T) {
	service := &usageHTTPStub{}
	h := NewAdminUsageHandler(service, nil).Routes()
	for _, path := range []string{"/summary?status=succeeded&limit=20", "/runs?status=succeeded&limit=20"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, `"costMicroUSD":"9007199254740993"`) {
			t.Fatalf("precision lost: %s", body)
		}
		for _, forbidden := range []string{"prompt", "messageId", "providerBase", "apiKey", "encrypted", "errorDetail"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("leaked %q: %s", forbidden, body)
			}
		}
	}
}

func TestAdminUsageHTTPRoleAndStrictQuery(t *testing.T) {
	h := NewAdminUsageHandler(&usageHTTPStub{}, nil).Routes()
	cases := []struct {
		path string
		role auth.Role
		want int
	}{
		{"/runs", auth.RoleStudent, http.StatusForbidden},
		{"/runs?limit=01", auth.RoleAdmin, http.StatusBadRequest},
		{"/runs?unknown=x", auth.RoleAdmin, http.StatusBadRequest},
		{"/runs?cursor=", auth.RoleAdmin, http.StatusBadRequest},
		{"/summary?from=2026-07-27", auth.RoleAdmin, http.StatusBadRequest},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: tc.role, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s status=%d body=%s", tc.path, w.Code, w.Body.String())
		}
	}
}
