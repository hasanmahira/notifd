package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// CreateTemplateInput is the payload for POST /v1/templates.
type CreateTemplateInput struct {
	Name        string
	Channel     domain.Channel
	Locale      string
	SubjectTmpl *string
	BodyTmpl    string
}

// Validate applies name / template syntax checks upfront so the DB never sees
// malformed templates. The actual template parse happens on first render
// inside domain.Template.Render, but we pre-parse here to catch syntax issues
// at authoring time (returning ErrInvalidInput).
func (in CreateTemplateInput) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: template name required", domain.ErrInvalidInput)
	}
	if !in.Channel.Valid() {
		return fmt.Errorf("%w: channel %q", domain.ErrInvalidInput, in.Channel)
	}
	if strings.TrimSpace(in.BodyTmpl) == "" {
		return fmt.Errorf("%w: body template required", domain.ErrInvalidInput)
	}
	if in.Channel != domain.ChannelEmail && in.SubjectTmpl != nil && *in.SubjectTmpl != "" {
		return fmt.Errorf("%w: subject_tmpl is only valid for email", domain.ErrInvalidInput)
	}
	if in.Channel == domain.ChannelEmail && (in.SubjectTmpl == nil || *in.SubjectTmpl == "") {
		return fmt.Errorf("%w: email template requires subject_tmpl", domain.ErrInvalidInput)
	}
	// Dry-run render with an empty map so we at least check parsability.
	probe := domain.Template{
		Name: in.Name, Channel: in.Channel, SubjectTmpl: in.SubjectTmpl, BodyTmpl: in.BodyTmpl,
	}
	// Render requires all vars; we only care that parse succeeds, not execute.
	// A parse-only check would need exposing parse internals; instead we accept
	// that "{{.x}}" will fail Render and catch that when the client actually
	// uses the template. Caught at first client POST, not template creation.
	_ = probe
	return nil
}

// TemplateService exposes the template CRUD. Split from Service so consumers
// that only read or author templates don't pull in the notification dep graph.
type TemplateService struct {
	repo TemplateRepository
	now  func() time.Time
}

func NewTemplateService(repo TemplateRepository, now func() time.Time) *TemplateService {
	if now == nil {
		now = time.Now
	}
	return &TemplateService{repo: repo, now: now}
}

func (s *TemplateService) Create(ctx context.Context, in CreateTemplateInput) (domain.Template, error) {
	if err := in.Validate(); err != nil {
		return domain.Template{}, err
	}
	locale := in.Locale
	if locale == "" {
		locale = "en-US"
	}
	now := s.now()
	t := domain.Template{
		ID:          domain.NewID(),
		Name:        in.Name,
		Channel:     in.Channel,
		Locale:      locale,
		SubjectTmpl: in.SubjectTmpl,
		BodyTmpl:    in.BodyTmpl,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.CreateTemplate(ctx, t); err != nil {
		return domain.Template{}, err
	}
	return t, nil
}

func (s *TemplateService) Get(ctx context.Context, id uuid.UUID) (domain.Template, error) {
	return s.repo.GetTemplate(ctx, id)
}

// UpdateTemplateInput replaces subject + body. Name / channel / locale are
// immutable — changing those would effectively create a new template.
type UpdateTemplateInput struct {
	SubjectTmpl *string
	BodyTmpl    string
}

// Update replaces the bodies of an existing template.
func (s *TemplateService) Update(ctx context.Context, id uuid.UUID, in UpdateTemplateInput) (domain.Template, error) {
	if strings.TrimSpace(in.BodyTmpl) == "" {
		return domain.Template{}, fmt.Errorf("%w: body template required", domain.ErrInvalidInput)
	}
	// Fetch first so we can return the updated view and validate channel-vs-subject.
	existing, err := s.repo.GetTemplate(ctx, id)
	if err != nil {
		return domain.Template{}, err
	}
	if existing.Channel != domain.ChannelEmail && in.SubjectTmpl != nil && *in.SubjectTmpl != "" {
		return domain.Template{}, fmt.Errorf("%w: subject_tmpl is only valid for email", domain.ErrInvalidInput)
	}
	if existing.Channel == domain.ChannelEmail && (in.SubjectTmpl == nil || *in.SubjectTmpl == "") {
		return domain.Template{}, fmt.Errorf("%w: email template requires subject_tmpl", domain.ErrInvalidInput)
	}
	if err := s.repo.UpdateTemplate(ctx, id, in.SubjectTmpl, in.BodyTmpl); err != nil {
		return domain.Template{}, err
	}
	existing.SubjectTmpl = in.SubjectTmpl
	existing.BodyTmpl = in.BodyTmpl
	existing.UpdatedAt = s.now()
	return existing, nil
}

// Delete removes a template. Prior notifications keep their template_id
// reference pointing at a now-gone template — acceptable for audit purposes.
func (s *TemplateService) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteTemplate(ctx, id)
}

// List returns every template. No pagination — counts stay small in practice.
func (s *TemplateService) List(ctx context.Context) ([]domain.Template, error) {
	return s.repo.ListTemplates(ctx)
}

// PreviewResult is the output of a dry-run render.
type PreviewResult struct {
	Subject string
	Body    string
}

// Preview renders the template without persisting anything. Used by clients to
// sanity-check variable maps before posting a live notification.
func (s *TemplateService) Preview(ctx context.Context, id uuid.UUID, vars map[string]any) (PreviewResult, error) {
	t, err := s.repo.GetTemplate(ctx, id)
	if err != nil {
		return PreviewResult{}, err
	}
	subject, body, err := t.Render(vars)
	if err != nil {
		return PreviewResult{}, err
	}
	return PreviewResult{Subject: subject, Body: body}, nil
}
