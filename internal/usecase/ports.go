package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// TemplateRepository persists notification templates. Kept as a separate
// interface so a future caller that only needs templates can mock a smaller
// surface.
type TemplateRepository interface {
	CreateTemplate(ctx context.Context, t domain.Template) error
	GetTemplate(ctx context.Context, id uuid.UUID) (domain.Template, error)
	UpdateTemplate(ctx context.Context, id uuid.UUID, subject *string, body string) error
	DeleteTemplate(ctx context.Context, id uuid.UUID) error
	ListTemplates(ctx context.Context) ([]domain.Template, error)
}

// Repository persists messages and their per-recipient delivery rows.
// Implementations: infrastructure/store/repo_notification.go.
type Repository interface {
	// CreateMessageAndUserNotifications inserts one message plus N per-recipient
	// rows atomically. The message and all user notifications share the same
	// batch_id when provided.
	CreateMessageAndUserNotifications(ctx context.Context, msg domain.Message, rows []domain.UserNotification) error

	// GetByID loads a single user_notification joined to its parent message.
	GetByID(ctx context.Context, id uuid.UUID) (domain.Notification, error)

	// GetByIdempotencyKey returns the existing notification for a key or ErrNotFound.
	GetByIdempotencyKey(ctx context.Context, key string) (domain.Notification, error)

	// List returns a page of notifications ordered by (created_at desc, id desc).
	List(ctx context.Context, q ListQuery) ([]domain.Notification, string, error)

	// GetBatch returns the aggregate summary for a batch_id.
	GetBatch(ctx context.Context, batchID uuid.UUID) (domain.BatchSummary, error)

	// UpdateStatus transitions a user_notifications row. Returns ErrConflict if
	// the current stored status forbids the requested transition.
	UpdateStatus(ctx context.Context, id uuid.UUID, upd StatusUpdate) error

	// IncrementAttempts bumps the attempts counter and stores last_error.
	IncrementAttempts(ctx context.Context, id uuid.UUID, lastError string) error
}

// StatusUpdate is a partial-update payload for UpdateStatus.
type StatusUpdate struct {
	Status            domain.Status
	ProviderMessageID *string
	SentAt            *time.Time
	LastError         *string
}

// ListQuery is the filter set for GET /v1/notifications.
type ListQuery struct {
	Status  *domain.Status
	Channel *domain.Channel
	From    *time.Time
	To      *time.Time
	Limit   int
	Cursor  string // opaque keyset cursor; empty for first page
}

// Publisher hands a notification off to the worker queue.
// Implementations: infrastructure/queue/asynq_client.go.
type Publisher interface {
	Publish(ctx context.Context, n domain.Notification) error
	// PublishAt schedules a future-dated task (scheduled notifications).
	PublishAt(ctx context.Context, n domain.Notification, when time.Time) error
	// Cancel removes a not-yet-running task from the queue. Safe to call even
	// if the task already completed.
	Cancel(ctx context.Context, n domain.Notification) error
}

// Provider delivers a rendered payload to the external mock (webhook.site).
// Implementations: infrastructure/provider/webhook_client.go.
type Provider interface {
	Send(ctx context.Context, n domain.Notification) (ProviderResult, error)
}

// ProviderResult captures the 202 response from the external provider.
type ProviderResult struct {
	MessageID string
	Status    string
	Timestamp time.Time
}

// RateLimiter is the per-channel token-bucket gate used on the worker side.
// Implementations: infrastructure/ratelimit/redis_bucket.go.
type RateLimiter interface {
	Acquire(ctx context.Context, ch domain.Channel) (RateLimitResult, error)
}

// RateLimitResult tells the worker whether to proceed, and if not, how long to wait.
type RateLimitResult struct {
	Allowed    bool
	RetryAfter time.Duration
}

// StatusPublisher fans out status-change events to Redis Pub/Sub for the WS hub.
type StatusPublisher interface {
	Publish(ctx context.Context, event StatusEvent) error
}

// StatusEvent is what worker->pubsub->websocket carries.
type StatusEvent struct {
	BatchID        *uuid.UUID
	NotificationID uuid.UUID
	Status         domain.Status
	Error          string
	Timestamp      time.Time
}

// IdempotencyStore is the Redis-backed guard in front of the HTTP layer.
// Claim returns (claimed=true) when no prior request has used `key` in the TTL
// window. If a prior request exists, `existingHash` is the SHA-256 hex digest
// of that first request's body, which the middleware compares to the incoming
// body so that same-key-with-different-body reliably returns 409.
type IdempotencyStore interface {
	Claim(ctx context.Context, key, bodyHash string, ttl time.Duration) (claimed bool, existingHash string, err error)
	SaveResponse(ctx context.Context, key string, status int, body []byte, ttl time.Duration) error
	LookupResponse(ctx context.Context, key string) (status int, body []byte, pending bool, err error)
}
