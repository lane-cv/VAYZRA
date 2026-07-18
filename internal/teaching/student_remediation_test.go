package teaching

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/redisx"
)

func TestStudentServiceNormalizesBrowseAndSearchBounds(t *testing.T) {
	store := &remediationStudentStore{}
	svc := NewStudentService(store, fixedTeachingClock)
	actor := studentTeachingPrincipal(uuid.New())
	if _, _, err := svc.Browse(context.Background(), actor, BrowseInput{}); err != nil {
		t.Fatal(err)
	}
	if store.browse.Limit != 20 {
		t.Fatalf("browse default limit=%d, want 20", store.browse.Limit)
	}
	if _, _, err := svc.Browse(context.Background(), actor, BrowseInput{Limit: 50, Kind: CatalogLesson}); err != nil {
		t.Fatal(err)
	}
	if store.browse.Limit != 50 || store.browse.Kind != CatalogLesson {
		t.Fatalf("browse input=%#v", store.browse)
	}
	for _, in := range []BrowseInput{{Limit: 51}, {Kind: CatalogKind("file")}} {
		if _, _, err := svc.Browse(context.Background(), actor, in); err != ErrInvalid {
			t.Fatalf("browse %#v error=%v, want invalid", in, err)
		}
	}
	if _, _, err := svc.Search(context.Background(), actor, SearchInput{Query: "数 学"}); err != nil {
		t.Fatal(err)
	}
	if store.search.Limit != 20 || !store.search.IncludeBody {
		t.Fatalf("long search=%#v", store.search)
	}
	if _, _, err := svc.Search(context.Background(), actor, SearchInput{Query: "数学", Limit: 11}); err != ErrInvalid {
		t.Fatalf("short error=%v", err)
	}
	if _, _, err := svc.Search(context.Background(), actor, SearchInput{Query: "数学"}); err != nil {
		t.Fatal(err)
	}
	if store.search.Limit != 10 || store.search.IncludeBody {
		t.Fatalf("short search=%#v", store.search)
	}
	if _, _, err := svc.Search(context.Background(), actor, SearchInput{Query: "学"}); err != ErrInvalid {
		t.Fatalf("one-rune error=%v", err)
	}
	if _, _, err := svc.Search(context.Background(), actor, SearchInput{Query: strings.Repeat("界", 65)}); err != ErrInvalid {
		t.Fatalf("65-rune error=%v", err)
	}
}

func TestStudentHTTPUsesCompactLowerCamelDTOs(t *testing.T) {
	service := &remediationStudentHTTPService{search: []SearchResult{{
		LessonID: uuid.New(), RevisionID: uuid.New(), Title: "Vectors", Summary: "short", Snippet: strings.Repeat("片", 240),
		GradeID: uuid.New(), GradeName: "G1", TermID: uuid.New(), TermName: "T1", SubjectID: uuid.New(), SubjectName: "Math",
		ChapterID: uuid.New(), ChapterName: "C1", RevisionStatus: "published",
	}}}
	h := NewStudentHandlerWithConfig(service, StudentHTTPConfig{SearchLimiter: allowSearchLimiter{}}).Routes()
	r := httptest.NewRequest(http.MethodGet, "/search?q=vectors", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	for _, required := range []string{"\"lessonId\"", "\"revisionId\"", "\"title\"", "\"snippet\"", "\"revisionStatus\"", "\"gradeId\""} {
		if !strings.Contains(body, required) {
			t.Fatalf("missing %s in %s", required, body)
		}
	}
	for _, forbidden := range []string{"\"LessonID\"", "\"BodyMarkdown\"", "\"bodyMarkdown\"", "\"Audience\"", "\"publishedBy\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("leaked %s in %s", forbidden, body)
		}
	}
	if utf8.RuneCountInString(service.search[0].Snippet) > 240 || w.Body.Len() > 4096 {
		t.Fatalf("unbounded response bytes=%d", w.Body.Len())
	}
}

func TestStudentSearchRateLimitReturnsStable429BeforeDispatch(t *testing.T) {
	service := &remediationStudentHTTPService{}
	limiter := &denySearchLimiter{decision: redisx.ResourceDecision{RetryAfter: 3 * time.Second}}
	h := NewStudentHandlerWithConfig(service, StudentHTTPConfig{SearchLimiter: limiter}).Routes()
	r := httptest.NewRequest(http.MethodGet, "/search?q=lesson", nil)
	studentID := uuid.New()
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3" || service.searchCalls != 0 || limiter.account != studentID {
		t.Fatalf("status=%d retry=%q calls=%d account=%s body=%s", w.Code, w.Header().Get("Retry-After"), service.searchCalls, limiter.account, w.Body.String())
	}
}

func TestStudentProgressLimiterReceivesSessionAndAccountBeforeDispatch(t *testing.T) {
	service := &remediationStudentHTTPService{}
	limiter := &remediationProgressLimiter{decision: redisx.ProgressDecision{RetryAfter: time.Second}}
	studentID, sessionID := uuid.New(), uuid.New()
	studentHandler := NewStudentHandlerWithConfig(service, StudentHTTPConfig{ProgressLimiter: limiter})
	authHandler := auth.NewHTTPHandler(&studentSessionAuth{sessionID: sessionID, userID: studentID}, auth.HTTPConfig{})
	router := chi.NewRouter()
	router.Use(authHandler.Authenticate)
	router.Mount("/", studentHandler.Routes())
	r := httptest.NewRequest(http.MethodPost, "/progress", strings.NewReader(`{"revisionId":"`+uuid.NewString()+`","observedAt":"2026-07-18T02:00:00Z"}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || service.progressCalls != 0 || limiter.session != sessionID || limiter.account != studentID {
		t.Fatalf("status=%d progress=%d session=%s account=%s", w.Code, service.progressCalls, limiter.session, limiter.account)
	}
}

func TestStudentSearchTransactionWiringIsReadOnlyAndBounded(t *testing.T) {
	if studentSearchTxOptions.AccessMode != pgx.ReadOnly {
		t.Fatalf("access mode=%s", studentSearchTxOptions.AccessMode)
	}
	if studentSearchStatementTimeout != 750*time.Millisecond {
		t.Fatalf("timeout=%s", studentSearchStatementTimeout)
	}
}

type remediationStudentStore struct {
	browse BrowseInput
	search SearchInput
}

func (s *remediationStudentStore) BrowseStudent(_ context.Context, in BrowseInput) ([]StudentCatalogNode, CatalogCursor, error) {
	s.browse = in
	return nil, CatalogCursor{}, nil
}
func (*remediationStudentStore) Recent(context.Context, uuid.UUID, int) ([]RecentLesson, error) {
	return nil, nil
}
func (s *remediationStudentStore) Search(_ context.Context, in SearchInput) ([]SearchResult, SearchCursor, error) {
	s.search = in
	return nil, SearchCursor{}, nil
}
func (*remediationStudentStore) GetLesson(context.Context, uuid.UUID, uuid.UUID) (StudentLesson, error) {
	return StudentLesson{}, nil
}
func (*remediationStudentStore) GetPosition(context.Context, uuid.UUID, uuid.UUID) (LessonProgress, error) {
	return LessonProgress{}, nil
}
func (*remediationStudentStore) UpdateProgress(context.Context, uuid.UUID, ProgressInput) error {
	return nil
}

type remediationStudentHTTPService struct {
	search                     []SearchResult
	searchCalls, progressCalls int
}

func (*remediationStudentHTTPService) Browse(context.Context, Principal, BrowseInput) ([]StudentCatalogNode, CatalogCursor, error) {
	return nil, CatalogCursor{}, nil
}
func (*remediationStudentHTTPService) Recent(context.Context, Principal, int) ([]RecentLesson, error) {
	return nil, nil
}
func (*remediationStudentHTTPService) GetLesson(context.Context, Principal, uuid.UUID) (StudentLesson, error) {
	return StudentLesson{}, nil
}
func (*remediationStudentHTTPService) GetPosition(context.Context, Principal, uuid.UUID) (LessonProgress, error) {
	return LessonProgress{}, nil
}
func (s *remediationStudentHTTPService) Search(context.Context, Principal, SearchInput) ([]SearchResult, SearchCursor, error) {
	s.searchCalls++
	return s.search, SearchCursor{}, nil
}
func (s *remediationStudentHTTPService) UpdateProgress(context.Context, Principal, ProgressInput) error {
	s.progressCalls++
	return nil
}

type allowSearchLimiter struct{}

func (allowSearchLimiter) AllowSearch(context.Context, uuid.UUID) (redisx.ResourceDecision, error) {
	return redisx.ResourceDecision{Allowed: true}, nil
}

type denySearchLimiter struct {
	decision redisx.ResourceDecision
	account  uuid.UUID
}

func (l *denySearchLimiter) AllowSearch(_ context.Context, account uuid.UUID) (redisx.ResourceDecision, error) {
	l.account = account
	return l.decision, nil
}

type remediationProgressLimiter struct {
	decision         redisx.ProgressDecision
	session, account uuid.UUID
}

func (l *remediationProgressLimiter) AllowProgressWrite(_ context.Context, session, account uuid.UUID) (redisx.ProgressDecision, error) {
	l.session, l.account = session, account
	return l.decision, nil
}
