package app

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"happylearn.local/app/internal/platform/httpx"
)

type Dependencies struct {
	Ready func(context.Context) error
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
	return r
}
