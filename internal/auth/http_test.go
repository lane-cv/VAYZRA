package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"happylearn.local/app/internal/platform/httpx"
	"happylearn.local/app/internal/platform/redisx"
)

func TestLoginSetsOpaqueCookieAndReturnsSafeUser(t *testing.T) {
	svc := &fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)}
	h := newHTTPTestHandler(svc)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !hasSecureHTTPOnlyCookie(w.Result().Cookies(), "hl_session") {
		t.Fatal("missing secure session cookie")
	}
	if !hasReadableCookie(w.Result().Cookies(), httpx.CSRFCookieName) {
		t.Fatal("missing readable csrf cookie")
	}
	if strings.Contains(w.Body.String(), "opaque-token") || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "hash") {
		t.Fatalf("secret leaked: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"data":{"id":`) || strings.Contains(w.Body.String(), "passwordHash") {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func TestLoginRejectsInvalidJSONContentTypeAndOversizedBodies(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		want                    int
	}{
		{"malformed", "application/json", `{`, http.StatusBadRequest},
		{"unknown field", "application/json", `{"username":"student01","password":"Long Temporary Password 42!","extra":true}`, http.StatusBadRequest},
		{"trailing JSON", "application/json", `{"username":"student01","password":"Long Temporary Password 42!"} {}`, http.StatusBadRequest},
		{"unsupported content type", "text/plain", `{"username":"student01","password":"Long Temporary Password 42!"}`, http.StatusUnsupportedMediaType},
		{"too large", "application/json", `{"username":"student01","password":"` + strings.Repeat("a", 33*1024) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHTTPTestHandler(&fakeHTTPService{loginAuth: activeAuthentication(false)})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "Long Temporary Password") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestLoginRejectsMultipleContentTypeValues(t *testing.T) {
	h := newHTTPTestHandler(&fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.Header.Add("Content-Type", "application/json")
	r.Header.Add("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestLoginCredentialFailuresUseSameGenericResponse(t *testing.T) {
	var bodies []string
	for _, err := range []error{ErrInvalidCredentials, ErrInvalidCredentials, errors.New("disabled")} {
		svc := &fakeHTTPService{loginErr: err}
		h := newHTTPTestHandler(svc)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"wrong"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		bodies = append(bodies, w.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("credential responses differ: %#v", bodies)
	}
}

func TestLoginRateLimitUsesRemoteAddressAndSkipsPasswordVerification(t *testing.T) {
	svc := &fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)}
	limiter := &fakeLoginLimiter{decision: redisx.Decision{Allowed: false, RetryAfter: 2100 * time.Millisecond}}
	h := newHTTPTestHandlerWithThrottle(svc, limiter, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":" Student01 ","password":"Long Temporary Password 42!"}`))
	r.RemoteAddr = "192.0.2.4:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.77")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3" || svc.loginCalls != 0 {
		t.Fatalf("status=%d retry=%q calls=%d body=%s", w.Code, w.Header().Get("Retry-After"), svc.loginCalls, w.Body.String())
	}
	if limiter.username != " Student01 " || limiter.ip != "192.0.2.4" || strings.Contains(w.Body.String(), "Student01") {
		t.Fatalf("limiter=%#v body=%s", limiter, w.Body.String())
	}
}

func TestLoginChallengeIsVerifiedBeforePasswordAndChallengeImageIsNoStore(t *testing.T) {
	svc := &fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)}
	limiter := &fakeLoginLimiter{decision: redisx.Decision{Allowed: true, ChallengeRequired: true}}
	captchas := &fakeCaptchaStore{challenge: redisx.Challenge{ID: "challenge-id", PNG: []byte("png")}}
	h := newHTTPTestHandlerWithThrottle(svc, limiter, captchas)

	missing := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	missing.Header.Set("Content-Type", "application/json")
	missingResult := httptest.NewRecorder()
	h.ServeHTTP(missingResult, missing)
	if missingResult.Code != http.StatusUnauthorized || !strings.Contains(missingResult.Body.String(), `"login_challenge_required"`) || !strings.Contains(missingResult.Body.String(), `"challengeUrl":"/api/v1/auth/challenge"`) || svc.loginCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", missingResult.Code, svc.loginCalls, missingResult.Body.String())
	}

	captchas.valid = true
	valid := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!","challengeId":"challenge-id","challengeAnswer":"ABCDE"}`))
	valid.Header.Set("Content-Type", "application/json")
	validResult := httptest.NewRecorder()
	h.ServeHTTP(validResult, valid)
	if validResult.Code != http.StatusOK || svc.loginCalls != 1 || captchas.id != "challenge-id" || captchas.answer != "ABCDE" {
		t.Fatalf("status=%d calls=%d captcha=%#v body=%s", validResult.Code, svc.loginCalls, captchas, validResult.Body.String())
	}

	challenge := httptest.NewRequest(http.MethodGet, "/api/v1/auth/challenge", nil)
	challenge.RemoteAddr = "192.0.2.4:1234"
	challengeResult := httptest.NewRecorder()
	h.ServeHTTP(challengeResult, challenge)
	if challengeResult.Code != http.StatusOK || challengeResult.Header().Get("Cache-Control") != "no-store, private" || challengeResult.Header().Get("Content-Type") != "image/png" || challengeResult.Header().Get("X-Challenge-ID") != "challenge-id" || challengeResult.Body.Len() != len(captchas.challenge.PNG) {
		t.Fatalf("status=%d headers=%v size=%d", challengeResult.Code, challengeResult.Header(), challengeResult.Body.Len())
	}
}

func TestChallengeUsesTrustedClientIPAndReturnsRateLimitWithoutIssuingImage(t *testing.T) {
	svc := &fakeHTTPService{}
	captchas := &fakeCaptchaStore{createErr: redisx.ErrCaptchaRateLimited}
	h := NewHTTPHandler(svc, HTTPConfig{Captchas: captchas, TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/challenge", nil)
	r.RemoteAddr = "10.1.2.3:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.4")
	w := httptest.NewRecorder()
	h.Challenge(w, r)
	if w.Code != http.StatusTooManyRequests || captchas.ip != "198.51.100.4" || captchas.createCalls != 1 || strings.Contains(w.Body.String(), "198.51.100.4") {
		t.Fatalf("status=%d captcha=%#v body=%s", w.Code, captchas, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store, private" || !strings.Contains(w.Body.String(), `"requestId":`) {
		t.Fatalf("headers=%v body=%s", w.Header(), w.Body.String())
	}
}

func TestLoginRequiresChallengeAfterRedisOutageWhenFailuresReachedThreshold(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := redisx.NewLoginLimiter(rdb, redisx.Policy{Secret: []byte("test-limiter-secret")})
	for range 3 {
		if err := limiter.RecordFailure(context.Background(), "student01", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	mini.Close()

	svc := &fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)}
	h := newHTTPTestHandlerWithThrottle(svc, limiter, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.RemoteAddr = "192.0.2.4:1234"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"login_challenge_required"`) || svc.loginCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, svc.loginCalls, w.Body.String())
	}
}

func TestLoginChallengeSurvivesFallbackPressureDuringRedisOutage(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := redisx.NewLoginLimiter(rdb, redisx.Policy{Secret: []byte("test-limiter-secret")})
	for range 3 {
		if err := limiter.RecordFailure(context.Background(), "victim", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	mini.Close()

	for i := 0; i < 4097; i++ {
		ip := fmt.Sprintf("198.51.%d.%d", i/256, i%256)
		if i%2 == 1 {
			ip = fmt.Sprintf("2001:db8::%x", i)
		}
		if _, err := limiter.Allow(context.Background(), fmt.Sprintf("pressure-%d", i), ip); err != nil {
			t.Fatal(err)
		}
	}

	svc := &fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)}
	h := newHTTPTestHandlerWithThrottle(svc, limiter, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"victim","password":"Long Temporary Password 42!"}`))
	r.RemoteAddr = "192.0.2.4:1234"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"login_challenge_required"`) || svc.loginCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, svc.loginCalls, w.Body.String())
	}
}

func TestLoginUsesClientIPFromTrustedProxy(t *testing.T) {
	svc := &fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)}
	h := NewHTTPHandler(svc, HTTPConfig{TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.RemoteAddr = "10.1.2.3:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.4")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Login(w, r)

	if w.Code != http.StatusOK || svc.loginInput.IP == nil || svc.loginInput.IP.String() != "198.51.100.4" {
		t.Fatalf("status=%d input=%#v body=%s", w.Code, svc.loginInput, w.Body.String())
	}
}

func TestLoginRejectsMalformedTrustedProxyForwardingBeforeLimiterAndService(t *testing.T) {
	svc := &fakeHTTPService{loginRawToken: "opaque-token", loginAuth: activeAuthentication(false)}
	limiter := &fakeLoginLimiter{decision: redisx.Decision{Allowed: false}}
	h := NewHTTPHandler(svc, HTTPConfig{Limiter: limiter, TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.RemoteAddr = "10.1.2.3:443"
	r.Header.Set("X-Forwarded-For", "not-an-ip")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Login(w, r)

	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"invalid_request"`) || limiter.allowCalls != 0 || svc.loginCalls != 0 {
		t.Fatalf("status=%d limiter=%d calls=%d body=%s", w.Code, limiter.allowCalls, svc.loginCalls, w.Body.String())
	}
}

func TestChangePasswordRejectsMalformedTrustedProxyForwardingBeforeService(t *testing.T) {
	svc := &fakeHTTPService{changeRawToken: "replacement-token", changeAuth: activeAuthentication(false)}
	h := NewHTTPHandler(svc, HTTPConfig{TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(`{"currentPassword":"Long Temporary Password 42!","newPassword":"Changed Temporary Password 42!"}`))
	r.RemoteAddr = "10.1.2.3:443"
	r.Header.Set("X-Forwarded-For", "not-an-ip")
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(context.WithValue(r.Context(), sessionTokenContextKey{}, "opaque-token"))
	w := httptest.NewRecorder()
	h.ChangePassword(w, r)

	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"invalid_request"`) || svc.changeInput.SessionToken != "" {
		t.Fatalf("status=%d input=%#v body=%s", w.Code, svc.changeInput, w.Body.String())
	}
}

func TestMeResponseIsNeverStored(t *testing.T) {
	h := NewHTTPHandler(&fakeHTTPService{}, HTTPConfig{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil).WithContext(ContextWithUser(context.Background(), activeAuthentication(false).User))
	w := httptest.NewRecorder()
	h.Me(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d cache=%q", w.Code, w.Header().Get("Cache-Control"))
	}
}
func TestAuthenticateRejectsUnauthenticatedMe(t *testing.T) {
	h := newHTTPTestHandler(&fakeHTTPService{authenticateErr: ErrUnauthenticated})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAuthenticateStoresVerifiedSessionIDOnly(t *testing.T) {
	sessionID := uuid.New()
	authentication := activeAuthentication(false)
	authentication.Session.ID = sessionID
	h := NewHTTPHandler(&fakeHTTPService{authenticateAuth: authentication}, HTTPConfig{})
	next := h.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := SessionIDFromContext(r.Context())
		if !ok || got != sessionID {
			t.Fatalf("session id=%s ok=%t", got, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	next.ServeHTTP(w, authenticatedRequest(http.MethodGet, "/private", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
	if _, ok := SessionIDFromContext(ContextWithUser(context.Background(), authentication.User)); ok {
		t.Fatal("trusted user-only test context fabricated a session id")
	}
}
func TestForcedPasswordChangeRestrictsOtherMutations(t *testing.T) {
	svc := &fakeHTTPService{authenticateAuth: activeAuthentication(true)}
	h := newHTTPTestHandler(svc)
	r := authenticatedRequest(http.MethodPost, "/api/v1/auth/logout-others", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || svc.logoutOthersToken != "" {
		t.Fatalf("status=%d calls=%q body=%s", w.Code, svc.logoutOthersToken, w.Body.String())
	}
}

func TestLogoutClearsCookiesAndLogoutOthersRetainsCurrentSession(t *testing.T) {
	t.Run("logout", func(t *testing.T) {
		svc := &fakeHTTPService{authenticateAuth: activeAuthentication(false)}
		h := newHTTPTestHandler(svc)
		r := authenticatedRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent || svc.logoutToken != "opaque-token" {
			t.Fatalf("status=%d token=%q body=%s", w.Code, svc.logoutToken, w.Body.String())
		}
		if !hasDeletedCookie(w.Result().Cookies(), "hl_session") || !hasDeletedCookie(w.Result().Cookies(), httpx.CSRFCookieName) {
			t.Fatal("logout did not clear auth cookies")
		}
	})
	t.Run("logout others", func(t *testing.T) {
		svc := &fakeHTTPService{authenticateAuth: activeAuthentication(false)}
		h := newHTTPTestHandler(svc)
		r := authenticatedRequest(http.MethodPost, "/api/v1/auth/logout-others", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent || svc.logoutOthersToken != "opaque-token" {
			t.Fatalf("status=%d token=%q body=%s", w.Code, svc.logoutOthersToken, w.Body.String())
		}
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == "hl_session" && cookie.MaxAge < 0 {
				t.Fatal("logout others cleared current session")
			}
		}
	})
}

func TestChangePasswordRotatesSessionAndCSRFToken(t *testing.T) {
	svc := &fakeHTTPService{authenticateAuth: activeAuthentication(true), changeRawToken: "replacement-token", changeAuth: activeAuthentication(false)}
	h := newHTTPTestHandler(svc)
	r := authenticatedRequest(http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(`{"currentPassword":"Long Temporary Password 42!","newPassword":"Changed Temporary Password 42!"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || svc.changeInput.SessionToken != "opaque-token" {
		t.Fatalf("status=%d input=%#v body=%s", w.Code, svc.changeInput, w.Body.String())
	}
	if !hasSecureHTTPOnlyCookie(w.Result().Cookies(), "hl_session") || !hasReadableCookie(w.Result().Cookies(), httpx.CSRFCookieName) {
		t.Fatal("password change did not rotate cookies")
	}
}

type fakeHTTPService struct {
	loginAuth, authenticateAuth, changeAuth Authentication
	loginRawToken, changeRawToken           string
	loginErr, authenticateErr, changeErr    error
	loginInput                              LoginInput
	changeInput                             ChangePasswordInput
	logoutToken, logoutOthersToken          string
	loginCalls                              int
}

func (s *fakeHTTPService) Login(_ context.Context, input LoginInput) (Authentication, string, error) {
	s.loginCalls++
	s.loginInput = input
	return s.loginAuth, s.loginRawToken, s.loginErr
}
func (s *fakeHTTPService) Authenticate(_ context.Context, _ string) (Authentication, error) {
	return s.authenticateAuth, s.authenticateErr
}
func (s *fakeHTTPService) ChangePassword(_ context.Context, input ChangePasswordInput) (Authentication, string, error) {
	s.changeInput = input
	return s.changeAuth, s.changeRawToken, s.changeErr
}
func (s *fakeHTTPService) Logout(_ context.Context, raw string) error {
	s.logoutToken = raw
	return nil
}
func (s *fakeHTTPService) LogoutOthers(_ context.Context, raw string) error {
	s.logoutOthersToken = raw
	return nil
}

func newHTTPTestHandler(svc *fakeHTTPService) http.Handler {
	return newHTTPTestHandlerWithThrottle(svc, nil, nil)
}

func newHTTPTestHandlerWithThrottle(svc *fakeHTTPService, limiter redisx.Limiter, captchas redisx.CaptchaService) http.Handler {
	h := NewHTTPHandler(svc, HTTPConfig{CookieSecure: true, Limiter: limiter, Captchas: captchas})
	r := chi.NewRouter()
	r.Get("/api/v1/auth/challenge", h.Challenge)
	r.Post("/api/v1/auth/login", h.Login)
	r.Group(func(private chi.Router) {
		private.Use(h.Authenticate)
		private.Get("/api/v1/auth/me", h.Me)
		private.Post("/api/v1/auth/change-password", h.ChangePassword)
		private.Post("/api/v1/auth/logout", h.Logout)
		private.Post("/api/v1/auth/logout-others", h.LogoutOthers)
	})
	return r
}

type fakeLoginLimiter struct {
	decision     redisx.Decision
	username, ip string
	allowCalls   int
}

func (f *fakeLoginLimiter) Allow(_ context.Context, username, ip string) (redisx.Decision, error) {
	f.username, f.ip = username, ip
	f.allowCalls++
	return f.decision, nil
}
func (f *fakeLoginLimiter) RecordFailure(context.Context, string, string) error { return nil }
func (f *fakeLoginLimiter) RecordSuccess(context.Context, string, string) error { return nil }

type fakeCaptchaStore struct {
	challenge      redisx.Challenge
	valid          bool
	id, answer, ip string
	createErr      error
	createCalls    int
}

func (f *fakeCaptchaStore) Create(_ context.Context, ip string) (redisx.Challenge, error) {
	f.ip = ip
	f.createCalls++
	return f.challenge, f.createErr
}
func (f *fakeCaptchaStore) Verify(_ context.Context, id, answer string) (bool, error) {
	f.id, f.answer = id, answer
	return f.valid, nil
}
func activeAuthentication(mustChange bool) Authentication {
	return Authentication{User: User{ID: uuid.MustParse("84c0f591-e99a-4a91-8250-25c159e1823a"), Username: "student01", DisplayName: "林同学", Role: RoleStudent, Status: StatusActive, MustChangePassword: mustChange}}
}

func authenticatedRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	return r
}

func hasSecureHTTPOnlyCookie(cookies []*http.Cookie, name string) bool {
	for _, cookie := range cookies {
		if cookie.Name == name && cookie.Value != "" && cookie.Secure && cookie.HttpOnly && cookie.Path == "/" && cookie.SameSite == http.SameSiteLaxMode && cookie.MaxAge > 0 && cookie.MaxAge <= 30*24*60*60 {
			return true
		}
	}
	return false
}

func hasReadableCookie(cookies []*http.Cookie, name string) bool {
	for _, cookie := range cookies {
		if cookie.Name == name && cookie.Value != "" && cookie.Secure && !cookie.HttpOnly && cookie.Path == "/" && cookie.SameSite == http.SameSiteLaxMode {
			return true
		}
	}
	return false
}

func hasDeletedCookie(cookies []*http.Cookie, name string) bool {
	for _, cookie := range cookies {
		if cookie.Name == name && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestRoleContextAndRequireRole(t *testing.T) {
	user := activeAuthentication(false).User
	ctx := context.WithValue(context.Background(), userContextKey{}, user)
	got, ok := UserFromContext(ctx)
	if !ok || got.ID != user.ID {
		t.Fatalf("context user = %#v, %t", got, ok)
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"missing principal", context.Background(), http.StatusUnauthorized},
		{"wrong role", context.WithValue(context.Background(), userContextKey{}, user), http.StatusForbidden},
		{"matching role", context.WithValue(context.Background(), userContextKey{}, User{Role: RoleAdmin}), http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := RequireRole(RoleAdmin)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
