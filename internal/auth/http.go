package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/httpx"
	"happylearn.local/app/internal/platform/redisx"
)

const (
	sessionCookieName = "hl_session"
	maxRequestBody    = 32 * 1024
	sessionMaxAge     = 30 * 24 * 60 * 60
)

type HTTPService interface {
	Login(context.Context, LoginInput) (Authentication, string, error)
	Authenticate(context.Context, string) (Authentication, error)
	ChangePassword(context.Context, ChangePasswordInput) (Authentication, string, error)
	Logout(context.Context, string) error
	LogoutOthers(context.Context, string) error
}

type HTTPConfig struct {
	CookieSecure      bool
	Limiter           redisx.Limiter
	Captchas          redisx.CaptchaService
	TrustedProxyCIDRs []netip.Prefix
}

type Handler struct {
	service           HTTPService
	cookieSecure      bool
	limiter           redisx.Limiter
	captchas          redisx.CaptchaService
	trustedProxyCIDRs []netip.Prefix
}

type UserView struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"displayName"`
	Role               Role   `json:"role"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

type userContextKey struct{}
type sessionTokenContextKey struct{}
type sessionIDContextKey struct{}

func NewHTTPHandler(service HTTPService, cfg HTTPConfig) *Handler {
	return &Handler{
		service: service, cookieSecure: cfg.CookieSecure, limiter: cfg.Limiter, captchas: cfg.Captchas,
		trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...),
	}
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		ChallengeID     string `json:"challengeId"`
		ChallengeAnswer string `json:"challengeAnswer"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Username) == "" || input.Password == "" {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	ip, err := h.clientIP(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	ipValue := ""
	if ip != nil {
		ipValue = ip.String()
	}
	if h.limiter != nil {
		decision, err := h.limiter.Allow(r.Context(), input.Username, ipValue)
		if err != nil {
			httpx.Error(w, r, http.StatusServiceUnavailable, "internal_error", "服务暂不可用")
			return
		}
		if !decision.Allowed {
			seconds := int(math.Ceil(decision.RetryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			httpx.Error(w, r, http.StatusTooManyRequests, "login_rate_limited", "登录操作过于频繁，请稍后重试")
			return
		}
		if decision.ChallengeRequired {
			if h.captchas == nil {
				h.writeChallengeRequired(w, r)
				return
			}
			verified, err := h.captchas.Verify(r.Context(), input.ChallengeID, input.ChallengeAnswer)
			if err != nil || !verified {
				h.writeChallengeRequired(w, r)
				return
			}
		}
	}
	authentication, rawToken, err := h.service.Login(r.Context(), LoginInput{
		Username: input.Username, Password: input.Password, IP: ip, UserAgent: r.UserAgent(),
	})
	if err != nil {
		if h.limiter != nil {
			_ = h.limiter.RecordFailure(r.Context(), input.Username, ipValue)
		}
		// Login failure must not disclose whether the username, password, or
		// account status caused the rejection.
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	if h.limiter != nil {
		_ = h.limiter.RecordSuccess(r.Context(), input.Username, ipValue)
	}
	if rawToken == "" {
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	if !h.setAuthenticationCookies(w, rawToken) {
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data UserView `json:"data"`
	}{Data: userView(authentication.User)})
}

func (h *Handler) Challenge(w http.ResponseWriter, r *http.Request) {
	if h.captchas == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "internal_error", "服务暂不可用")
		return
	}
	ip, err := h.clientIP(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	challenge, err := h.captchas.Create(r.Context(), ip.String())
	if errors.Is(err, redisx.ErrCaptchaRateLimited) {
		httpx.Error(w, r, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后重试")
		return
	}
	if err != nil || challenge.ID == "" || len(challenge.PNG) == 0 || len(challenge.PNG) > 50*1024 {
		httpx.Error(w, r, http.StatusServiceUnavailable, "internal_error", "服务暂不可用")
		return
	}
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Challenge-ID", challenge.ID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(challenge.PNG)
}

func (h *Handler) writeChallengeRequired(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusUnauthorized, struct {
		Error struct {
			Code         string `json:"code"`
			Message      string `json:"message"`
			RequestID    string `json:"requestId"`
			ChallengeURL string `json:"challengeUrl"`
		} `json:"error"`
	}{
		Error: struct {
			Code         string `json:"code"`
			Message      string `json:"message"`
			RequestID    string `json:"requestId"`
			ChallengeURL string `json:"challengeUrl"`
		}{Code: "login_challenge_required", Message: "请完成验证码后重试", RequestID: httpx.RequestIDFromContext(r.Context()), ChallengeURL: "/api/v1/auth/challenge"},
	})
}
func (h *Handler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.service == nil {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		rawToken, ok := sessionCookie(r)
		if !ok {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		authentication, err := h.service.Authenticate(r.Context(), rawToken)
		if err != nil || authentication.User.Status != StatusActive {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		if authentication.User.MustChangePassword && !passwordChangeAllowed(r.URL.Path) {
			httpx.Error(w, r, http.StatusForbidden, "password_change_required", "请先修改密码")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey{}, authentication.User)
		ctx = context.WithValue(ctx, sessionTokenContextKey{}, rawToken)
		ctx = context.WithValue(ctx, sessionIDContextKey{}, authentication.Session.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data UserView `json:"data"`
	}{Data: userView(user)})
}

func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.CurrentPassword == "" || input.NewPassword == "" {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	rawToken, ok := sessionTokenFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	ip, err := h.clientIP(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	authentication, replacement, err := h.service.ChangePassword(r.Context(), ChangePasswordInput{
		SessionToken: rawToken, CurrentPassword: input.CurrentPassword, NewPassword: input.NewPassword, IP: ip, UserAgent: r.UserAgent(),
	})
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		if errors.Is(err, ErrInvalidCredentials) {
			httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
			return
		}
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	if replacement == "" {
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	if !h.setAuthenticationCookies(w, replacement) {
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data UserView `json:"data"`
	}{Data: userView(authentication.User)})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	rawToken, ok := sessionTokenFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	if err := h.service.Logout(r.Context(), rawToken); err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	h.clearAuthenticationCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) LogoutOthers(w http.ResponseWriter, r *http.Request) {
	rawToken, ok := sessionTokenFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	if err := h.service.LogoutOthers(r.Context(), rawToken); err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey{}).(User)
	return user, ok
}

// ContextWithUser is intended for trusted in-process middleware and tests.
func ContextWithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

// SessionIDFromContext returns an ID only when authentication middleware
// verified the session for this request. ContextWithUser intentionally does
// not populate it so tests and in-process callers cannot fabricate sessions.
func SessionIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(sessionIDContextKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}
func RequireRole(role Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok {
				httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
				return
			}
			if user.Role != role {
				httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (h *Handler) setAuthenticationCookies(w http.ResponseWriter, rawToken string) bool {
	csrfToken, err := httpx.NewCSRFToken()
	if err != nil {
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: rawToken, Path: "/", MaxAge: sessionMaxAge, HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: httpx.CSRFCookieName, Value: csrfToken, Path: "/", MaxAge: sessionMaxAge, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode})
	return true
}

func (h *Handler) clearAuthenticationCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookieName, httpx.CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == sessionCookieName, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode})
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSONDecodeError(w, r, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONDecodeError(w, r, err)
		return false
	}
	return true
}

func writeJSONDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		return
	}
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}

func (h *Handler) clientIP(r *http.Request) (net.IP, error) {
	addr, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		return nil, err
	}
	return append(net.IP(nil), addr.AsSlice()...), nil
}

func sessionCookie(r *http.Request) (string, bool) {
	var value string
	found := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == sessionCookieName {
			found++
			value = cookie.Value
		}
	}
	return value, found == 1 && value != ""
}

func sessionTokenFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(sessionTokenContextKey{}).(string)
	return token, ok && token != ""
}

func passwordChangeAllowed(path string) bool {
	switch path {
	case "/api/v1/auth/me", "/api/v1/auth/change-password", "/api/v1/auth/logout":
		return true
	default:
		return false
	}
}

func userView(user User) UserView {
	return UserView{ID: user.ID.String(), Username: user.Username, DisplayName: user.DisplayName, Role: user.Role, MustChangePassword: user.MustChangePassword}
}
