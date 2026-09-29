package usecase

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

type fakeTemplateRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]domain.Template
}

func newFakeTemplateRepo() *fakeTemplateRepo {
	return &fakeTemplateRepo{byID: map[uuid.UUID]domain.Template{}}
}

func (r *fakeTemplateRepo) CreateTemplate(_ context.Context, t domain.Template) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.byID {
		if existing.Name == t.Name && existing.Channel == t.Channel && existing.Locale == t.Locale {
			return domain.ErrConflict
		}
	}
	r.byID[t.ID] = t
	return nil
}

func (r *fakeTemplateRepo) GetTemplate(_ context.Context, id uuid.UUID) (domain.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return domain.Template{}, domain.ErrNotFound
	}
	return t, nil
}

func (r *fakeTemplateRepo) UpdateTemplate(_ context.Context, id uuid.UUID, subject *string, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	t.SubjectTmpl = subject
	t.BodyTmpl = body
	t.UpdatedAt = time.Now()
	r.byID[id] = t
	return nil
}

func (r *fakeTemplateRepo) DeleteTemplate(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeTemplateRepo) ListTemplates(_ context.Context) ([]domain.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Template, 0, len(r.byID))
	for _, t := range r.byID {
		out = append(out, t)
	}
	return out, nil
}

func newTemplateServiceForTest(t *testing.T) (*TemplateService, *fakeTemplateRepo) {
	t.Helper()
	repo := newFakeTemplateRepo()
	svc := NewTemplateService(repo, func() time.Time {
		return time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	})
	return svc, repo
}

func TestTemplateCreate_SMS(t *testing.T) {
	svc, _ := newTemplateServiceForTest(t)
	got, err := svc.Create(context.Background(), CreateTemplateInput{
		Name:     "order-shipped",
		Channel:  domain.ChannelSMS,
		BodyTmpl: "Hello {{.name}}, order {{.orderID}} shipped.",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Name != "order-shipped" || got.Channel != domain.ChannelSMS || got.Locale != "en-US" {
		t.Fatalf("unexpected template: %+v", got)
	}
}

func TestTemplateCreate_EmailRequiresSubject(t *testing.T) {
	svc, _ := newTemplateServiceForTest(t)
	_, err := svc.Create(context.Background(), CreateTemplateInput{
		Name:     "welcome",
		Channel:  domain.ChannelEmail,
		BodyTmpl: "Body",
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput for email without subject, got %v", err)
	}
}

func TestTemplateCreate_RejectsSubjectOnSMS(t *testing.T) {
	svc, _ := newTemplateServiceForTest(t)
	sub := "Subject"
	_, err := svc.Create(context.Background(), CreateTemplateInput{
		Name:        "no-subject-sms",
		Channel:     domain.ChannelSMS,
		SubjectTmpl: &sub,
		BodyTmpl:    "Body",
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput when SMS has subject, got %v", err)
	}
}

func TestTemplate_Render_HappyPath(t *testing.T) {
	sub := "Order {{.orderID}} update"
	tmpl := domain.Template{
		Name:        "order",
		Channel:     domain.ChannelEmail,
		SubjectTmpl: &sub,
		BodyTmpl:    "Hello {{.name}}, status: {{.status}}",
	}
	s, b, err := tmpl.Render(map[string]any{"name": "Mahir", "orderID": "A-1", "status": "shipped"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if s != "Order A-1 update" {
		t.Fatalf("subject: want 'Order A-1 update', got %q", s)
	}
	if b != "Hello Mahir, status: shipped" {
		t.Fatalf("body: want 'Hello Mahir, status: shipped', got %q", b)
	}
}

func TestTemplate_Render_MissingVariable(t *testing.T) {
	tmpl := domain.Template{
		Name:     "missing",
		Channel:  domain.ChannelSMS,
		BodyTmpl: "Hello {{.name}}",
	}
	_, _, err := tmpl.Render(map[string]any{})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput on missing var, got %v", err)
	}
}

// --- render-from-template integration on Service.Create ---

func newServiceWithTemplates(t *testing.T, tmplRepo TemplateRepository) (*Service, *fakeRepo, *fakePublisher) {
	t.Helper()
	repo := newFakeRepo()
	pub := newFakePublisher()
	svc := NewService(ServiceDeps{
		Repo:      repo,
		Pub:       pub,
		Templates: tmplRepo,
		Log:       slog.New(slog.NewTextHandler(newTestWriter(t), nil)),
		Now:       func() time.Time { return time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC) },
	})
	return svc, repo, pub
}

func TestCreateFromTemplate_SMS(t *testing.T) {
	tmplRepo := newFakeTemplateRepo()
	svc, repo, pub := newServiceWithTemplates(t, tmplRepo)

	tid := domain.NewID()
	tmplRepo.byID[tid] = domain.Template{
		ID: tid, Name: "greeting", Channel: domain.ChannelSMS,
		BodyTmpl: "Hi {{.name}}, code is {{.code}}",
	}

	n, err := svc.Create(context.Background(), CreateNotificationInput{
		IdempotencyKey: "k",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityHigh,
		TemplateID:     &tid,
		Variables:      map[string]any{"name": "Mahir", "code": "998877"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if n.Message.TemplateID == nil || *n.Message.TemplateID != tid {
		t.Fatalf("template_id should be recorded on the message; got %+v", n.Message.TemplateID)
	}
	got := repo.msgs[n.Message.ID].Content
	want := `{"text":"Hi Mahir, code is 998877"}`
	if string(got) != want {
		t.Fatalf("rendered content: want %s, got %s", want, string(got))
	}
	if len(pub.enq) != 1 {
		t.Fatalf("should enqueue 1 task")
	}
}

func TestCreateFromTemplate_ChannelMismatch(t *testing.T) {
	tmplRepo := newFakeTemplateRepo()
	svc, _, _ := newServiceWithTemplates(t, tmplRepo)

	tid := domain.NewID()
	tmplRepo.byID[tid] = domain.Template{
		ID: tid, Name: "mismatch", Channel: domain.ChannelEmail,
		SubjectTmpl: strPtr("s"), BodyTmpl: "b",
	}
	_, err := svc.Create(context.Background(), CreateNotificationInput{
		IdempotencyKey: "k2",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS, // mismatch
		Priority:       domain.PriorityHigh,
		TemplateID:     &tid,
		Variables:      map[string]any{},
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput on channel mismatch, got %v", err)
	}
}

func TestCreate_RejectsBothContentAndTemplate(t *testing.T) {
	tmplRepo := newFakeTemplateRepo()
	svc, _, _ := newServiceWithTemplates(t, tmplRepo)

	tid := domain.NewID()
	tmplRepo.byID[tid] = domain.Template{
		ID: tid, Name: "either-or", Channel: domain.ChannelSMS, BodyTmpl: "b",
	}
	_, err := svc.Create(context.Background(), CreateNotificationInput{
		IdempotencyKey: "k3",
		Recipient:      "+905551234567",
		Channel:        domain.ChannelSMS,
		Priority:       domain.PriorityHigh,
		Content:        mkContent(t, map[string]string{"text": "hi"}),
		TemplateID:     &tid,
		Variables:      map[string]any{},
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput when both content and template supplied, got %v", err)
	}
}

func strPtr(s string) *string { return &s }

// --- polish: list / update / delete / preview / helpers ---

func TestTemplate_List(t *testing.T) {
	svc, _ := newTemplateServiceForTest(t)
	for _, n := range []string{"a", "b", "c"} {
		if _, err := svc.Create(context.Background(), CreateTemplateInput{
			Name: n, Channel: domain.ChannelSMS, BodyTmpl: "x",
		}); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
	out, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 templates, got %d", len(out))
	}
}

func TestTemplate_Update_ChannelRules(t *testing.T) {
	svc, _ := newTemplateServiceForTest(t)
	created, err := svc.Create(context.Background(), CreateTemplateInput{
		Name: "u", Channel: domain.ChannelSMS, BodyTmpl: "v1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// happy: update body only
	updated, err := svc.Update(context.Background(), created.ID, UpdateTemplateInput{BodyTmpl: "v2"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.BodyTmpl != "v2" {
		t.Fatalf("body_tmpl: want v2, got %s", updated.BodyTmpl)
	}
	// reject: setting subject on an SMS template
	sub := "nope"
	_, err = svc.Update(context.Background(), created.ID, UpdateTemplateInput{
		SubjectTmpl: &sub, BodyTmpl: "v3",
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput for sms+subject, got %v", err)
	}
	// reject: empty body
	_, err = svc.Update(context.Background(), created.ID, UpdateTemplateInput{BodyTmpl: ""})
	if err == nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput for empty body, got %v", err)
	}
}

func TestTemplate_Delete(t *testing.T) {
	svc, _ := newTemplateServiceForTest(t)
	created, _ := svc.Create(context.Background(), CreateTemplateInput{
		Name: "d", Channel: domain.ChannelSMS, BodyTmpl: "body",
	})
	if err := svc.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := svc.Get(context.Background(), created.ID)
	if err == nil || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	// double-delete is 404
	err = svc.Delete(context.Background(), created.ID)
	if err == nil || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete missing: want ErrNotFound, got %v", err)
	}
}

func TestTemplate_Preview_DoesNotPersist(t *testing.T) {
	svc, repo := newTemplateServiceForTest(t)
	created, _ := svc.Create(context.Background(), CreateTemplateInput{
		Name: "prev", Channel: domain.ChannelSMS, BodyTmpl: "Hello {{.name}}",
	})

	res, err := svc.Preview(context.Background(), created.ID, map[string]any{"name": "Mahir"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.Body != "Hello Mahir" {
		t.Fatalf("body: want 'Hello Mahir', got %q", res.Body)
	}
	// repo state unchanged — only the 1 template we created
	if len(repo.byID) != 1 {
		t.Fatalf("preview must not touch the repo; got %d templates", len(repo.byID))
	}
}

func TestTemplate_Helpers(t *testing.T) {
	cases := []struct {
		name string
		body string
		vars map[string]any
		want string
	}{
		{"upper", "hi {{upper .name}}", map[string]any{"name": "mahir"}, "hi MAHIR"},
		{"lower", "{{lower .shout}} world", map[string]any{"shout": "HELLO"}, "hello world"},
		{"title", "{{title .s}}", map[string]any{"s": "hello WORLD"}, "Hello World"},
		{"truncate", "{{truncate 5 .s}}", map[string]any{"s": "abcdefghij"}, "abcde…"},
		{"default", "{{default \"guest\" .name}}", map[string]any{"name": ""}, "guest"},
		{"formatDate", "{{formatDate .ts \"2006-01-02\"}}", map[string]any{"ts": "2026-04-23T12:00:00Z"}, "2026-04-23"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmpl := domain.Template{Name: c.name, Channel: domain.ChannelSMS, BodyTmpl: c.body}
			_, got, err := tmpl.Render(c.vars)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
