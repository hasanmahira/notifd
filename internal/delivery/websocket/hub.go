package websocket

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hasanmahira/notifd/internal/infrastructure/pubsub"
	"github.com/redis/go-redis/v9"
)

// Hub maintains per-batch subscriber lists and fans events in from the
// single Redis Pub/Sub subscription.
//
// Design:
//   - One goroutine per API pod subscribes to StatusChannel (not one per client).
//   - Incoming messages are demuxed by batch_id and written to each client's
//     buffered send channel.
//   - Slow clients get dropped; we never block the subscribe goroutine.
type Hub struct {
	rd      *redis.Client
	log     *slog.Logger
	metrics *observ.Metrics

	mu   sync.RWMutex
	subs map[uuid.UUID]map[*client]struct{} // batchID -> clients
	all  map[*client]struct{}               // every client (for shutdown)
}

func NewHub(rd *redis.Client, log *slog.Logger, m *observ.Metrics) *Hub {
	return &Hub{
		rd:      rd,
		log:     log,
		metrics: m,
		subs:    make(map[uuid.UUID]map[*client]struct{}),
		all:     make(map[*client]struct{}),
	}
}

// Run blocks until ctx is canceled. Subscribes to Redis once and fans out.
func (h *Hub) Run(ctx context.Context) error {
	sub := h.rd.Subscribe(ctx, pubsub.StatusChannel)
	defer func() { _ = sub.Close() }()

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			h.dispatch(msg.Payload)
		}
	}
}

func (h *Hub) dispatch(raw string) {
	var env pubsub.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		h.log.Warn("ws hub: bad envelope", slog.Any("err", err))
		return
	}
	if env.BatchID == nil {
		return
	}
	batchID, err := uuid.Parse(*env.BatchID)
	if err != nil {
		return
	}

	// Hold the lock across the send so removeClient (which closes the channel
	// via context cancel — not close()) cannot race with us choosing to send.
	// The tradeoff is that a very slow client blocks this goroutine; we cap
	// that with the default branch (drop-and-disconnect).
	h.mu.RLock()
	clients := make([]*client, 0, len(h.subs[batchID]))
	for c := range h.subs[batchID] {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case <-c.done:
			// Client already being torn down; skip. Hub entry will be removed
			// by its own goroutine.
		case c.send <- []byte(raw):
			if h.metrics != nil {
				h.metrics.WebSocketMessages.Inc()
			}
		default:
			// client's send buffer is full — drop it to protect the hub loop.
			h.log.Warn("ws client slow; dropping", slog.String("remote", c.remote))
			h.removeClient(c)
			_ = c.conn.Close(websocket.StatusPolicyViolation, "slow consumer")
		}
	}
}

// add registers a client under its batch id. Call exactly once per connection.
func (h *Hub) add(batchID uuid.UUID, c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[batchID]; !ok {
		h.subs[batchID] = map[*client]struct{}{}
	}
	h.subs[batchID][c] = struct{}{}
	h.all[c] = struct{}{}
	if h.metrics != nil {
		h.metrics.WebSocketConns.Inc()
	}
}

// removeClient deregisters a client from every structure.
func (h *Hub) removeClient(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.all[c]; !ok {
		return
	}
	delete(h.all, c)
	if set, ok := h.subs[c.batchID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.subs, c.batchID)
		}
	}
	if h.metrics != nil {
		h.metrics.WebSocketConns.Dec()
	}
}

// Shutdown closes every active connection with a Going Away code.
func (h *Hub) Shutdown(ctx context.Context) {
	h.mu.Lock()
	conns := make([]*client, 0, len(h.all))
	for c := range h.all {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		_ = c.conn.Close(websocket.StatusGoingAway, "server shutting down")
	}
}
