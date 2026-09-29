package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/hasanmahira/notifd/internal/domain"
)

func mkContent(t *testing.T, v any) domain.Content {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return domain.Content(b)
}

func newServiceForTest(t *testing.T) (*Service, *fakeRepo, *fakePublisher) {
	t.Helper()
	repo := newFakeRepo()
	pub := newFakePublisher()
	svc := NewService(ServiceDeps{
		Repo: repo,
		Pub:  pub,
		Log:  slog.New(slog.NewTextHandler(newTestWriter(t), nil)),
		Now:  func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) },
	})
	return svc, repo, pub
}

type testWriter struct{ t *testing.T }

func newTestWriter(t *testing.T) *testWriter { return &testWriter{t: t} }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func TestCreate_HappyPath_SMS(t *testing.T) {
	svc, repo, pub := newServiceForTest(t)

	in := CreateNotificationInput{
		IdempotencyKey: "key-1",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityHigh,
		Content:        mkContent(t, map[string]string{"text": "hi"}),
	}
	got, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.UserNotification.Status != domain.StatusPending {
		t.Fatalf("status: want pending, got %s", got.UserNotification.Status)
	}
	if got.Message.Channel != domain.ChannelSMS {
		t.Fatalf("channel: want sms, got %s", got.Message.Channel)
	}
	if len(repo.rows) != 1 {
		t.Fatalf("repo rows: want 1, got %d", len(repo.rows))
	}
	if len(pub.enq) != 1 {
		t.Fatalf("enqueue calls: want 1, got %d", len(pub.enq))
	}
}

func TestCreate_IdempotencyReturnsExisting(t *testing.T) {
	svc, _, pub := newServiceForTest(t)

	in := CreateNotificationInput{
		IdempotencyKey: "dup",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Content:        mkContent(t, map[string]string{"text": "hello"}),
	}
	first, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("second create (should be idempotent): %v", err)
	}
	if first.UserNotification.ID != second.UserNotification.ID {
		t.Fatalf("idempotent create returned different ids: %s vs %s",
			first.UserNotification.ID, second.UserNotification.ID)
	}
	if got := len(pub.enq); got != 1 {
		t.Fatalf("enqueue count: want 1 (second create must not re-enqueue), got %d", got)
	}
}

func TestCreate_ValidationErrors(t *testing.T) {
	svc, _, _ := newServiceForTest(t)

	cases := []struct {
		name string
		mut  func(in *CreateNotificationInput)
	}{
		{"missing idempotency", func(in *CreateNotificationInput) { in.IdempotencyKey = "" }},
		{"bad channel", func(in *CreateNotificationInput) { in.Channel = "carrierpigeon" }},
		{"bad priority", func(in *CreateNotificationInput) { in.Priority = "urgent" }},
		{"bad recipient", func(in *CreateNotificationInput) { in.Recipient = "not-a-phone" }},
		{"empty content", func(in *CreateNotificationInput) { in.Content = domain.Content(`{}`) }},
		{"oversize sms", func(in *CreateNotificationInput) {
			long := make([]byte, domain.MaxSMSChars+10)
			for i := range long {
				long[i] = 'a'
			}
			in.Content = mkContent(t, map[string]string{"text": string(long)})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := CreateNotificationInput{
				IdempotencyKey: "k",
				Recipient:      "+905551234567",
				Channel:        domain.ChannelSMS,
				Priority:       domain.PriorityNormal,
				Content:        mkContent(t, map[string]string{"text": "ok"}),
			}
			c.mut(&in)
			_, err := svc.Create(context.Background(), in)
			if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("want ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestCreateBatch_AcceptsAndEnqueues(t *testing.T) {
	svc, repo, pub := newServiceForTest(t)

	items := []BatchItemInput{
		{Recipient: "+905551111111"},
		{Recipient: "+905552222222"},
		{Recipient: "+905553333333"},
	}
	in := CreateBatchInput{
		IdempotencyKey: "batch-1",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Content:        mkContent(t, map[string]string{"text": "hi"}),
		Items:          items,
	}
	res, err := svc.CreateBatch(context.Background(), in)
	if err != nil {
		t.Fatalf("batch create: %v", err)
	}
	if res.Accepted != 3 {
		t.Fatalf("accepted: want 3, got %d", res.Accepted)
	}
	if len(repo.rows) != 3 {
		t.Fatalf("rows: want 3, got %d", len(repo.rows))
	}
	if len(repo.msgs) != 1 {
		t.Fatalf("messages: want 1 (fan-out), got %d", len(repo.msgs))
	}
	if len(pub.enq) != 3 {
		t.Fatalf("enqueue: want 3, got %d", len(pub.enq))
	}
}

func TestCreateBatch_RejectsInvalidItem(t *testing.T) {
	svc, _, _ := newServiceForTest(t)
	in := CreateBatchInput{
		IdempotencyKey: "batch-2",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Content:        mkContent(t, map[string]string{"text": "hi"}),
		Items: []BatchItemInput{
			{Recipient: "+905551111111"},
			{Recipient: "not-a-phone"},
		},
	}
	_, err := svc.CreateBatch(context.Background(), in)
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestCreateBatch_RejectsOversizedBatch(t *testing.T) {
	svc, _, _ := newServiceForTest(t)
	items := make([]BatchItemInput, domain.MaxBatchSize+1)
	for i := range items {
		items[i] = BatchItemInput{Recipient: domain.Recipient("+9055512345" + string(rune('0'+(i%10))))}
	}
	_, err := svc.CreateBatch(context.Background(), CreateBatchInput{
		IdempotencyKey: "k",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Content:        mkContent(t, map[string]string{"text": "hi"}),
		Items:          items,
	})
	if err == nil || !errors.Is(err, domain.ErrBatchTooLarge) {
		t.Fatalf("want ErrBatchTooLarge, got %v", err)
	}
}

func TestCancel_HappyPath(t *testing.T) {
	svc, repo, pub := newServiceForTest(t)
	created, _ := svc.Create(context.Background(), CreateNotificationInput{
		IdempotencyKey: "k1",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityHigh,
		Content:        mkContent(t, map[string]string{"text": "x"}),
	})
	_, err := svc.Cancel(context.Background(), created.UserNotification.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if repo.rows[created.UserNotification.ID].Status != domain.StatusCanceled {
		t.Fatalf("status: want canceled, got %s", repo.rows[created.UserNotification.ID].Status)
	}
	if len(pub.cancel) != 1 {
		t.Fatalf("publisher cancel calls: want 1, got %d", len(pub.cancel))
	}
}

func TestCreate_ScheduledRoutesToPublishAt(t *testing.T) {
	svc, _, pub := newServiceForTest(t)

	// Relative to "now" so this test never expires (was a hard-coded date that
	// eventually fell into the past and made ScheduledAt validation reject it).
	future := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	n, err := svc.Create(context.Background(), CreateNotificationInput{
		IdempotencyKey: "sched",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Content:        mkContent(t, map[string]string{"text": "later"}),
		ScheduledAt:    &future,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	when, ok := pub.enqueued[n.UserNotification.ID]
	if !ok {
		t.Fatalf("expected an enqueue call for scheduled notification")
	}
	if when.IsZero() {
		t.Fatalf("scheduled_at notification must route to PublishAt, got immediate Publish")
	}
	if !when.Equal(future) {
		t.Fatalf("enqueue time: want %v, got %v", future, when)
	}
}

func TestCreate_RejectsPastScheduledAt(t *testing.T) {
	svc, _, _ := newServiceForTest(t)
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := svc.Create(context.Background(), CreateNotificationInput{
		IdempotencyKey: "past",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityNormal,
		Content:        mkContent(t, map[string]string{"text": "late"}),
		ScheduledAt:    &past,
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput for past scheduled_at, got %v", err)
	}
}

func TestCancel_RejectsTerminal(t *testing.T) {
	svc, repo, _ := newServiceForTest(t)
	created, _ := svc.Create(context.Background(), CreateNotificationInput{
		IdempotencyKey: "k2",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityHigh,
		Content:        mkContent(t, map[string]string{"text": "x"}),
	})
	// force terminal
	row := repo.rows[created.UserNotification.ID]
	row.Status = domain.StatusSent
	repo.rows[created.UserNotification.ID] = row

	_, err := svc.Cancel(context.Background(), created.UserNotification.ID)
	if err == nil || !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}
