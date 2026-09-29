package usecase

import (
	"context"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// Get fetches a single notification by id.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Notification, error) {
	return s.repo.GetByID(ctx, id)
}

// GetBatch returns the aggregate summary for a batch.
func (s *Service) GetBatch(ctx context.Context, batchID uuid.UUID) (domain.BatchSummary, error) {
	return s.repo.GetBatch(ctx, batchID)
}

// List returns a page of notifications plus an opaque cursor for the next page.
// Empty cursor string means there are no more rows.
func (s *Service) List(ctx context.Context, q ListQuery) ([]domain.Notification, string, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	return s.repo.List(ctx, q)
}
