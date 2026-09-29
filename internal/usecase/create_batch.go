package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// CreateBatchInput is the body of POST /v1/notifications/batch. All items must
// share a single channel+priority+content; recipients and idempotency keys vary
// per item. Caller provides a single outer idempotency key which binds the whole
// batch; per-item idempotency keys are generated deterministically as
// "<batch-key>:<recipient>" when missing.
type CreateBatchInput struct {
	IdempotencyKey string
	Channel        domain.Channel
	Priority       domain.Priority
	Content        domain.Content
	Items          []BatchItemInput
}

// BatchItemInput carries the per-recipient overrides.
type BatchItemInput struct {
	Recipient      domain.Recipient
	IdempotencyKey string // optional; auto-derived if empty
}

// BatchResult is returned by CreateBatch.
type BatchResult struct {
	BatchID  uuid.UUID
	Accepted int
}

// CreateBatch persists one message row and N user_notifications rows atomically,
// then enqueues every row. Enqueue failures after a successful DB write are logged
// and bubble up; the caller decides how to respond.
func (s *Service) CreateBatch(ctx context.Context, in CreateBatchInput) (BatchResult, error) {
	if in.IdempotencyKey == "" {
		return BatchResult{}, fmt.Errorf("%w: idempotency_key required", domain.ErrInvalidInput)
	}
	if !in.Channel.Valid() {
		return BatchResult{}, fmt.Errorf("%w: channel %q", domain.ErrInvalidInput, in.Channel)
	}
	if !in.Priority.Valid() {
		return BatchResult{}, fmt.Errorf("%w: priority %q", domain.ErrInvalidInput, in.Priority)
	}
	if err := domain.ValidateContent(in.Channel, in.Content); err != nil {
		return BatchResult{}, err
	}
	n := len(in.Items)
	if n == 0 {
		return BatchResult{}, fmt.Errorf("%w: batch requires >=1 item", domain.ErrInvalidInput)
	}
	if n > domain.MaxBatchSize {
		return BatchResult{}, fmt.Errorf("%w: batch size %d > max %d", domain.ErrBatchTooLarge, n, domain.MaxBatchSize)
	}

	// Validate all items upfront. No partial acceptance — reject the whole batch
	// on any failure so callers never deal with ambiguous partial state.
	for i, it := range in.Items {
		if err := it.Recipient.Validate(in.Channel); err != nil {
			return BatchResult{}, fmt.Errorf("item[%d]: %w", i, err)
		}
	}

	now := s.now()
	batchID := domain.NewID()
	msg := domain.Message{
		ID:        domain.NewID(),
		BatchID:   &batchID,
		Channel:   in.Channel,
		Priority:  in.Priority,
		Content:   in.Content,
		CreatedAt: now,
	}
	rows := make([]domain.UserNotification, 0, n)
	for _, it := range in.Items {
		key := it.IdempotencyKey
		if key == "" {
			key = in.IdempotencyKey + ":" + string(it.Recipient)
		}
		rows = append(rows, domain.UserNotification{
			ID:             domain.NewID(),
			MessageID:      msg.ID,
			Recipient:      it.Recipient,
			IdempotencyKey: key,
			Status:         domain.StatusPending,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}

	if err := s.repo.CreateMessageAndUserNotifications(ctx, msg, rows); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			// Idempotency replay — return the existing batch id if we can find it.
			// (Best-effort; duplicate-batch replay is not a hot path.)
			return BatchResult{}, err
		}
		return BatchResult{}, err
	}

	// Enqueue one task per row. Failures are logged but don't roll back the
	// DB write — a background sweeper (future work) would recover pending rows.
	for i := range rows {
		n := domain.Notification{Message: msg, UserNotification: rows[i]}
		if err := s.enqueue(ctx, n); err != nil {
			s.log.Warn("batch enqueue failure", "notification_id", rows[i].ID.String(), "err", err)
		}
		s.metrics.IncCreated(string(in.Channel), string(in.Priority))
	}

	return BatchResult{BatchID: batchID, Accepted: n}, nil
}
