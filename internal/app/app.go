package app

import (
	"context"
	"io/fs"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/qanda"
	"happylearn.local/app/internal/students"
	"happylearn.local/app/internal/teaching"

	"happylearn.local/app/internal/platform/httpx"
	"happylearn.local/app/internal/platform/redisx"
	"happylearn.local/app/internal/platform/staticweb"
)

type Dependencies struct {
	Ready             func(context.Context) error
	Auth              auth.HTTPService
	Students          students.HTTPService
	Teaching          teaching.AdminHTTPService
	Uploads           files.UploadHTTPService
	FileAccess        files.AccessHTTPService
	FileBindings      files.BindingHTTPService
	FileCenter        files.FileCenterHTTPService
	StudentTeaching   teaching.StudentHTTPService
	StudentQuestions  qanda.StudentHTTPService
	PublicOrigin      string
	CookieSecure      bool
	TrustedProxyCIDRs []netip.Prefix
	Limiter           redisx.Limiter
	ProgressLimiter   redisx.ProgressWriteLimiter
	SearchLimiter     redisx.SearchRateLimiter
	Captchas          redisx.CaptchaService
	StaticFiles       fs.FS
}

func New(d Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.RequestID, middleware.Recoverer)
	r.Get("/api/v1/health/live", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/v1/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if d.Ready == nil {
			httpx.Error(w, r, http.StatusServiceUnavailable, "not_ready", "服务暂不可用")
			return
		}
		if err := d.Ready(r.Context()); err != nil {
			httpx.Error(w, r, http.StatusServiceUnavailable, "not_ready", "服务暂不可用")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if d.Auth != nil {
		authHTTP := auth.NewHTTPHandler(d.Auth, auth.HTTPConfig{
			CookieSecure: d.CookieSecure, Limiter: d.Limiter, Captchas: d.Captchas, TrustedProxyCIDRs: d.TrustedProxyCIDRs,
		})
		r.Route("/api/v1", func(api chi.Router) {
			api.Use(httpx.NoStore)
			api.Use(httpx.OriginGuard(d.PublicOrigin))
			api.Use(httpx.CSRF)
			api.Get("/auth/challenge", authHTTP.Challenge)
			api.Post("/auth/login", authHTTP.Login)
			api.Group(func(private chi.Router) {
				private.Use(authHTTP.Authenticate)
				private.Get("/auth/me", authHTTP.Me)
				private.Post("/auth/change-password", authHTTP.ChangePassword)
				private.Post("/auth/logout", authHTTP.Logout)
				private.Post("/auth/logout-others", authHTTP.LogoutOthers)
				if d.Students != nil {
					private.Mount("/admin/students", students.NewHandlerWithConfig(d.Students, students.HTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs}).Routes())
				}
				if d.FileBindings != nil {
					bindingHTTP := files.NewBindingHandler(d.FileBindings, d.TrustedProxyCIDRs)
					private.With(auth.RequireRole(auth.RoleAdmin)).Get("/admin/lessons/{id}/files", bindingHTTP.List)
					private.With(auth.RequireRole(auth.RoleAdmin)).Put("/admin/lessons/{id}/files", bindingHTTP.Replace)
				}
				if d.Teaching != nil {
					private.Mount("/admin", teaching.NewAdminHandlerWithConfig(d.Teaching, teaching.AdminHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs}).Routes())
				}
				if d.Uploads != nil {
					private.Mount("/admin/uploads", files.NewUploadHandlerWithConfig(d.Uploads, files.UploadHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs}).Routes())
				}
				if d.FileCenter != nil {
					private.Mount("/admin/files", files.NewFileCenterHandler(d.FileCenter, d.TrustedProxyCIDRs).Routes())
				}
				if d.StudentTeaching != nil {
					private.Mount("/student", teaching.NewStudentHandlerWithConfig(d.StudentTeaching, teaching.StudentHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs, ProgressLimiter: d.ProgressLimiter, SearchLimiter: d.SearchLimiter}).Routes())
				}
				if d.StudentQuestions != nil {
					private.Mount("/student/questions", qanda.NewStudentHandlerWithConfig(d.StudentQuestions, qanda.StudentHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs}).Routes())
				}
				if d.FileAccess != nil {
					private.Mount("/files", files.NewAccessHandler(d.FileAccess, d.TrustedProxyCIDRs).Routes())
				}
			})
		})
	}
	if d.StaticFiles != nil {
		r.NotFound(staticweb.New(d.StaticFiles).ServeHTTP)
	}
	return r
}
