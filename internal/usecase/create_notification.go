package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
)

// CreateNotificationInput is the per-item payload accepted by both the single
// and batch endpoints. Validation is performed in Service.Create before any DB
// or queue interaction.
//
// Either `Content` OR (`TemplateID` + `Variables`) must be provided, not both.
// When a template is referenced the Service renders it against Variables and
// writes the rendered content to the DB; the worker has no template awareness.
type CreateNotificationInput struct {
	IdempotencyKey string
	Recipient      domain.Recipient
	Channel        domain.Channel
	Priority       domain.Priority
	Content        domain.Content
	TemplateID     *uuid.UUID
	Variables      map[string]any
	ScheduledAt    *time.Time
}

// Validate performs per-item validation. Caller should treat the first error
// as authoritative and bubble it up as a 400.
//
// Content / template are XOR: exactly one must be set.
func (in CreateNotificationInput) Validate() error {
	if in.IdempotencyKey == "" {
		return fmt.Errorf("%w: idempotency_key required", domain.ErrInvalidInput)
	}
	if !in.Channel.Valid() {
		return fmt.Errorf("%w: channel %q", domain.ErrInvalidInput, in.Channel)
	}
	if !in.Priority.Valid() {
		return fmt.Errorf("%w: priority %q", domain.ErrInvalidInput, in.Priority)
	}
	if err := in.Recipient.Validate(in.Channel); err != nil {
		return err
	}

	hasContent := len(in.Content) > 0
	hasTemplate := in.TemplateID != nil
	if hasContent == hasTemplate {
		return fmt.Errorf("%w: exactly one of content or template_id is required", domain.ErrInvalidInput)
	}

	// Content is validated up front. Template-rendered content is validated
	// after Service.Create renders it.
	if hasContent {
		if err := domain.ValidateContent(in.Channel, in.Content); err != nil {
			return err
		}
	}
	if in.ScheduledAt != nil && in.ScheduledAt.Before(time.Now().Add(-1*time.Minute)) {
		return fmt.Errorf("%w: scheduled_at is in the past", domain.ErrInvalidInput)
	}
	return nil
}

// Create persists a single notification (one message + one user_notification)
// and enqueues it for immediate or scheduled delivery. If the input references
// a template, we render it against Variables and store the rendered content —
// the worker has no template awareness, and idempotency hashing sees the
// rendered body so replays with the same (template_id, variables) stay stable.
func (s *Service) Create(ctx context.Context, in CreateNotificationInput) (domain.Notification, error) {
	if err := in.Validate(); err != nil {
		return domain.Notification{}, err
	}

	content := in.Content
	var templateID *uuid.UUID
	if in.TemplateID != nil {
		if s.templates == nil {
			return domain.Notification{}, fmt.Errorf("%w: template support is not wired", domain.ErrInvalidInput)
		}
		rendered, err := s.renderTemplate(ctx, *in.TemplateID, in.Channel, in.Variables)
		if err != nil {
			return domain.Notification{}, err
		}
		content = rendered
		templateID = in.TemplateID
	}

	now := s.now()
	msg := domain.Message{
		ID:          domain.NewID(),
		Channel:     in.Channel,
		Priority:    in.Priority,
		Content:     content,
		TemplateID:  templateID,
		ScheduledAt: in.ScheduledAt,
		CreatedAt:   now,
	}
	un := domain.UserNotification{
		ID:             domain.NewID(),
		MessageID:      msg.ID,
		Recipient:      in.Recipient,
		IdempotencyKey: in.IdempotencyKey,
		Status:         domain.StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.repo.CreateMessageAndUserNotifications(ctx, msg, []domain.UserNotification{un}); err != nil {
		// Treat unique-constraint conflict as "idempotency reuse — return the existing row."
		if errors.Is(err, domain.ErrConflict) {
			existing, lookupErr := s.repo.GetByIdempotencyKey(ctx, in.IdempotencyKey)
			if lookupErr == nil {
				return existing, nil
			}
			return domain.Notification{}, err
		}
		return domain.Notification{}, err
	}

	n := domain.Notification{Message: msg, UserNotification: un}
	if err := s.enqueue(ctx, n); err != nil {
		// If enqueue fails after a successful DB write we still return success —
		// a cron-like sweeper would re-enqueue from DB in a follow-up iteration.
		s.log.Warn("enqueue failed post-create", "err", err)
	}
	s.metrics.IncCreated(string(msg.Channel), string(msg.Priority))
	return n, nil
}

func (s *Service) enqueue(ctx context.Context, n domain.Notification) error {
	if n.Message.ScheduledAt != nil && n.Message.ScheduledAt.After(s.now()) {
		return s.pub.PublishAt(ctx, n, *n.Message.ScheduledAt)
	}
	return s.pub.Publish(ctx, n)
}

// renderTemplate loads a template, asserts channel match, renders it against
// vars, then builds the channel-specific content JSON that the worker will
// forward to the provider.
func (s *Service) renderTemplate(ctx context.Context, id uuid.UUID, channel domain.Channel, vars map[string]any) (domain.Content, error) {
	tmpl, err := s.templates.GetTemplate(ctx, id)
	if err != nil {
		return nil, err
	}
	if tmpl.Channel != channel {
		return nil, fmt.Errorf("%w: template channel (%s) != request channel (%s)",
			domain.ErrInvalidInput, tmpl.Channel, channel)
	}
	subject, body, err := tmpl.Render(vars)
	if err != nil {
		return nil, err
	}

	var payload any
	switch channel {
	case domain.ChannelSMS:
		payload = map[string]string{"text": body}
	case domain.ChannelEmail:
		payload = map[string]string{"subject": subject, "body": body}
	case domain.ChannelPush:
		// For push the template body is the notification body; title is pulled
		// from vars if present so a single template can serve multiple titles.
		title, _ := vars["title"].(string)
		if title == "" {
			title = tmpl.Name
		}
		payload = map[string]any{"title": title, "body": body}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal rendered payload: %w", err)
	}
	// Re-validate rendered content against channel rules (size limits etc).
	if err := domain.ValidateContent(channel, domain.Content(raw)); err != nil {
		return nil, err
	}
	return domain.Content(raw), nil
}
