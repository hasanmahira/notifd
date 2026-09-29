package http

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	ws "github.com/hasanmahira/notifd/internal/delivery/websocket"
	"github.com/hasanmahira/notifd/internal/infrastructure/auth"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hasanmahira/notifd/internal/usecase"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RouterDeps is the dependency bundle a Router needs. Constructed in main.go.
type RouterDeps struct {
	Health      *HealthChecker
	Notif       *NotificationHandler
	Batch       *BatchHandler
	Template    *TemplateHandler
	WS          *ws.Handler
	Verifier    auth.Verifier
	Idempotency usecase.IdempotencyStore
	Metrics     *observ.Metrics
	Log         *slog.Logger
}

// NewRouter wires every public HTTP route.
// The ordering is deliberate:
//  1. public routes (/healthz, /readyz, /metrics) live outside the auth
//     middleware so liveness probes never 401
//  2. /v1/* is wrapped in auth + idempotency + body-limit
func NewRouter(d RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestIDMiddleware())
	r.Use(accessLogMiddleware(d.Log, d.Metrics))
	r.Use(bodyLimitMiddleware(1 << 20)) // 1MB

	// public
	r.Get("/healthz", d.Health.Liveness)
	r.Get("/readyz", d.Health.Readiness)
	if d.Metrics != nil {
		r.Handle("/metrics", promhttp.HandlerFor(d.Metrics.Registry, promhttp.HandlerOpts{}))
	} else {
		r.Handle("/metrics", promhttp.Handler())
	}

	// OpenAPI + Swagger UI (also public; the spec + UI are static assets)
	r.Get("/openapi.yaml", serveStatic("docs/openapi.yaml", "application/yaml"))
	r.Get("/swagger", redirectTo("/swagger/"))
	r.Get("/swagger/", serveStatic("docs/swagger.html", "text/html; charset=utf-8"))
	r.Get("/swagger/index.html", serveStatic("docs/swagger.html", "text/html; charset=utf-8"))

	// authenticated
	r.Route("/v1", func(r chi.Router) {
		r.Use(authMiddleware(d.Verifier))

		r.Route("/notifications", func(r chi.Router) {
			// writes need idempotency
			r.Group(func(r chi.Router) {
				r.Use(idempotencyMiddleware(d.Idempotency, d.Log))
				r.Post("/", d.Notif.Create)
				r.Post("/batch", d.Notif.CreateBatch)
			})
			// reads + cancel (cancel is idempotent by resource state)
			r.Get("/", d.Notif.List)
			r.Get("/{id}", d.Notif.Get)
			r.Post("/{id}/cancel", d.Notif.Cancel)
		})

		r.Get("/batches/{batch_id}", d.Batch.Get)

		if d.Template != nil {
			r.Route("/templates", func(r chi.Router) {
				// Creates (idempotency-guarded) and preview (read-only).
				r.Group(func(r chi.Router) {
					r.Use(idempotencyMiddleware(d.Idempotency, d.Log))
					r.Post("/", d.Template.Create)
				})
				r.Get("/", d.Template.List)
				r.Get("/{id}", d.Template.Get)
				r.Put("/{id}", d.Template.Update)
				r.Delete("/{id}", d.Template.Delete)
				// Preview is a dry-run — no persistence, no idempotency key needed.
				r.Post("/{id}/preview", d.Template.Preview)
			})
		}

		// WebSocket upgrade lives outside the auth middleware because browsers
		// cannot set Authorization on the upgrade request — the handler does
		// its own JWT verification against ?token=... or the Authorization header.
	})

	if d.WS != nil {
		r.Get("/v1/ws/batch/{batch_id}", d.WS.Subscribe)
	}

	return r
}
