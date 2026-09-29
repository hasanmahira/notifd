package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/infrastructure/auth"
	"github.com/hasanmahira/notifd/internal/usecase"
)

// --- tiny fakes local to the http layer ---

type memRepo struct {
	mu   sync.Mutex
	msgs map[uuid.UUID]domain.Message
	rows map[uuid.UUID]domain.UserNotification
	keys map[string]uuid.UUID
}

func newMemRepo() *memRepo {
	return &memRepo{
		msgs: map[uuid.UUID]domain.Message{},
		rows: map[uuid.UUID]domain.UserNotification{},
		keys: map[string]uuid.UUID{},
	}
}

func (r *memRepo) CreateMessageAndUserNotifications(_ context.Context, m domain.Message, rows []domain.UserNotification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range rows {
		if _, dup := r.keys[x.IdempotencyKey]; dup {
			return domain.ErrConflict
		}
	}
	r.msgs[m.ID] = m
	for _, x := range rows {
		r.rows[x.ID] = x
		r.keys[x.IdempotencyKey] = x.ID
	}
	return nil
}

func (r *memRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	x, ok := r.rows[id]
	if !ok {
		return domain.Notification{}, domain.ErrNotFound
	}
	return domain.Notification{Message: r.msgs[x.MessageID], UserNotification: x}, nil
}

func (r *memRepo) GetByIdempotencyKey(_ context.Context, k string) (domain.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.keys[k]
	if !ok {
		return domain.Notification{}, domain.ErrNotFound
	}
	x := r.rows[id]
	return domain.Notification{Message: r.msgs[x.MessageID], UserNotification: x}, nil
}

func (r *memRepo) List(_ context.Context, _ usecase.ListQuery) ([]domain.Notification, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Notification, 0, len(r.rows))
	for _, x := range r.rows {
		out = append(out, domain.Notification{Message: r.msgs[x.MessageID], UserNotification: x})
	}
	return out, "", nil
}

func (r *memRepo) GetBatch(_ context.Context, b uuid.UUID) (domain.BatchSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := domain.BatchSummary{BatchID: b, ByStatus: map[domain.Status]int{}}
	for _, m := range r.msgs {
		if m.BatchID != nil && *m.BatchID == b {
			s.CreatedAt = m.CreatedAt
			for _, x := range r.rows {
				if x.MessageID == m.ID {
					s.Total++
					s.ByStatus[x.Status]++
				}
			}
		}
	}
	if s.Total == 0 {
		return domain.BatchSummary{}, domain.ErrNotFound
	}
	return s, nil
}

func (r *memRepo) UpdateStatus(_ context.Context, id uuid.UUID, upd usecase.StatusUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	x, ok := r.rows[id]
	if !ok {
		return domain.ErrNotFound
	}
	if x.Status.Terminal() {
		return nil
	}
	x.Status = upd.Status
	r.rows[id] = x
	return nil
}

func (r *memRepo) IncrementAttempts(_ context.Context, id uuid.UUID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	x := r.rows[id]
	x.Attempts++
	r.rows[id] = x
	return nil
}

type memIdem struct {
	mu     sync.Mutex
	hashes map[string]string // key -> body hash
	resps  map[string][]byte
	codes  map[string]int
}

func newMemIdem() *memIdem {
	return &memIdem{hashes: map[string]string{}, resps: map[string][]byte{}, codes: map[string]int{}}
}

func (m *memIdem) Claim(_ context.Context, k, hash string, _ time.Duration) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.hashes[k]; ok {
		return false, existing, nil
	}
	m.hashes[k] = hash
	return true, hash, nil
}

func (m *memIdem) SaveResponse(_ context.Context, k string, code int, body []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resps[k] = append([]byte(nil), body...)
	m.codes[k] = code
	return nil
}

func (m *memIdem) LookupResponse(_ context.Context, k string) (int, []byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if body, ok := m.resps[k]; ok {
		return m.codes[k], body, false, nil
	}
	if _, ok := m.hashes[k]; ok {
		return 0, nil, true, nil
	}
	return 0, nil, false, domain.ErrNotFound
}

type noopPub struct{}

func (noopPub) Publish(context.Context, domain.Notification) error              { return nil }
func (noopPub) PublishAt(context.Context, domain.Notification, time.Time) error { return nil }
func (noopPub) Cancel(context.Context, domain.Notification) error               { return nil }

// --- test helpers ---

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	repo := newMemRepo()
	svc := usecase.NewService(usecase.ServiceDeps{Repo: repo, Pub: noopPub{}, Log: log})
	notif := NewNotificationHandler(svc)
	batch := NewBatchHandler(svc)
	verifier := auth.New("test-secret", "test")
	idem := newMemIdem()
	router := NewRouter(RouterDeps{
		Notif:       notif,
		Batch:       batch,
		Health:      &HealthChecker{},
		Verifier:    verifier,
		Idempotency: idem,
		Log:         log,
	})
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)
	token, err := verifier.Issue("tester", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return ts, token
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func postJSON(t *testing.T, ts *httptest.Server, path, token, idemKey string, body any) *http.Response {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestCreateAuthRequired(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := postJSON(t, ts, "/v1/notifications", "", uuid.NewString(), map[string]any{
		"recipient": "+905551234567", "channel": "sms", "priority": "high",
		"content": map[string]string{"text": "hi"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", resp.StatusCode)
	}
}

func TestCreateHappyPath(t *testing.T) {
	ts, tok := newTestServer(t)
	resp := postJSON(t, ts, "/v1/notifications", tok, uuid.NewString(), map[string]any{
		"recipient": "+905551234567", "channel": "sms", "priority": "high",
		"content": map[string]string{"text": "hi"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: want 201, got %d", resp.StatusCode)
	}
	var body notificationView
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID == "" || body.Status != "pending" || body.Channel != "sms" {
		t.Fatalf("unexpected response: %+v", body)
	}
}

func TestCreateIdempotentReplay(t *testing.T) {
	ts, tok := newTestServer(t)
	key := uuid.NewString()
	payload := map[string]any{
		"recipient": "+905551234567", "channel": "sms", "priority": "high",
		"content": map[string]string{"text": "hi"},
	}
	first := postJSON(t, ts, "/v1/notifications", tok, key, payload)
	defer first.Body.Close()
	var a notificationView
	_ = json.NewDecoder(first.Body).Decode(&a)

	second := postJSON(t, ts, "/v1/notifications", tok, key, payload)
	defer second.Body.Close()
	var b notificationView
	_ = json.NewDecoder(second.Body).Decode(&b)

	if a.ID == "" || a.ID != b.ID {
		t.Fatalf("idempotent replay must return same id; got %q vs %q", a.ID, b.ID)
	}
	if second.Header.Get("X-Idempotent-Replayed") != "true" {
		t.Fatalf("expected X-Idempotent-Replayed=true on replay, got %q", second.Header.Get("X-Idempotent-Replayed"))
	}
}

func TestIdempotencyKeyRequired(t *testing.T) {
	ts, tok := newTestServer(t)
	resp := postJSON(t, ts, "/v1/notifications", tok, "", map[string]any{
		"recipient": "+905551234567", "channel": "sms", "priority": "high",
		"content": map[string]string{"text": "hi"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", resp.StatusCode)
	}
}

func TestValidationRejects(t *testing.T) {
	ts, tok := newTestServer(t)
	// invalid channel
	resp := postJSON(t, ts, "/v1/notifications", tok, uuid.NewString(), map[string]any{
		"recipient": "+905551234567", "channel": "carrierpigeon", "priority": "high",
		"content": map[string]string{"text": "hi"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: want 400 for bad channel, got %d", resp.StatusCode)
	}
}

func TestBatchHappyPath(t *testing.T) {
	ts, tok := newTestServer(t)
	resp := postJSON(t, ts, "/v1/notifications/batch", tok, uuid.NewString(), map[string]any{
		"channel": "sms", "priority": "normal",
		"content": map[string]string{"text": "hi"},
		"items": []map[string]any{
			{"recipient": "+905551111111"},
			{"recipient": "+905552222222"},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: want 202, got %d", resp.StatusCode)
	}
	var body batchView
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Accepted != 2 || body.BatchID == "" {
		t.Fatalf("unexpected: %+v", body)
	}
}

// sanity: wrap/unwrap keeps the sentinel accessible through json
func TestIdempotencyDifferentBodyReturns409(t *testing.T) {
	ts, tok := newTestServer(t)
	key := uuid.NewString()

	first := postJSON(t, ts, "/v1/notifications", tok, key, map[string]any{
		"recipient": "+905551234567", "channel": "sms", "priority": "high",
		"content": map[string]string{"text": "original"},
	})
	first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first: want 201, got %d", first.StatusCode)
	}

	second := postJSON(t, ts, "/v1/notifications", tok, key, map[string]any{
		"recipient": "+905559999999", "channel": "sms", "priority": "high",
		"content": map[string]string{"text": "different body"},
	})
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("same key + different body: want 409, got %d", second.StatusCode)
	}
	var body ErrorBody
	_ = json.NewDecoder(second.Body).Decode(&body)
	if body.Error != "idempotency_key_reused_different_body" {
		t.Fatalf("error code: want idempotency_key_reused_different_body, got %q", body.Error)
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	ts, tok := newTestServer(t)
	resp := postJSON(t, ts, "/v1/notifications", tok, uuid.NewString(), map[string]any{
		"channel": "sms",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	var body ErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error == "" {
		t.Fatalf("expected error code in response body")
	}
	if !errors.Is(domain.ErrInvalidInput, domain.ErrInvalidInput) {
		t.Fatal("unreachable; only here to keep errors import live")
	}
}
