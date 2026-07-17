package app

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"happylearn.local/app/internal/auth"

	"happylearn.local/app/internal/platform/httpx"
)

type Dependencies struct {
	Ready        func(context.Context) error
	Auth         auth.HTTPService
	PublicOrigin string
	CookieSecure bool
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
		authHTTP := auth.NewHTTPHandler(d.Auth, auth.HTTPConfig{CookieSecure: d.CookieSecure})
		r.Route("/api/v1", func(api chi.Router) {
			api.Use(httpx.OriginGuard(d.PublicOrigin))
			api.Use(httpx.CSRF)
			api.Post("/auth/login", authHTTP.Login)
			api.Group(func(private chi.Router) {
				private.Use(authHTTP.Authenticate)
				private.Get("/auth/me", authHTTP.Me)
				private.Post("/auth/change-password", authHTTP.ChangePassword)
				private.Post("/auth/logout", authHTTP.Logout)
				private.Post("/auth/logout-others", authHTTP.LogoutOthers)
			})
		})
	}
	return r
}
