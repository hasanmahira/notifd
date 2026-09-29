package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/usecase"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo implements usecase.Repository on top of pgx.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// CreateMessageAndUserNotifications writes the parent message and all per-recipient
// rows in a single transaction. A unique-constraint violation on
// `user_notifications.idempotency_key` is translated into domain.ErrConflict.
func (r *Repo) CreateMessageAndUserNotifications(ctx context.Context, msg domain.Message, rows []domain.UserNotification) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		insert into notification_messages (id, batch_id, channel, priority, content, template_id, scheduled_at, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
	`, msg.ID, msg.BatchID, string(msg.Channel), string(msg.Priority),
		[]byte(msg.Content), msg.TemplateID, msg.ScheduledAt, msg.CreatedAt); err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	batch := &pgx.Batch{}
	for _, un := range rows {
		batch.Queue(`
			insert into user_notifications (id, message_id, recipient, idempotency_key, status, attempts, created_at, updated_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
		`, un.ID, un.MessageID, string(un.Recipient), un.IdempotencyKey,
			string(un.Status), un.Attempts, un.CreatedAt, un.UpdatedAt)
	}
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < len(rows); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			var pgErr *pgconn.PgError
			// Narrow: only treat the idempotency-key unique index as a replay.
			// Other 23505s (e.g. PK collisions from a UUID generator bug) should
			// surface as real errors.
			if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
				pgErr.ConstraintName == "user_notifications_idempotency_key_key" {
				return fmt.Errorf("%w: %s", domain.ErrConflict, pgErr.Message)
			}
			return fmt.Errorf("insert user_notification[%d]: %w", i, err)
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const selectJoined = `
	select
		un.id, un.message_id, un.recipient, un.idempotency_key, un.status,
		un.attempts, un.last_error, un.provider_message_id, un.sent_at,
		un.created_at, un.updated_at,
		m.id, m.batch_id, m.channel, m.priority, m.content,
		m.template_id, m.scheduled_at, m.created_at
	from user_notifications un
	join notification_messages m on m.id = un.message_id
`

func (r *Repo) GetByID(ctx context.Context, id uuid.UUID) (domain.Notification, error) {
	row := r.pool.QueryRow(ctx, selectJoined+` where un.id = $1`, id)
	return scanJoined(row)
}

func (r *Repo) GetByIdempotencyKey(ctx context.Context, key string) (domain.Notification, error) {
	row := r.pool.QueryRow(ctx, selectJoined+` where un.idempotency_key = $1`, key)
	return scanJoined(row)
}

// UpdateStatus applies a partial update. Terminal-to-anything transitions are
// refused at the SQL level via the WHERE clause so concurrent writes can't
// walk a notification out of a terminal state.
//
// If the guard refuses the write, returns domain.ErrTerminal so callers can
// distinguish "I wrote the new status" from "a concurrent cancel won the race."
// This is critical for the send pipeline: if we race with Cancel, Sender needs
// to know not to call the provider.
func (r *Repo) UpdateStatus(ctx context.Context, id uuid.UUID, upd usecase.StatusUpdate) error {
	tag, err := r.pool.Exec(ctx, `
		update user_notifications set
			status              = $2,
			provider_message_id = coalesce($3, provider_message_id),
			sent_at             = coalesce($4, sent_at),
			last_error          = coalesce($5, last_error),
			updated_at          = now()
		where id = $1
		  and status not in ('sent','failed','canceled','dead_letter')
	`, id, string(upd.Status), upd.ProviderMessageID, upd.SentAt, upd.LastError)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		n, gerr := r.GetByID(ctx, id)
		if errors.Is(gerr, domain.ErrNotFound) {
			return gerr
		}
		if gerr != nil {
			return gerr
		}
		if n.UserNotification.Status.Terminal() {
			return fmt.Errorf("%w: id=%s current=%s", domain.ErrTerminal, id, n.UserNotification.Status)
		}
	}
	return nil
}

func (r *Repo) IncrementAttempts(ctx context.Context, id uuid.UUID, lastError string) error {
	_, err := r.pool.Exec(ctx, `
		update user_notifications
		set attempts = attempts + 1, last_error = $2, updated_at = now()
		where id = $1
	`, id, lastError)
	return err
}

// List uses keyset pagination over (created_at desc, id desc). The cursor is an
// opaque base64(JSON{created_at_unix_nanos, id}). We encode as int64 nanos
// (not RFC3339) because Go's time parser loses sub-microsecond precision on
// round-trip, which caused occasional row-skip / row-duplication at page
// boundaries under concurrent inserts.
type cursorPayload struct {
	CreatedAtNanos int64     `json:"c"`
	ID             uuid.UUID `json:"i"`
}

func (r *Repo) List(ctx context.Context, q usecase.ListQuery) ([]domain.Notification, string, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}

	var args []any
	where := []string{"1=1"}

	if q.Status != nil {
		args = append(args, string(*q.Status))
		where = append(where, fmt.Sprintf("un.status = $%d", len(args)))
	}
	if q.Channel != nil {
		args = append(args, string(*q.Channel))
		where = append(where, fmt.Sprintf("m.channel = $%d", len(args)))
	}
	if q.From != nil {
		args = append(args, *q.From)
		where = append(where, fmt.Sprintf("un.created_at >= $%d", len(args)))
	}
	if q.To != nil {
		args = append(args, *q.To)
		where = append(where, fmt.Sprintf("un.created_at <= $%d", len(args)))
	}

	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: bad cursor", domain.ErrInvalidInput)
		}
		var cp cursorPayload
		if err := json.Unmarshal(raw, &cp); err != nil {
			return nil, "", fmt.Errorf("%w: bad cursor payload", domain.ErrInvalidInput)
		}
		args = append(args, time.Unix(0, cp.CreatedAtNanos).UTC(), cp.ID)
		where = append(where,
			fmt.Sprintf("(un.created_at, un.id) < ($%d, $%d)", len(args)-1, len(args)))
	}

	args = append(args, limit+1)
	q1 := fmt.Sprintf(`
		%s
		where %s
		order by un.created_at desc, un.id desc
		limit $%d
	`, selectJoined, strings.Join(where, " and "), len(args))

	rows, err := r.pool.Query(ctx, q1, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := make([]domain.Notification, 0, limit)
	for rows.Next() {
		n, err := scanJoinedRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(out) > limit {
		last := out[limit-1]
		out = out[:limit]
		cp := cursorPayload{
			CreatedAtNanos: last.UserNotification.CreatedAt.UTC().UnixNano(),
			ID:             last.UserNotification.ID,
		}
		raw, _ := json.Marshal(cp)
		nextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nextCursor, nil
}

func (r *Repo) GetBatch(ctx context.Context, batchID uuid.UUID) (domain.BatchSummary, error) {
	var (
		summary = domain.BatchSummary{BatchID: batchID, ByStatus: map[domain.Status]int{}}
		created time.Time
		total   int
	)
	row := r.pool.QueryRow(ctx, `
		select m.created_at, count(un.id)
		from notification_messages m
		join user_notifications un on un.message_id = m.id
		where m.batch_id = $1
		group by m.created_at
		limit 1
	`, batchID)
	if err := row.Scan(&created, &total); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BatchSummary{}, fmt.Errorf("%w: batch %s", domain.ErrNotFound, batchID)
		}
		return domain.BatchSummary{}, err
	}
	summary.CreatedAt = created
	summary.Total = total

	rows, err := r.pool.Query(ctx, `
		select un.status, count(*)
		from notification_messages m
		join user_notifications un on un.message_id = m.id
		where m.batch_id = $1
		group by un.status
	`, batchID)
	if err != nil {
		return domain.BatchSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return domain.BatchSummary{}, err
		}
		summary.ByStatus[domain.Status(s)] = n
	}
	return summary, rows.Err()
}

// ---------- scan helpers ----------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJoined(r rowScanner) (domain.Notification, error) {
	return scanJoinedRow(r)
}

func scanJoinedRow(r rowScanner) (domain.Notification, error) {
	var (
		un                domain.UserNotification
		msg               domain.Message
		recipient         string
		status            string
		channel           string
		priority          string
		content           []byte
		lastError         *string
		providerMessageID *string
		sentAt            *time.Time
		batchID           *uuid.UUID
		templateID        *uuid.UUID
		scheduledAt       *time.Time
	)
	err := r.Scan(
		&un.ID, &un.MessageID, &recipient, &un.IdempotencyKey, &status,
		&un.Attempts, &lastError, &providerMessageID, &sentAt,
		&un.CreatedAt, &un.UpdatedAt,
		&msg.ID, &batchID, &channel, &priority, &content,
		&templateID, &scheduledAt, &msg.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Notification{}, fmt.Errorf("%w", domain.ErrNotFound)
		}
		return domain.Notification{}, err
	}
	un.Recipient = domain.Recipient(recipient)
	un.Status = domain.Status(status)
	un.LastError = lastError
	un.ProviderMessageID = providerMessageID
	un.SentAt = sentAt
	msg.BatchID = batchID
	msg.Channel = domain.Channel(channel)
	msg.Priority = domain.Priority(priority)
	msg.Content = domain.Content(content)
	msg.TemplateID = templateID
	msg.ScheduledAt = scheduledAt
	return domain.Notification{Message: msg, UserNotification: un}, nil
}
