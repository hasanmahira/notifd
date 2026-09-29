package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hasanmahira/notifd/internal/config"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hasanmahira/notifd/internal/infrastructure/provider"
	"github.com/hasanmahira/notifd/internal/infrastructure/pubsub"
	"github.com/hasanmahira/notifd/internal/infrastructure/queue"
	"github.com/hasanmahira/notifd/internal/infrastructure/ratelimit"
	"github.com/hasanmahira/notifd/internal/infrastructure/store"
	"github.com/hasanmahira/notifd/internal/usecase"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := observ.NewLogger(cfg.LogLevel, "worker")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.OTel.Enabled {
		shutdown, err := observ.InitOTel(ctx, cfg.OTel.ServiceName+"-worker")
		if err != nil {
			log.Warn("otel init failed", slog.Any("err", err))
		} else {
			defer func() {
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = shutdown(sctx)
			}()
		}
	}

	metrics := observ.NewMetrics()

	pg, err := store.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pg.Close()

	rd, err := store.NewRedis(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()

	repo := store.NewRepo(pg)

	breaker := provider.NewBreaker("webhook", log)
	prov := provider.New(provider.Config{
		URL:     cfg.Provider.WebhookURL,
		Timeout: cfg.Provider.Timeout,
	}, breaker, log, metrics)

	limiter := ratelimit.New(rd, ratelimit.Config{Capacity: 100, RefillPerSec: 100})
	statusPub := pubsub.NewPublisher(rd)

	sender := usecase.NewSender(usecase.SenderDeps{
		Repo:            repo,
		Provider:        prov,
		Limiter:         limiter,
		StatusPublisher: statusPub,
		Metrics:         metrics,
		Log:             log,
	})

	srv := queue.NewServer(queue.ServerDeps{Cfg: cfg, Sender: sender, Log: log})

	// Metrics exposition: worker serves /metrics and /healthz too so Prometheus
	// can scrape both instances.
	startMetricsServer(ctx, metrics, log)

	errs := make(chan error, 1)
	go func() { errs <- srv.Start() }()

	log.Info("worker ready",
		slog.String("redis", cfg.Redis.Addr),
		slog.Int("concurrency", cfg.Worker.Concurrency),
	)

	select {
	case err := <-errs:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		log.Info("shutdown signal received, draining worker")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Worker.ShutdownTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() { srv.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		log.Warn("worker shutdown timeout exceeded")
	}
	log.Info("worker stopped")
	return nil
}
