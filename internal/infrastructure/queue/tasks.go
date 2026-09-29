package queue

import (
	"encoding/json"

	"github.com/google/uuid"
)

// Task type identifiers used for registering handlers.
const (
	TypeSendNotification = "notification:send"
	// Scheduled tasks use ProcessAt on the same type; there is no separate type.
)

// SendPayload is what the publisher marshals onto an Asynq task.
// Keeping this tiny keeps the queue payloads cheap and lets us enrich from the
// repository on worker pickup (source of truth lives in Postgres).
type SendPayload struct {
	NotificationID uuid.UUID  `json:"id"`
	BatchID        *uuid.UUID `json:"batch_id,omitempty"`
	Channel        string     `json:"channel"`
	RequestID      string     `json:"request_id,omitempty"`
}

func marshal(p SendPayload) ([]byte, error) { return json.Marshal(p) }

func unmarshal(b []byte) (SendPayload, error) {
	var p SendPayload
	err := json.Unmarshal(b, &p)
	return p, err
}
