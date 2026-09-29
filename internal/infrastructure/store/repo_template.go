package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CreateTemplate inserts a new template. A unique-violation on
// (name, channel, locale) translates to domain.ErrConflict so the HTTP layer
// can respond with 409.
func (r *Repo) CreateTemplate(ctx context.Context, t domain.Template) error {
	_, err := r.pool.Exec(ctx, `
		insert into notification_templates
		  (id, name, channel, locale, subject_tmpl, body_tmpl, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8)
	`, t.ID, t.Name, string(t.Channel), t.Locale, t.SubjectTmpl, t.BodyTmpl, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: template (name,channel,locale) already exists", domain.ErrConflict)
		}
		return fmt.Errorf("insert template: %w", err)
	}
	return nil
}

// GetTemplate fetches a template by id.
func (r *Repo) GetTemplate(ctx context.Context, id uuid.UUID) (domain.Template, error) {
	var (
		t           domain.Template
		channel     string
		subjectTmpl *string
	)
	row := r.pool.QueryRow(ctx, `
		select id, name, channel, locale, subject_tmpl, body_tmpl, created_at, updated_at
		from notification_templates
		where id = $1
	`, id)
	err := row.Scan(&t.ID, &t.Name, &channel, &t.Locale, &subjectTmpl, &t.BodyTmpl, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Template{}, fmt.Errorf("%w: template %s", domain.ErrNotFound, id)
		}
		return domain.Template{}, err
	}
	t.Channel = domain.Channel(channel)
	t.SubjectTmpl = subjectTmpl
	return t, nil
}

// UpdateTemplate replaces subject/body for an existing template.
func (r *Repo) UpdateTemplate(ctx context.Context, id uuid.UUID, subject *string, body string) error {
	tag, err := r.pool.Exec(ctx, `
		update notification_templates
		set subject_tmpl = $2, body_tmpl = $3, updated_at = $4
		where id = $1
	`, id, subject, body, time.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: template %s", domain.ErrNotFound, id)
	}
	return nil
}

// DeleteTemplate removes a template. Past notifications keep their
// template_id reference dangling — template_id is not an FK so this is safe;
// we simply preserve audit history ("this notification was rendered from
// template X, which has since been deleted").
func (r *Repo) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `delete from notification_templates where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: template %s", domain.ErrNotFound, id)
	}
	return nil
}

// ListTemplates returns all templates ordered by most recently updated first.
// Intentionally unpaginated — template counts are small (tens, not millions).
func (r *Repo) ListTemplates(ctx context.Context) ([]domain.Template, error) {
	rows, err := r.pool.Query(ctx, `
		select id, name, channel, locale, subject_tmpl, body_tmpl, created_at, updated_at
		from notification_templates
		order by updated_at desc
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Template
	for rows.Next() {
		var (
			t           domain.Template
			channel     string
			subjectTmpl *string
		)
		if err := rows.Scan(&t.ID, &t.Name, &channel, &t.Locale, &subjectTmpl, &t.BodyTmpl, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Channel = domain.Channel(channel)
		t.SubjectTmpl = subjectTmpl
		out = append(out, t)
	}
	return out, rows.Err()
}
