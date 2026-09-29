package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// seed creates one message + one user_notification row in the fake repo and
// returns its id. Shared across the Sender tests.
func seed(t *testing.T, repo *fakeRepo) uuid.UUID {
	t.Helper()
	msgID := domain.NewID()
	unID := domain.NewID()
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	msg := domain.Message{
		ID:        msgID,
		Channel:   domain.ChannelSMS,
		Priority:  domain.PriorityHigh,
		Content:   domain.Content([]byte(`{"text":"hi"}`)),
		CreatedAt: now,
	}
	un := domain.UserNotification{
		ID:             unID,
		MessageID:      msgID,
		Recipient:      "+905551234567",
		IdempotencyKey: "k-" + unID.String(),
		Status:         domain.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repo.CreateMessageAndUserNotifications(context.Background(), msg, []domain.UserNotification{un}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return unID
}

func newSenderForTest(t *testing.T) (*Sender, *fakeRepo, *fakeProvider, *fakeLimiter, *fakeStatusPub) {
	t.Helper()
	repo := newFakeRepo()
	prov := newFakeProvider()
	lim := newFakeLimiter()
	pub := newFakeStatusPub()
	s := NewSender(SenderDeps{
		Repo:            repo,
		Provider:        prov,
		Limiter:         lim,
		StatusPublisher: pub,
		Log:             slog.New(slog.NewTextHandler(newTestWriter(t), nil)),
		Now:             func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) },
	})
	return s, repo, prov, lim, pub
}

// --- happy path ---

func TestSender_HappyPath(t *testing.T) {
	s, repo, prov, _, pub := newSenderForTest(t)
	id := seed(t, repo)

	if err := s.Send(context.Background(), id); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := repo.rows[id]
	if got.Status != domain.StatusSent {
		t.Fatalf("status: want sent, got %s", got.Status)
	}
	if got.ProviderMessageID == nil || *got.ProviderMessageID != "mock-123" {
		t.Fatalf("provider_message_id missing or wrong: %+v", got.ProviderMessageID)
	}
	if got.SentAt == nil {
		t.Fatalf("sent_at should be set after successful send")
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls: want 1, got %d", prov.calls)
	}
	evts := pub.Events()
	if len(evts) != 1 || evts[0].Status != domain.StatusSent {
		t.Fatalf("status events: want [sent], got %+v", evts)
	}
}

// --- rate-limit denial -> typed error, no provider hit ---

func TestSender_RateLimitDenied(t *testing.T) {
	s, repo, prov, lim, pub := newSenderForTest(t)
	id := seed(t, repo)

	lim.fn = func(domain.Channel) (RateLimitResult, error) {
		return RateLimitResult{Allowed: false, RetryAfter: 42 * time.Millisecond}, nil
	}

	err := s.Send(context.Background(), id)
	var rle *RateLimitError
	if err == nil || !errors.As(err, &rle) {
		t.Fatalf("want *RateLimitError, got %v", err)
	}
	if rle.RetryAfter != 42*time.Millisecond {
		t.Fatalf("retry after: want 42ms, got %v", rle.RetryAfter)
	}
	if prov.calls != 0 {
		t.Fatalf("provider must not be hit on denial; calls=%d", prov.calls)
	}
	if repo.rows[id].Status != domain.StatusPending {
		t.Fatalf("status should stay pending; got %s", repo.rows[id].Status)
	}
	if len(pub.Events()) != 0 {
		t.Fatalf("no status event expected on rate-limit denial")
	}
}

// --- transient provider error -> bubbles up, status flipped back to pending ---

func TestSender_TransientProviderError(t *testing.T) {
	s, repo, prov, _, pub := newSenderForTest(t)
	id := seed(t, repo)
	prov.sendFn = func(context.Context, domain.Notification) (ProviderResult, error) {
		return ProviderResult{}, fmt.Errorf("%w: 503 provider down", domain.ErrProviderTransient)
	}

	err := s.Send(context.Background(), id)
	if err == nil {
		t.Fatalf("want transient error to bubble, got nil")
	}
	if !errors.Is(err, domain.ErrProviderTransient) {
		t.Fatalf("want ErrProviderTransient, got %v", err)
	}
	if repo.rows[id].Status != domain.StatusPending {
		t.Fatalf("status should be back to pending for retry; got %s", repo.rows[id].Status)
	}
	if repo.rows[id].Attempts != 1 {
		t.Fatalf("attempts: want 1, got %d", repo.rows[id].Attempts)
	}
	if len(pub.Events()) != 0 {
		t.Fatalf("transient errors do not emit terminal status events")
	}
}

// --- terminal provider error -> status=failed, no retry ---

func TestSender_TerminalProviderError(t *testing.T) {
	s, repo, _, _, pub := newSenderForTest(t)
	id := seed(t, repo)

	_, prov, _, _ := setProviderTerminal(t, s)
	_ = prov

	err := s.Send(context.Background(), id)
	if err != nil {
		t.Fatalf("terminal error should be swallowed (Asynq skip retry), got %v", err)
	}
	if repo.rows[id].Status != domain.StatusFailed {
		t.Fatalf("status: want failed, got %s", repo.rows[id].Status)
	}
	evts := pub.Events()
	if len(evts) != 1 || evts[0].Status != domain.StatusFailed {
		t.Fatalf("status events: want [failed], got %+v", evts)
	}
}

// helper to replace the provider inside a Sender. We reach in via reflection-free
// test-only accessor to avoid exposing sender internals. Instead, we rebuild
// the sender for this one case.
func setProviderTerminal(t *testing.T, s *Sender) (*Sender, *fakeProvider, *fakeLimiter, *fakeStatusPub) {
	t.Helper()
	prov := newFakeProvider()
	prov.sendFn = func(context.Context, domain.Notification) (ProviderResult, error) {
		return ProviderResult{}, fmt.Errorf("%w: 400 bad recipient", domain.ErrProviderTerminal)
	}
	s.provider = prov
	return s, prov, nil, nil
}

// --- cancel-in-flight: status canceled before dequeue -> send no-ops ---

func TestSender_SkipsCanceled(t *testing.T) {
	s, repo, prov, lim, pub := newSenderForTest(t)
	id := seed(t, repo)
	// flip to canceled
	row := repo.rows[id]
	row.Status = domain.StatusCanceled
	repo.rows[id] = row

	if err := s.Send(context.Background(), id); err != nil {
		t.Fatalf("send on canceled row should be no-op, got %v", err)
	}
	if prov.calls != 0 {
		t.Fatalf("provider must not be called for canceled row")
	}
	if lim.calls != 0 {
		t.Fatalf("limiter must not be called for canceled row")
	}
	if len(pub.Events()) != 0 {
		t.Fatalf("no events expected for canceled row")
	}
}

// --- MarkDeadLetter moves status, bumps metric (via publisher side-effect) ---

func TestSender_MarkDeadLetter(t *testing.T) {
	s, repo, _, _, pub := newSenderForTest(t)
	id := seed(t, repo)

	if err := s.MarkDeadLetter(context.Background(), id, "max retries exceeded"); err != nil {
		t.Fatalf("mark dlq: %v", err)
	}
	if repo.rows[id].Status != domain.StatusDeadLetter {
		t.Fatalf("status: want dead_letter, got %s", repo.rows[id].Status)
	}
	evts := pub.Events()
	if len(evts) != 1 || evts[0].Status != domain.StatusDeadLetter {
		t.Fatalf("status events: want [dead_letter], got %+v", evts)
	}
}

// --- ensure JSON unmarshal of Content isn't broken by tests ---

func TestContent_JSONShape(t *testing.T) {
	c := domain.Content(`{"text":"hi"}`)
	var m map[string]string
	if err := json.Unmarshal(c, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["text"] != "hi" {
		t.Fatalf("text: want hi, got %s", m["text"])
	}
}
