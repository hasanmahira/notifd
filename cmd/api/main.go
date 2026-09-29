package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hasanmahira/notifd/internal/config"
	httpdelivery "github.com/hasanmahira/notifd/internal/delivery/http"
	ws "github.com/hasanmahira/notifd/internal/delivery/websocket"
	"github.com/hasanmahira/notifd/internal/infrastructure/auth"
	"github.com/hasanmahira/notifd/internal/infrastructure/idempotency"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hasanmahira/notifd/internal/infrastructure/queue"
	"github.com/hasanmahira/notifd/internal/infrastructure/store"
	"github.com/hasanmahira/notifd/internal/usecase"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := observ.NewLogger(cfg.LogLevel, "api")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.OTel.Enabled {
		shutdown, err := observ.InitOTel(ctx, cfg.OTel.ServiceName+"-api")
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
	verifier := auth.New(cfg.JWT.Secret, cfg.JWT.Issuer)
	idemStore := idempotency.New(rd)
	publisher := queue.NewPublisher(cfg.Redis, cfg.Worker.MaxRetry, log)
	defer func() { _ = publisher.Close() }()

	// Background sampler fills notifd_queue_depth gauges every 5s.
	samplerStop := queue.StartDepthSampler(ctx, cfg.Redis, metrics, log, 5*time.Second)
	defer samplerStop()

	svc := usecase.NewService(usecase.ServiceDeps{
		Repo:      repo,
		Pub:       publisher,
		Templates: repo, // same *store.Repo implements both ports
		Metrics:   metrics,
		Log:       log,
	})
	tmplSvc := usecase.NewTemplateService(repo, nil)

	notif := httpdelivery.NewNotificationHandler(svc)
	batch := httpdelivery.NewBatchHandler(svc)
	tmplH := httpdelivery.NewTemplateHandler(tmplSvc)
	health := httpdelivery.NewHealthChecker(pg, rd, log)

	wsHub := ws.NewHub(rd, log, metrics)
	wsHandler := ws.NewHandler(wsHub, verifier, log, ws.Config{
		Env:         cfg.Env,
		OriginHosts: cfg.WS.OriginAllowList,
	})

	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel() // safety: also fires on any early return below
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		if err := wsHub.Run(hubCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("ws hub stopped", slog.Any("err", err))
		}
	}()

	router := httpdelivery.NewRouter(httpdelivery.RouterDeps{
		Health:      health,
		Notif:       notif,
		Batch:       batch,
		Template:    tmplH,
		WS:          wsHandler,
		Verifier:    verifier,
		Idempotency: idemStore,
		Metrics:     metrics,
		Log:         log,
	})

	srv := httpdelivery.NewServer(cfg.HTTPAddr, router, log)

	errs := make(chan error, 1)
	go func() { errs <- srv.Start() }()

	log.Info("api ready", slog.String("addr", cfg.HTTPAddr))

	select {
	case err := <-errs:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Deterministic shutdown order:
	//   1. close all active WebSocket conns (clients see 1001 Going Away)
	//   2. cancel the hub's Run ctx so its Redis subscribe goroutine returns
	//   3. wait for the hub goroutine to exit so rd.Close() (via defer) doesn't
	//      pull the rug out from under it and emit noisy errors
	//   4. drain HTTP
	wsHub.Shutdown(shutdownCtx)
	hubCancel()
	select {
	case <-hubDone:
	case <-shutdownCtx.Done():
		log.Warn("ws hub did not exit in shutdown window")
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", slog.Any("err", err))
	}
	log.Info("api stopped")
	return nil
}
