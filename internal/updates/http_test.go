package updates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type fakeAgent struct {
	status        Status
	checkCalls    int
	applyCalls    int
	rollbackCalls int
	applyErr      error
	rollbackErr   error
}

func (f *fakeAgent) Status(context.Context) (Status, error) { return f.status, nil }

func (f *fakeAgent) Check(context.Context) (Status, error) {
	f.checkCalls++
	return f.status, nil
}

func (f *fakeAgent) Apply(context.Context) (Status, error) {
	f.applyCalls++
	if f.applyErr != nil {
		return Status{}, f.applyErr
	}
	return f.status, nil
}

func (f *fakeAgent) Rollback(context.Context) (Status, error) {
	f.rollbackCalls++
	if f.rollbackErr != nil {
		return Status{}, f.rollbackErr
	}
	return f.status, nil
}

func activeAdmin() auth.User {
	return auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}
}

func updateRequest(handler http.Handler, method, target string, user auth.User) *httptest.ResponseRecorder {
	record := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, nil)
	request = request.WithContext(auth.ContextWithUser(request.Context(), user))
	handler.ServeHTTP(record, request)
	return record
}

func TestAdminHandlerAllowsActiveAdminsToCheckAndApply(t *testing.T) {
	agent := &fakeAgent{status: Status{
		Enabled: true, State: StateAvailable, Ref: "master",
		CurrentCommit:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LatestCommit:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		UpdateAvailable: true,
		CanRollback:     true,
	}}
	service, err := NewService(agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAdminHandler(service, nil).Routes()
	admin := activeAdmin()

	check := updateRequest(handler, http.MethodPost, "/check", admin)
	if check.Code != http.StatusOK || agent.checkCalls != 1 {
		t.Fatalf("check status=%d calls=%d", check.Code, agent.checkCalls)
	}
	apply := updateRequest(handler, http.MethodPost, "/apply", admin)
	if apply.Code != http.StatusAccepted || agent.applyCalls != 1 {
		t.Fatalf("apply status=%d calls=%d", apply.Code, agent.applyCalls)
	}
	var envelope struct {
		Data Status `json:"data"`
	}
	if err := json.Unmarshal(apply.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.State != StateAvailable {
		t.Fatalf("unexpected state %q", envelope.Data.State)
	}
	rollback := updateRequest(handler, http.MethodPost, "/rollback", admin)
	if rollback.Code != http.StatusAccepted || agent.rollbackCalls != 1 {
		t.Fatalf("rollback status=%d calls=%d", rollback.Code, agent.rollbackCalls)
	}
}

func TestAdminHandlerReportsUnavailableRollbackClearly(t *testing.T) {
	agent := &fakeAgent{rollbackErr: ErrRollbackUnavailable}
	service, err := NewService(agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := updateRequest(NewAdminHandler(service, nil).Routes(), http.MethodPost, "/rollback", activeAdmin())
	if response.Code != http.StatusConflict {
		t.Fatalf("rollback status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "rollback_unavailable") {
		t.Fatalf("rollback body=%s", response.Body.String())
	}
}

func TestAdminHandlerReportsOutdatedAgentProtocolClearly(t *testing.T) {
	agent := &fakeAgent{status: Status{LegacyProtocol: true}, applyErr: ErrAgentProtocolOutdated}
	service, err := NewService(agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := updateRequest(NewAdminHandler(service, nil).Routes(), http.MethodPost, "/apply", activeAdmin())
	if response.Code != http.StatusConflict || agent.applyCalls != 0 {
		t.Fatalf("apply status=%d calls=%d body=%s", response.Code, agent.applyCalls, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "update_agent_protocol_outdated") ||
		!strings.Contains(response.Body.String(), "宿主机完整重新部署") {
		t.Fatalf("apply body=%s", response.Body.String())
	}
}

func TestAdminHandlerRejectsNonAdminsAndDirtyApply(t *testing.T) {
	agent := &fakeAgent{applyErr: ErrDirtyCheckout}
	service, err := NewService(agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewAdminHandler(service, nil).Routes()

	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	if response := updateRequest(handler, http.MethodGet, "/status", student); response.Code != http.StatusForbidden {
		t.Fatalf("student status=%d", response.Code)
	}
	if response := updateRequest(handler, http.MethodPost, "/apply", activeAdmin()); response.Code != http.StatusPreconditionFailed {
		t.Fatalf("dirty apply status=%d", response.Code)
	}
	if !errors.Is(agent.applyErr, ErrDirtyCheckout) {
		t.Fatal("test setup lost dirty error")
	}
}
