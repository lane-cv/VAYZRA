package updates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type fakeAgent struct {
	status     Status
	checkCalls int
	applyCalls int
	applyErr   error
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
