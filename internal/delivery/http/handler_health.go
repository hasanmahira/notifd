package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// HealthChecker is the dependency bundle for /healthz and /readyz.
type HealthChecker struct {
	PG     *pgxpool.Pool
	Redis  *redis.Client
	Logger *slog.Logger
}

func NewHealthChecker(pg *pgxpool.Pool, rd *redis.Client, log *slog.Logger) *HealthChecker {
	return &HealthChecker{PG: pg, Redis: rd, Logger: log}
}

// Liveness always returns 200 when the process is able to handle HTTP.
// Use this for container liveness probes (is the process alive?).
func (h *HealthChecker) Liveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readiness checks that PG and Redis are reachable within 500ms.
// Use this for container readiness probes (can the process serve traffic?).
func (h *HealthChecker) Readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()

	status := map[string]string{"postgres": "ok", "redis": "ok"}
	code := http.StatusOK

	if h.PG != nil {
		if err := h.PG.Ping(ctx); err != nil {
			status["postgres"] = err.Error()
			code = http.StatusServiceUnavailable
		}
	}
	if h.Redis != nil {
		if err := h.Redis.Ping(ctx).Err(); err != nil {
			status["redis"] = err.Error()
			code = http.StatusServiceUnavailable
		}
	}

	writeJSON(w, code, status)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
