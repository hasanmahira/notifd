package queue

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

	"github.com/hibiken/asynq"
	"github.com/hasanmahira/notifd/internal/config"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hasanmahira/notifd/internal/usecase"
)

// Server is the Asynq worker server. Priority queues are weighted 6/3/1 —
// matches the expected flash-sale arrival mix. Low queue cannot starve; Asynq
// uses weighted-round-robin, not strict priority.
type Server struct {
	srv    *asynq.Server
	sender *usecase.Sender
	log    *slog.Logger
	mux    *asynq.ServeMux
}

type ServerDeps struct {
	Cfg    config.Config
	Sender *usecase.Sender
	Log    *slog.Logger
}

func NewServer(d ServerDeps) *Server {
	opt := asynq.RedisClientOpt{
		Addr:     d.Cfg.Redis.Addr,
		Password: d.Cfg.Redis.Password,
		DB:       d.Cfg.Redis.DB,
	}
	cfg := asynq.Config{
		Concurrency: d.Cfg.Worker.Concurrency,
		Queues: map[string]int{
			"high":    6,
			"default": 3,
			"low":     1,
		},
		// RateLimitError is backpressure, NOT a failure: don't burn a retry.
		IsFailure: func(err error) bool {
			var rle *usecase.RateLimitError
			return !errors.As(err, &rle)
		},
		// Per-attempt delay: rate-limit -> configured retry-after; other errors -> exp backoff.
		//
		// Note on scalability: at high worker counts (N pods * C concurrency) the
		// bucket becomes the hot key and most tasks make multiple ratelimit calls
		// per successful send. Observed throughput stays *under* the 100/s cap
		// (correct per the design goal) but below theoretical max. A production
		// fix is a lease-based limiter where each worker reserves a shard of the
		// token budget for a short window — deferred as future work.
		RetryDelayFunc: func(n int, err error, _ *asynq.Task) time.Duration {
			var rle *usecase.RateLimitError
			if errors.As(err, &rle) && rle.RetryAfter > 0 {
				// Small jitter spreads bursts without inflating the tail latency.
				jitter := time.Duration(rand.IntN(50)) * time.Millisecond //nolint:gosec // jitter is not security-sensitive
				return rle.RetryAfter + jitter
			}
			return expBackoffWithJitter(n)
		},
		ShutdownTimeout: d.Cfg.Worker.ShutdownTimeout,
		Logger:          &slogAsynqAdapter{log: d.Log},
	}
	s := &Server{sender: d.Sender, log: d.Log}
	// Hook into the last retry: when Asynq is about to archive the task, flip
	// the DB status to dead_letter so the API surface reflects reality.
	cfg.ErrorHandler = asynq.ErrorHandlerFunc(s.onError)

	srv := asynq.NewServer(opt, cfg)
	s.srv = srv

	mux := asynq.NewServeMux()
	mux.HandleFunc(TypeSendNotification, s.handleSend)
	s.mux = mux
	return s
}

// Start blocks until Shutdown returns.
func (s *Server) Start() error {
	s.log.Info("asynq worker server starting")
	return s.srv.Run(s.mux)
}

// Shutdown triggers graceful drain.
func (s *Server) Shutdown() {
	s.log.Info("asynq worker server shutting down")
	s.srv.Shutdown()
}

func (s *Server) handleSend(ctx context.Context, t *asynq.Task) error {
	payload, err := unmarshal(t.Payload())
	if err != nil {
		return asynq.SkipRetry // malformed task — archive it rather than retry forever
	}
	// Propagate correlation id so worker logs join API logs.
	if payload.RequestID != "" {
		ctx = observ.WithRequestID(ctx, payload.RequestID)
	}
	ctx = observ.WithNotificationID(ctx, payload.NotificationID.String())
	return s.sender.Send(ctx, payload.NotificationID)
}

// onError fires after every failed handler return. If the task has no retries
// left, Asynq will archive it; we flip the DB row to dead_letter so readers
// see a consistent terminal state.
//
// NOTE: the ctx handed in here is the task context which Asynq has already
// signaled done. Calling MarkDeadLetter through that ctx would tie the DB
// write + Pub/Sub publish to a canceled deadline and the transition would
// never persist. Detach with a short fresh timeout.
func (s *Server) onError(parentCtx context.Context, t *asynq.Task, err error) {
	retried, _ := asynq.GetRetryCount(parentCtx)
	maxRetry, _ := asynq.GetMaxRetry(parentCtx)
	if retried+1 < maxRetry {
		s.log.Warn("task failed, will retry",
			slog.String("type", t.Type()),
			slog.Int("attempt", retried+1),
			slog.Int("max", maxRetry),
			slog.Any("err", err),
		)
		return
	}

	payload, perr := unmarshal(t.Payload())
	if perr != nil {
		s.log.Error("final attempt but payload unmarshal failed", slog.Any("err", perr))
		return
	}

	// Detach: fresh background ctx with a short budget so we're not tied to
	// Asynq's already-canceled parent.
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if merr := s.sender.MarkDeadLetter(bgCtx, payload.NotificationID, err.Error()); merr != nil {
		s.log.Error("mark dead_letter", slog.Any("err", merr))
	} else {
		s.log.Warn("task moved to dead_letter", slog.String("notification_id", payload.NotificationID.String()))
	}
}

// expBackoffWithJitter returns 1s, 2s, 4s, 8s, ... capped at ~2m, with ±20% jitter.
func expBackoffWithJitter(attempt int) time.Duration {
	base := time.Second * time.Duration(math.Pow(2, float64(attempt)))
	if base > 2*time.Minute {
		base = 2 * time.Minute
	}
	// ±20% jitter, non-negative.
	jitter := time.Duration(rand.Float64()*float64(base)*0.4 - float64(base)*0.2) //nolint:gosec
	if base+jitter < 0 {
		return base
	}
	return base + jitter
}

// slogAsynqAdapter wires Asynq's logger interface into our slog instance.
type slogAsynqAdapter struct{ log *slog.Logger }

func (a *slogAsynqAdapter) Debug(args ...any) { a.log.Debug("asynq", slog.Any("args", args)) }
func (a *slogAsynqAdapter) Info(args ...any)  { a.log.Info("asynq", slog.Any("args", args)) }
func (a *slogAsynqAdapter) Warn(args ...any)  { a.log.Warn("asynq", slog.Any("args", args)) }
func (a *slogAsynqAdapter) Error(args ...any) { a.log.Error("asynq", slog.Any("args", args)) }
func (a *slogAsynqAdapter) Fatal(args ...any) {
	a.log.Error("asynq FATAL", slog.Any("args", args))
}
