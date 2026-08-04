package app

import (
	"context"
	"io/fs"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"

	"happylearn.local/app/internal/aiqa"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/notifications"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/qanda"
	"happylearn.local/app/internal/students"
	"happylearn.local/app/internal/teaching"
	"happylearn.local/app/internal/updates"

	"happylearn.local/app/internal/platform/httpx"
	"happylearn.local/app/internal/platform/redisx"
	"happylearn.local/app/internal/platform/safelog"
	"happylearn.local/app/internal/platform/staticweb"
)

type Dependencies struct {
	Logger              safelog.Logger
	Ready               func(context.Context) error
	Auth                auth.HTTPService
	Students            students.HTTPService
	Teaching            teaching.AdminHTTPService
	Uploads             files.UploadHTTPService
	QAUploads           files.UploadHTTPService
	AIUploads           files.UploadHTTPService
	FileAccess          files.AccessHTTPService
	QAFileAccess        files.QAAccessHTTPService
	FileBindings        files.BindingHTTPService
	FileCenter          files.FileCenterHTTPService
	StudentTeaching     teaching.StudentHTTPService
	StudentQuestions    qanda.StudentHTTPService
	AdminQuestions      qanda.AdminHTTPService
	AdminAI             aiqa.AdminConfigHTTPService
	AdminAIUsage        aiqa.AdminUsageService
	StudentAI           aiqa.StudentService
	StudentAIEvents     aiqa.StudentEventStore
	StudentAISummaries  aiqa.SummaryService
	Notifications       notifications.HTTPService
	OperationsWriteGate operations.WriteGate
	AdminOperations     operations.HTTPService
	AdminBackups        backup.HTTPService
	AdminUpdates        updates.HTTPService
	AIFileAccess        files.AIAccessHTTPService
	PublicOrigin        string
	CookieSecure        bool
	TrustedProxyCIDRs   []netip.Prefix
	Limiter             redisx.Limiter
	ProgressLimiter     redisx.ProgressWriteLimiter
	SearchLimiter       redisx.SearchRateLimiter
	ProviderTestLimiter redisx.ProviderTestRateLimiter
	Captchas            redisx.CaptchaService
	StaticFiles         fs.FS
}

func New(d Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Use(
		httpx.RequestID,
		httpx.SafeRequestLog(d.Logger, time.Now),
		httpx.SafeRecoverer(d.Logger),
	)
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
			if d.OperationsWriteGate != nil {
				api.Use(operations.UnsafeWriteGate(d.OperationsWriteGate))
			}
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
					private.Mount("/admin/uploads", files.NewUploadHandlerWithConfig(d.Uploads, files.UploadHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs, AllowedRoles: []auth.Role{auth.RoleAdmin}}).Routes())
				}
				if d.QAUploads != nil {
					private.Mount("/student/question-uploads", files.NewUploadHandlerWithConfig(d.QAUploads, files.UploadHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs, AllowedRoles: []auth.Role{auth.RoleStudent}}).Routes())
					private.Mount("/admin/question-uploads", files.NewUploadHandlerWithConfig(d.QAUploads, files.UploadHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs, AllowedRoles: []auth.Role{auth.RoleAdmin}}).Routes())
				}
				if d.AIUploads != nil {
					private.Mount("/student/ai-uploads", files.NewUploadHandlerWithConfig(d.AIUploads, files.UploadHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs, AllowedRoles: []auth.Role{auth.RoleStudent}}).Routes())
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
				if d.AdminQuestions != nil {
					private.Mount("/admin/questions", qanda.NewAdminHandlerWithConfig(d.AdminQuestions, qanda.AdminHTTPConfig{TrustedProxyCIDRs: d.TrustedProxyCIDRs}).Routes())
				}
				if d.AdminAI != nil {
					private.Mount("/admin/ai", aiqa.NewAdminConfigHandlerWithConfig(d.AdminAI, aiqa.AdminConfigHTTPConfig{
						TrustedProxyCIDRs: d.TrustedProxyCIDRs, ProviderTestLimiter: d.ProviderTestLimiter,
					}).Routes())
				}
				if d.AdminAIUsage != nil {
					private.Mount("/admin/ai/usage", aiqa.NewAdminUsageHandler(d.AdminAIUsage, d.TrustedProxyCIDRs).Routes())
				}
				if d.AdminBackups != nil {
					private.Mount("/admin/operations/backups", backup.NewAdminHandler(d.AdminBackups, d.TrustedProxyCIDRs).Routes())
				}
				if d.AdminOperations != nil {
					private.Mount("/admin/operations", operations.NewAdminHandler(d.AdminOperations, d.TrustedProxyCIDRs).Routes())
				}
				if d.AdminUpdates != nil {
					private.Mount("/admin/updates", updates.NewAdminHandler(d.AdminUpdates, d.TrustedProxyCIDRs).Routes())
				}
				if d.StudentAI != nil {
					private.Mount("/student/ai", aiqa.NewStudentHandlerWithConfig(d.StudentAI, d.StudentAIEvents, aiqa.StudentHTTPConfig{
						TrustedProxyCIDRs: d.TrustedProxyCIDRs,
					}).Routes())
				}
				if d.StudentAISummaries != nil {
					private.Mount("/student/question-summaries", aiqa.NewStudentSummaryHandler(d.StudentAISummaries, d.TrustedProxyCIDRs).Routes())
				}
				if d.Notifications != nil {
					private.Mount("/notifications", notifications.NewHandler(d.Notifications).Routes())
				}
				transferAuditLog := func(category string) {
					d.Logger.Error("file.transfer.audit", safelog.Field{
						Name:  "category",
						Value: category,
					})
				}
				if d.FileAccess != nil {
					private.Mount("/files", files.NewAccessHandlerWithLog(
						d.FileAccess,
						d.TrustedProxyCIDRs,
						transferAuditLog,
					).Routes())
				}
				if d.QAFileAccess != nil {
					private.Mount("/question-files", files.NewQAAccessHandlerWithLog(
						d.QAFileAccess,
						d.TrustedProxyCIDRs,
						transferAuditLog,
					).Routes())
				}
				if d.AIFileAccess != nil {
					private.Mount("/ai-question-files", files.NewAIAccessHandlerWithLog(
						d.AIFileAccess,
						d.TrustedProxyCIDRs,
						transferAuditLog,
					).Routes())
				}
			})
		})
	}
	if d.StaticFiles != nil {
		r.NotFound(staticweb.New(d.StaticFiles).ServeHTTP)
	}
	return r
}
