package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/aiqa"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/qanda"
)

type serverStudentAI struct{}

type serverAIReads struct{}

func (*serverAIReads) ListQuestionSummaries(context.Context, aiqa.Principal, aiqa.SummaryFilter) ([]aiqa.QuestionSummary, aiqa.SummaryCursor, error) {
	return []aiqa.QuestionSummary{}, aiqa.SummaryCursor{}, nil
}
func (*serverAIReads) UsageSummary(context.Context, aiqa.Principal, aiqa.UsageFilter) (aiqa.UsageSummary, error) {
	return aiqa.UsageSummary{}, nil
}
func (*serverAIReads) UsageRuns(context.Context, aiqa.Principal, aiqa.UsageFilter) ([]aiqa.UsageRun, aiqa.UsageCursor, error) {
	return []aiqa.UsageRun{}, aiqa.UsageCursor{}, nil
}

func (*serverStudentAI) CreateThread(context.Context, aiqa.Principal, aiqa.CreateThreadInput) (aiqa.ThreadDetail, aiqa.Run, error) {
	return aiqa.ThreadDetail{}, aiqa.Run{}, aiqa.ErrNotFound
}
func (*serverStudentAI) ListThreads(context.Context, aiqa.Principal, aiqa.ThreadCursor) ([]aiqa.Thread, aiqa.ThreadCursor, error) {
	return []aiqa.Thread{}, aiqa.ThreadCursor{}, nil
}
func (*serverStudentAI) GetThread(context.Context, aiqa.Principal, uuid.UUID, aiqa.MessageCursor) (aiqa.ThreadDetail, error) {
	return aiqa.ThreadDetail{}, aiqa.ErrNotFound
}
func (*serverStudentAI) AddMessage(context.Context, aiqa.Principal, aiqa.AddMessageInput) (aiqa.ThreadDetail, aiqa.Run, error) {
	return aiqa.ThreadDetail{}, aiqa.Run{}, aiqa.ErrNotFound
}
func (*serverStudentAI) CancelRun(context.Context, aiqa.Principal, uuid.UUID) (aiqa.Run, error) {
	return aiqa.Run{}, aiqa.ErrNotFound
}
func (*serverStudentAI) RetryRun(context.Context, aiqa.Principal, uuid.UUID, string) (aiqa.Run, error) {
	return aiqa.Run{}, aiqa.ErrNotFound
}
func (*serverStudentAI) RunStreamState(context.Context, aiqa.Principal, uuid.UUID) (aiqa.RunStreamState, error) {
	return aiqa.RunStreamState{}, aiqa.ErrNotFound
}
func (*serverStudentAI) ListRunEvents(context.Context, aiqa.Principal, uuid.UUID, int64, int64, int) ([]aiqa.RunEvent, error) {
	return nil, aiqa.ErrNotFound
}

type serverAIFileAccess struct{}

func (*serverAIFileAccess) Status(_ context.Context, _ files.Principal, id uuid.UUID) (files.AIFileStatus, error) {
	return files.AIFileStatus{FileVersionID: id, ProcessingState: "ready", DetectedMIME: "text/plain", Size: 5, PreviewAvailable: true}, nil
}
func (*serverAIFileAccess) Open(context.Context, files.Principal, files.AIOpenInput) (files.OpenedFile, error) {
	return files.OpenedFile{}, files.ErrNotFound
}
func (*serverAIFileAccess) Reject(context.Context, files.Principal, uuid.UUID, string) error {
	return nil
}

func TestBuildApplicationWiresStudentAIAndControlledFileFactories(t *testing.T) {
	studentFactory, fileFactory := false, false
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverStudentAuth{}, nil },
		newStudentAI: func(context.Context, *pgxpool.Pool, config.Config) (aiqa.StudentService, aiqa.StudentEventStore, error) {
			studentFactory = true
			service := &serverStudentAI{}
			return service, service, nil
		},
		newAIFileAccess: func(context.Context, *pgxpool.Pool, config.Config) (files.AIAccessHTTPService, error) {
			fileFactory = true
			return &serverAIFileAccess{}, nil
		},
		ready: func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close: func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeResources)
	for _, path := range []string{"/api/v1/student/ai/threads", "/api/v1/ai-question-files/" + uuid.NewString() + "/status"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body)
		}
	}
	if !studentFactory || !fileFactory {
		t.Fatalf("studentFactory=%t fileFactory=%t", studentFactory, fileFactory)
	}
}

func TestBuildApplicationWiresAIReadFactories(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth auth.HTTPService
		path string
	}{
		{"student summary", serverStudentAuth{}, "/api/v1/student/question-summaries"},
		{"admin usage", serverAdminAuth{}, "/api/v1/admin/ai/usage/summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
				open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
				migrate: func(context.Context, *pgxpool.Pool) error { return nil },
				newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return tc.auth, nil },
				newAIReads: func(*pgxpool.Pool) (aiqa.SummaryService, aiqa.AdminUsageService) {
					called = true
					reads := &serverAIReads{}
					return reads, reads
				},
				ready: func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
				close: func(*pgxpool.Pool) {},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closeResources)
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if !called || w.Code != http.StatusOK {
				t.Fatalf("called=%t status=%d body=%s", called, w.Code, w.Body.String())
			}
		})
	}
}

func TestBuildApplicationWiresAuthRoutesAndConfiguredSecurity(t *testing.T) {
	var openedURL string
	migrated := false
	closed := false
	h, closeResources, err := buildApplication(context.Background(), config.Config{
		DatabaseURL:  "postgres://app:secret@db.example/happylearn",
		PublicOrigin: "https://learn.example.com",
		CookieSecure: true,
	}, applicationDependencies{
		open:    func(_ context.Context, url string) (*pgxpool.Pool, error) { openedURL = url; return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { migrated = true; return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close:   func(*pgxpool.Pool) { closed = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if openedURL != "postgres://app:secret@db.example/happylearn" || !migrated {
		t.Fatalf("opened=%q migrated=%t", openedURL, migrated)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://learn.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !hasSecureSessionCookie(w.Result().Cookies()) {
		t.Fatal("login route did not receive CookieSecure configuration")
	}
	closeResources()
	if !closed {
		t.Fatal("resources not closed")
	}
}

func TestBuildApplicationForwardsTrustedProxyCIDRs(t *testing.T) {
	svc := &serverCapturingAuth{}
	h, closeResources, err := buildApplication(context.Background(), config.Config{
		DatabaseURL:       "postgres://app:secret@db.example/happylearn",
		PublicOrigin:      "https://learn.example.com",
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return svc, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close:   func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeResources)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.RemoteAddr = "10.1.2.3:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.4")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://learn.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK || svc.loginInput.IP == nil || svc.loginInput.IP.String() != "198.51.100.4" {
		t.Fatalf("status=%d input=%#v body=%s", w.Code, svc.loginInput, w.Body.String())
	}
}

func TestBuildApplicationWiresStudentQuestionRoutes(t *testing.T) {
	questions := &serverStudentQuestions{}
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:         func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate:      func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth:      func(*pgxpool.Pool) (auth.HTTPService, error) { return serverStudentAuth{}, nil },
		newQuestions: func(*pgxpool.Pool) qanda.HTTPServices { return qanda.HTTPServices{Student: questions} },
		ready:        func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close:        func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeResources)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/student/questions", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || questions.lists != 1 {
		t.Fatalf("status=%d lists=%d body=%s", w.Code, questions.lists, w.Body.String())
	}
}

func TestBuildApplicationWiresAdminQuestionRoutes(t *testing.T) {
	questions := &serverAdminQuestions{}
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{open: func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil }, migrate: func(context.Context, *pgxpool.Pool) error { return nil }, newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverAdminAuth{}, nil }, newQuestions: func(*pgxpool.Pool) qanda.HTTPServices { return qanda.HTTPServices{Admin: questions} }, ready: func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } }, close: func(*pgxpool.Pool) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeResources)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/questions", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || questions.lists != 1 {
		t.Fatalf("status=%d lists=%d body=%s", w.Code, questions.lists, w.Body.String())
	}
}

func TestBuildApplicationWiresQuestionUploadFactory(t *testing.T) {
	called := false
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open: func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil }, migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverStudentAuth{}, nil },
		newQAUploads: func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error) {
			called = true
			return &serverUploadCleaner{}, nil
		},
		ready: func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } }, close: func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeResources)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/student/question-uploads/"+uuid.NewString(), nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("called=%t status=%d body=%s", called, w.Code, w.Body.String())
	}
}

type serverAdminAuth struct{ serverFakeAuth }

func (serverAdminAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}, nil
}

type serverAdminQuestions struct{ lists int }

func (s *serverAdminQuestions) ListAdminThreads(context.Context, qanda.Principal, qanda.AdminThreadFilter, qanda.ThreadCursor) ([]qanda.Thread, qanda.ThreadCursor, error) {
	s.lists++
	return []qanda.Thread{}, qanda.ThreadCursor{}, nil
}
func (*serverAdminQuestions) GetAdminThread(context.Context, qanda.Principal, uuid.UUID, qanda.MessageCursor) (qanda.AdminThreadDetail, error) {
	return qanda.AdminThreadDetail{}, nil
}
func (*serverAdminQuestions) AddAdminMessage(context.Context, qanda.Principal, qanda.AddAdminMessageInput) (qanda.Thread, qanda.Message, error) {
	return qanda.Thread{}, qanda.Message{}, nil
}
func (*serverAdminQuestions) ChangeStatus(context.Context, qanda.Principal, qanda.ChangeStatusInput) (qanda.Thread, error) {
	return qanda.Thread{}, nil
}
func (*serverAdminQuestions) AddTeacherNote(context.Context, qanda.Principal, qanda.AddTeacherNoteInput) (qanda.TeacherNote, error) {
	return qanda.TeacherNote{}, nil
}

type serverStudentAuth struct{ serverFakeAuth }

func (serverStudentAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{User: auth.User{ID: studentHTTPServerUser, Role: auth.RoleStudent, Status: auth.StatusActive}}, nil
}

var studentHTTPServerUser = uuid.MustParse("50000000-0000-4000-8000-000000000005")

type serverStudentQuestions struct{ lists int }

func (*serverStudentQuestions) CreateThread(context.Context, qanda.Principal, qanda.CreateThreadInput) (qanda.Thread, qanda.Message, error) {
	return qanda.Thread{}, qanda.Message{}, nil
}
func (s *serverStudentQuestions) ListStudentThreads(context.Context, qanda.Principal, qanda.Status, qanda.ThreadCursor) ([]qanda.Thread, qanda.ThreadCursor, error) {
	s.lists++
	return []qanda.Thread{}, qanda.ThreadCursor{}, nil
}
func (*serverStudentQuestions) GetStudentThread(context.Context, qanda.Principal, uuid.UUID) (qanda.ThreadDetail, error) {
	return qanda.ThreadDetail{}, nil
}
func (*serverStudentQuestions) ListStudentMessages(context.Context, qanda.Principal, uuid.UUID, qanda.MessageCursor) ([]qanda.Message, qanda.MessageCursor, error) {
	return nil, qanda.MessageCursor{}, nil
}
func (*serverStudentQuestions) AddStudentMessage(context.Context, qanda.Principal, qanda.AddMessageInput) (qanda.Thread, qanda.Message, error) {
	return qanda.Thread{}, qanda.Message{}, nil
}

func TestBuildApplicationClosesPoolAndHidesMigrationFailure(t *testing.T) {
	closed := false
	secret := "postgres://app:very-secret@db.example/happylearn"
	_, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return errors.New(secret) },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { t.Fatal("newAuth should not run"); return nil, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return nil },
		close:   func(*pgxpool.Pool) { closed = true },
	})
	if closeResources != nil || err == nil || strings.Contains(err.Error(), secret) || !closed {
		t.Fatalf("closeResourcesNil=%t error=%v", closeResources == nil, err)
	}
}

type serverCapturingAuth struct {
	serverFakeAuth
	loginInput auth.LoginInput
}

func (s *serverCapturingAuth) Login(_ context.Context, input auth.LoginInput) (auth.Authentication, string, error) {
	s.loginInput = input
	return serverFakeAuth{}.Login(context.Background(), input)
}

type serverFakeAuth struct{}

func (serverFakeAuth) Login(context.Context, auth.LoginInput) (auth.Authentication, string, error) {
	return auth.Authentication{User: auth.User{ID: uuid.MustParse("84c0f591-e99a-4a91-8250-25c159e1823a"), Username: "student01", Role: auth.RoleStudent, Status: auth.StatusActive}}, "opaque-token", nil
}
func (serverFakeAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{}, auth.ErrUnauthenticated
}
func (serverFakeAuth) ChangePassword(context.Context, auth.ChangePasswordInput) (auth.Authentication, string, error) {
	return auth.Authentication{}, "", auth.ErrUnauthenticated
}
func (serverFakeAuth) Logout(context.Context, string) error       { return nil }
func (serverFakeAuth) LogoutOthers(context.Context, string) error { return nil }

func hasSecureSessionCookie(cookies []*http.Cookie) bool {
	for _, cookie := range cookies {
		if cookie.Name == "hl_session" && cookie.Secure && cookie.HttpOnly {
			return true
		}
	}
	return false
}

func TestBuildApplicationReadyRequiresObjectStoreAndHidesFailure(t *testing.T) {
	called := false
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		objectReady: func(context.Context, config.Config) (func(context.Context) error, error) {
			called = true
			return func(context.Context) error { return errors.New("private object endpoint secret") }, nil
		},
		close: func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeResources()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if !called || w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("called=%t status=%d body=%s", called, w.Code, w.Body.String())
	}
}

func TestReadyHandlerReturnsWhenSharedDependencyBudgetExpires(t *testing.T) {
	cancelled := make(chan struct{})
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		objectReady: func(context.Context, config.Config) (func(context.Context) error, error) {
			return func(ctx context.Context) error { <-ctx.Done(); close(cancelled); return ctx.Err() }, nil
		},
		readinessTimeout: 20 * time.Millisecond,
		close:            func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeResources()
	started := time.Now()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable || time.Since(started) > 500*time.Millisecond || strings.Contains(w.Body.String(), "deadline") {
		t.Fatalf("status=%d elapsed=%s body=%s", w.Code, time.Since(started), w.Body.String())
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("blocking object readiness context not cancelled")
	}
}

func TestBuildApplicationStartsOutboxAfterServicesAndStopsBeforeDatabase(t *testing.T) {
	var order []string
	_, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { order = append(order, "open"); return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { order = append(order, "migrate"); return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
			order = append(order, "services")
			return serverFakeAuth{}, nil
		},
		ready: func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		startOutbox: func(*pgxpool.Pool, operations.ClaimGate) func() {
			order = append(order, "outbox-start")
			return func() { order = append(order, "outbox-stop") }
		},
		close: func(*pgxpool.Pool) { order = append(order, "database-close") },
	})
	if err != nil {
		t.Fatal(err)
	}
	closeResources()
	want := []string{"open", "migrate", "services", "outbox-start", "outbox-stop", "database-close"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v want=%v", order, want)
	}
}

func TestBuildApplicationDoesNotStartOutboxWhenServiceConstructionFails(t *testing.T) {
	started := false
	_, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:        func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate:     func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth:     func(*pgxpool.Pool) (auth.HTTPService, error) { return nil, errors.New("private") },
		startOutbox: func(*pgxpool.Pool, operations.ClaimGate) func() { started = true; return func() {} },
		close:       func(*pgxpool.Pool) {},
	})
	if err == nil || closeResources != nil || started {
		t.Fatalf("err=%v closeNil=%t started=%t", err, closeResources == nil, started)
	}
}
