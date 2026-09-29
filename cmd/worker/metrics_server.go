package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// startMetricsServer exposes /metrics and /healthz on :9100 for Prometheus to
// scrape the worker process alongside the API. Runs in the background; exits
// when ctx is canceled.
func startMetricsServer(ctx context.Context, m *observ.Metrics, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:              ":9100",
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("worker metrics server stopped", slog.Any("err", err))
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
}
