package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"

	"happylearn.local/app/internal/platform/httpx"
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
	CookieSecure bool
}

type Handler struct {
	service      HTTPService
	cookieSecure bool
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

func NewHTTPHandler(service HTTPService, cfg HTTPConfig) *Handler {
	return &Handler{service: service, cookieSecure: cfg.CookieSecure}
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Username) == "" || input.Password == "" {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
		return
	}
	authentication, rawToken, err := h.service.Login(r.Context(), LoginInput{
		Username: input.Username, Password: input.Password, IP: requestIP(r), UserAgent: r.UserAgent(),
	})
	if err != nil {
		// Login failure must not disclose whether the username, password, or
		// account status caused the rejection.
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
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
	authentication, replacement, err := h.service.ChangePassword(r.Context(), ChangePasswordInput{
		SessionToken: rawToken, CurrentPassword: input.CurrentPassword, NewPassword: input.NewPassword, IP: requestIP(r), UserAgent: r.UserAgent(),
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

func requestIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
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
