package usecase

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// Cancel attempts to cancel a pending or processing notification.
// Returns ErrConflict if the notification is already in a terminal state.
// The flow: (1) load, (2) refuse if terminal, (3) update DB, (4) remove from
// queue. Queue removal failures are non-fatal — the worker will still see the
// canceled status when it dequeues and bail.
func (s *Service) Cancel(ctx context.Context, id uuid.UUID) (domain.Notification, error) {
	n, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return domain.Notification{}, err
	}
	if !n.UserNotification.Status.Cancelable() {
		return n, fmt.Errorf("%w: status %s is not cancelable", domain.ErrConflict, n.UserNotification.Status)
	}

	if err := s.repo.UpdateStatus(ctx, id, StatusUpdate{Status: domain.StatusCanceled}); err != nil {
		return domain.Notification{}, err
	}
	if err := s.pub.Cancel(ctx, n); err != nil {
		s.log.Warn("queue cancel returned error; status already flipped in DB",
			"notification_id", id.String(), "err", err)
	}
	n.UserNotification.Status = domain.StatusCanceled
	return n, nil
}
