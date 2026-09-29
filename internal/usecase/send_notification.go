package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// Sender is the worker-side service. It is intentionally separate from the
// API-facing Service so each has its own dependency surface.
type Sender struct {
	repo      Repository
	provider  Provider
	limiter   RateLimiter
	publisher StatusPublisher
	metrics   MetricsRecorder
	log       *slog.Logger
	now       func() time.Time
}

type SenderDeps struct {
	Repo            Repository
	Provider        Provider
	Limiter         RateLimiter
	StatusPublisher StatusPublisher
	Metrics         MetricsRecorder
	Log             *slog.Logger
	Now             func() time.Time
}

func NewSender(deps SenderDeps) *Sender {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Metrics == nil {
		deps.Metrics = NoopRecorder{}
	}
	return &Sender{
		repo:      deps.Repo,
		provider:  deps.Provider,
		limiter:   deps.Limiter,
		publisher: deps.StatusPublisher,
		metrics:   deps.Metrics,
		log:       deps.Log,
		now:       deps.Now,
	}
}

// RateLimitError is a typed error the worker returns when the distributed rate
// limiter denies a token. Asynq's IsFailure / RetryDelayFunc treat this
// specially: NOT counted as a failure, and re-enqueued after the suggested delay.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited; retry after %s", e.RetryAfter)
}

// Send executes one delivery attempt: pre-check cancel, acquire rate token,
// call provider, persist result, publish status event.
func (s *Sender) Send(ctx context.Context, id uuid.UUID) error {
	n, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if n.UserNotification.Status.Terminal() {
		// Cancel-in-flight, dead_letter-replay, or double-dispatch. Nothing to do.
		return nil
	}

	// Rate-limit gate. On denial, return RateLimitError so Asynq re-enqueues.
	lim, err := s.limiter.Acquire(ctx, n.Message.Channel)
	if err != nil {
		// Redis outage: degrade to no-op allow. We log so ops can see it.
		s.log.Warn("ratelimit acquire failed; degrading to allow", "err", err)
	} else if !lim.Allowed {
		s.metrics.IncRateLimitDeny(string(n.Message.Channel))
		return &RateLimitError{RetryAfter: lim.RetryAfter}
	}

	// Mark processing. If UpdateStatus returns ErrTerminal here, a concurrent
	// Cancel (or a previously-succeeded attempt after Asynq double-dispatch)
	// already committed a terminal status — do NOT call the provider, else a
	// canceled notification would still be physically delivered.
	if err := s.repo.UpdateStatus(ctx, id, StatusUpdate{Status: domain.StatusProcessing}); err != nil {
		if errors.Is(err, domain.ErrTerminal) {
			s.log.Info("send skipped: terminal state won the race",
				"notification_id", id.String(), "err", err)
			return nil
		}
		return err
	}

	res, err := s.provider.Send(ctx, n)
	if err != nil {
		return s.handleProviderError(ctx, n, err)
	}

	sentAt := s.now()
	upd := StatusUpdate{
		Status:            domain.StatusSent,
		ProviderMessageID: &res.MessageID,
		SentAt:            &sentAt,
	}
	if err := s.repo.UpdateStatus(ctx, id, upd); err != nil {
		return err
	}
	s.metrics.IncSent(string(n.Message.Channel), string(domain.StatusSent))
	s.emitStatus(ctx, n, domain.StatusSent, "")
	return nil
}

func (s *Sender) handleProviderError(ctx context.Context, n domain.Notification, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	_ = s.repo.IncrementAttempts(ctx, n.UserNotification.ID, msg)

	if errors.Is(err, domain.ErrProviderTerminal) {
		_ = s.repo.UpdateStatus(ctx, n.UserNotification.ID, StatusUpdate{
			Status:    domain.StatusFailed,
			LastError: &msg,
		})
		s.metrics.IncSent(string(n.Message.Channel), string(domain.StatusFailed))
		s.emitStatus(ctx, n, domain.StatusFailed, msg)
		return nil // terminal; tell Asynq this attempt is "done" (no retry)
	}

	// Transient — flip status back to pending so a future dequeue sees correct
	// state if the retry scheduler ends up delayed.
	_ = s.repo.UpdateStatus(ctx, n.UserNotification.ID, StatusUpdate{
		Status:    domain.StatusPending,
		LastError: &msg,
	})
	s.metrics.IncRetry(string(n.Message.Channel))
	return err // bubble up so Asynq retries with exponential backoff
}

func (s *Sender) emitStatus(ctx context.Context, n domain.Notification, st domain.Status, errMsg string) {
	if s.publisher == nil {
		return
	}
	evt := StatusEvent{
		BatchID:        n.Message.BatchID,
		NotificationID: n.UserNotification.ID,
		Status:         st,
		Error:          errMsg,
		Timestamp:      s.now(),
	}
	if err := s.publisher.Publish(ctx, evt); err != nil {
		s.log.Warn("status publish failed", "err", err)
	}
}

// MarkDeadLetter is called by the worker when Asynq's retry exhaustion hook fires.
func (s *Sender) MarkDeadLetter(ctx context.Context, id uuid.UUID, reason string) error {
	r := reason
	if err := s.repo.UpdateStatus(ctx, id, StatusUpdate{
		Status:    domain.StatusDeadLetter,
		LastError: &r,
	}); err != nil {
		return err
	}
	n, err := s.repo.GetByID(ctx, id)
	if err == nil {
		s.metrics.IncDeadLetter(string(n.Message.Channel))
		s.emitStatus(ctx, n, domain.StatusDeadLetter, reason)
	}
	return nil
}
