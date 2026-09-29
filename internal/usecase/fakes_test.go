package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// fakeRepo is a minimal in-memory Repository used by usecase tests. It validates
// the idempotency unique-constraint rule (same key rejected) and exposes
// stored rows for assertions.
type fakeRepo struct {
	mu      sync.Mutex
	msgs    map[uuid.UUID]domain.Message
	rows    map[uuid.UUID]domain.UserNotification
	byKey   map[string]uuid.UUID // idempotency_key -> user_notification id
	listErr error                // injectable error for List
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		msgs:  map[uuid.UUID]domain.Message{},
		rows:  map[uuid.UUID]domain.UserNotification{},
		byKey: map[string]uuid.UUID{},
	}
}

func (f *fakeRepo) CreateMessageAndUserNotifications(_ context.Context, m domain.Message, rows []domain.UserNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range rows {
		if _, dup := f.byKey[r.IdempotencyKey]; dup {
			return fmt.Errorf("%w: duplicate idempotency key", domain.ErrConflict)
		}
	}
	f.msgs[m.ID] = m
	for _, r := range rows {
		f.rows[r.ID] = r
		f.byKey[r.IdempotencyKey] = r.ID
	}
	return nil
}

func (f *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return domain.Notification{}, domain.ErrNotFound
	}
	return domain.Notification{Message: f.msgs[r.MessageID], UserNotification: r}, nil
}

func (f *fakeRepo) GetByIdempotencyKey(_ context.Context, key string) (domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byKey[key]
	if !ok {
		return domain.Notification{}, domain.ErrNotFound
	}
	r := f.rows[id]
	return domain.Notification{Message: f.msgs[r.MessageID], UserNotification: r}, nil
}

func (f *fakeRepo) List(_ context.Context, q ListQuery) ([]domain.Notification, string, error) {
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Notification, 0, len(f.rows))
	for _, r := range f.rows {
		if q.Status != nil && r.Status != *q.Status {
			continue
		}
		m := f.msgs[r.MessageID]
		if q.Channel != nil && m.Channel != *q.Channel {
			continue
		}
		out = append(out, domain.Notification{Message: m, UserNotification: r})
	}
	return out, "", nil
}

func (f *fakeRepo) GetBatch(_ context.Context, batchID uuid.UUID) (domain.BatchSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	summary := domain.BatchSummary{BatchID: batchID, ByStatus: map[domain.Status]int{}}
	for _, m := range f.msgs {
		if m.BatchID == nil || *m.BatchID != batchID {
			continue
		}
		summary.CreatedAt = m.CreatedAt
		for _, r := range f.rows {
			if r.MessageID == m.ID {
				summary.Total++
				summary.ByStatus[r.Status]++
			}
		}
	}
	if summary.Total == 0 {
		return domain.BatchSummary{}, domain.ErrNotFound
	}
	return summary, nil
}

func (f *fakeRepo) UpdateStatus(_ context.Context, id uuid.UUID, upd StatusUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return domain.ErrNotFound
	}
	if r.Status.Terminal() {
		return nil // no-op; mirrors SQL WHERE clause
	}
	r.Status = upd.Status
	if upd.ProviderMessageID != nil {
		r.ProviderMessageID = upd.ProviderMessageID
	}
	if upd.SentAt != nil {
		r.SentAt = upd.SentAt
	}
	if upd.LastError != nil {
		r.LastError = upd.LastError
	}
	r.UpdatedAt = time.Now()
	f.rows[id] = r
	return nil
}

func (f *fakeRepo) IncrementAttempts(_ context.Context, id uuid.UUID, last string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return domain.ErrNotFound
	}
	r.Attempts++
	r.LastError = &last
	f.rows[id] = r
	return nil
}

// fakePublisher records enqueue calls without doing anything real.
type fakePublisher struct {
	mu       sync.Mutex
	enq      []uuid.UUID
	cancel   []uuid.UUID
	enqueued map[uuid.UUID]time.Time // id -> scheduled at (zero for immediate)
	err      error
}

func newFakePublisher() *fakePublisher {
	return &fakePublisher{enqueued: map[uuid.UUID]time.Time{}}
}

func (f *fakePublisher) Publish(_ context.Context, n domain.Notification) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enq = append(f.enq, n.UserNotification.ID)
	f.enqueued[n.UserNotification.ID] = time.Time{}
	return nil
}

func (f *fakePublisher) PublishAt(_ context.Context, n domain.Notification, when time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enq = append(f.enq, n.UserNotification.ID)
	f.enqueued[n.UserNotification.ID] = when
	return nil
}

func (f *fakePublisher) Cancel(_ context.Context, n domain.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel = append(f.cancel, n.UserNotification.ID)
	return nil
}

// --- worker-side fakes ---

// fakeProvider lets each test swap in a canned Send behavior. The default is
// a successful 202-style response.
type fakeProvider struct {
	mu     sync.Mutex
	calls  int
	sendFn func(context.Context, domain.Notification) (ProviderResult, error)
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		sendFn: func(_ context.Context, _ domain.Notification) (ProviderResult, error) {
			return ProviderResult{MessageID: "mock-123", Status: "accepted", Timestamp: time.Now()}, nil
		},
	}
}

func (f *fakeProvider) Send(ctx context.Context, n domain.Notification) (ProviderResult, error) {
	f.mu.Lock()
	f.calls++
	fn := f.sendFn
	f.mu.Unlock()
	return fn(ctx, n)
}

// fakeLimiter yields a programmable sequence of decisions. Empty -> always allow.
type fakeLimiter struct {
	mu    sync.Mutex
	calls int
	fn    func(domain.Channel) (RateLimitResult, error)
}

func newFakeLimiter() *fakeLimiter {
	return &fakeLimiter{fn: func(domain.Channel) (RateLimitResult, error) {
		return RateLimitResult{Allowed: true}, nil
	}}
}

func (f *fakeLimiter) Acquire(_ context.Context, ch domain.Channel) (RateLimitResult, error) {
	f.mu.Lock()
	f.calls++
	fn := f.fn
	f.mu.Unlock()
	return fn(ch)
}

// fakeStatusPub records published status events for assertion.
type fakeStatusPub struct {
	mu     sync.Mutex
	events []StatusEvent
}

func newFakeStatusPub() *fakeStatusPub { return &fakeStatusPub{} }

func (f *fakeStatusPub) Publish(_ context.Context, e StatusEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeStatusPub) Events() []StatusEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]StatusEvent, len(f.events))
	copy(out, f.events)
	return out
}

// helper to guard against shadowed imports in minimal tests
var _ = errors.New
