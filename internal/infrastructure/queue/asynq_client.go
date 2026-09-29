package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hasanmahira/notifd/internal/config"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hasanmahira/notifd/internal/usecase"
	"github.com/hibiken/asynq"
)

// Publisher is the Asynq-backed implementation of usecase.Publisher.
type Publisher struct {
	client    *asynq.Client
	inspector *asynq.Inspector
	log       *slog.Logger
	maxRetry  int
}

// NewPublisher constructs the Asynq client + inspector. Call Close on shutdown.
// `maxRetry` is the per-task retry ceiling; at 0 it falls back to 8.
func NewPublisher(rd config.RedisConfig, maxRetry int, log *slog.Logger) *Publisher {
	opt := asynq.RedisClientOpt{Addr: rd.Addr, Password: rd.Password, DB: rd.DB}
	if maxRetry <= 0 {
		maxRetry = 8
	}
	return &Publisher{
		client:    asynq.NewClient(opt),
		inspector: asynq.NewInspector(opt),
		log:       log,
		maxRetry:  maxRetry,
	}
}

func (p *Publisher) Close() error {
	var err error
	if c := p.client; c != nil {
		if e := c.Close(); e != nil {
			err = e
		}
	}
	if i := p.inspector; i != nil {
		_ = i.Close()
	}
	return err
}

// Publish enqueues an immediate-delivery task. Queue name comes from priority
// so the worker's weighted scheduler (see asynq_server.go) picks high first.
func (p *Publisher) Publish(ctx context.Context, n domain.Notification) error {
	return p.enqueue(ctx, n, nil)
}

// PublishAt schedules a future delivery.
func (p *Publisher) PublishAt(ctx context.Context, n domain.Notification, when time.Time) error {
	t := when
	return p.enqueue(ctx, n, &t)
}

// Cancel removes a task from the queue by NotificationID.
func (p *Publisher) Cancel(_ context.Context, n domain.Notification) error {
	// Asynq tasks use a TaskID we set at enqueue time to the NotificationID string.
	// DeleteTask is best-effort: if the task is currently being processed,
	// CancelProcessing asks the worker to stop; we call both.
	id := n.UserNotification.ID.String()
	queue := n.Message.Priority.Queue()

	if err := p.inspector.DeleteTask(queue, id); err != nil {
		// Not finding the task is not an error for Cancel — it may have already run.
		p.log.Debug("inspector delete", slog.String("notification_id", id), slog.String("err", err.Error()))
	}
	if err := p.inspector.CancelProcessing(id); err != nil {
		p.log.Debug("inspector cancel processing", slog.String("notification_id", id), slog.String("err", err.Error()))
	}
	return nil
}

func (p *Publisher) enqueue(ctx context.Context, n domain.Notification, at *time.Time) error {
	body, err := marshal(SendPayload{
		NotificationID: n.UserNotification.ID,
		BatchID:        n.Message.BatchID,
		Channel:        string(n.Message.Channel),
		RequestID:      observ.RequestID(ctx),
	})
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	task := asynq.NewTask(TypeSendNotification, body, asynq.TaskID(n.UserNotification.ID.String()))

	opts := []asynq.Option{
		asynq.Queue(n.Message.Priority.Queue()),
		asynq.MaxRetry(p.maxRetry),
		asynq.Timeout(30 * time.Second),
	}
	if at != nil {
		opts = append(opts, asynq.ProcessAt(*at))
	}
	if _, err := p.client.EnqueueContext(ctx, task, opts...); err != nil {
		return fmt.Errorf("asynq enqueue: %w", err)
	}
	return nil
}

// compile-time port check
var _ usecase.Publisher = (*Publisher)(nil)
