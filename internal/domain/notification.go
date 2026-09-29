package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Content is the opaque JSON payload to deliver. Shape is channel-specific and
// validated at the service layer, not at the DB.
type Content json.RawMessage

// Message is a single authored notification payload. In the fan-out schema one
// Message can back many per-recipient user_notifications rows.
type Message struct {
	ID          uuid.UUID
	BatchID     *uuid.UUID
	Channel     Channel
	Priority    Priority
	Content     Content
	TemplateID  *uuid.UUID
	ScheduledAt *time.Time
	CreatedAt   time.Time
}

// UserNotification is a single per-recipient delivery row with its own lifecycle.
type UserNotification struct {
	ID                uuid.UUID
	MessageID         uuid.UUID
	Recipient         Recipient
	IdempotencyKey    string
	Status            Status
	Attempts          int
	LastError         *string
	ProviderMessageID *string
	SentAt            *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Notification is the API-facing projection (Message + UserNotification joined).
type Notification struct {
	Message          Message
	UserNotification UserNotification
}

// BatchSummary is the aggregate view of a batch: total and per-status counts.
type BatchSummary struct {
	BatchID    uuid.UUID
	Total      int
	ByStatus   map[Status]int
	CreatedAt  time.Time
	Channels   []Channel
	Priorities []Priority
}
