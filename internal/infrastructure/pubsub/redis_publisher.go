package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/usecase"
	"github.com/redis/go-redis/v9"
)

// StatusChannel is the Redis Pub/Sub channel worker->api uses for live
// notification-status updates.
const StatusChannel = "notifications:updates"

// Envelope is the wire format on the Pub/Sub channel. We keep it flat and
// self-describing so consumers don't need schema coordination.
type Envelope struct {
	BatchID        *string   `json:"batch_id,omitempty"`
	NotificationID uuid.UUID `json:"notification_id"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

// Publisher implements usecase.StatusPublisher by emitting to Redis Pub/Sub.
type Publisher struct {
	rd *redis.Client
}

func NewPublisher(rd *redis.Client) *Publisher { return &Publisher{rd: rd} }

func (p *Publisher) Publish(ctx context.Context, evt usecase.StatusEvent) error {
	var batch *string
	if evt.BatchID != nil {
		s := evt.BatchID.String()
		batch = &s
	}
	body, err := json.Marshal(Envelope{
		BatchID:        batch,
		NotificationID: evt.NotificationID,
		Status:         string(evt.Status),
		Error:          evt.Error,
		Timestamp:      evt.Timestamp,
	})
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if err := p.rd.Publish(ctx, StatusChannel, body).Err(); err != nil {
		return fmt.Errorf("redis publish: %w", err)
	}
	return nil
}

var _ usecase.StatusPublisher = (*Publisher)(nil)
