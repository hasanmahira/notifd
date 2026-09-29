package websocket

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// client is the per-connection server-side state. Owned by one goroutine at a
// time: write pump reads `send` and writes to the socket. The read pump only
// drains ping/pong and detects close.
//
// Lifecycle: newClient → add to hub → read+write pumps → one pump errors →
// markDone closes `done`. dispatch checks `done` before sending. We never
// `close(send)` because that races with concurrent dispatch writers; instead
// the channel is GC'd when the client is unreferenced.
type client struct {
	conn    *websocket.Conn
	batchID uuid.UUID
	send    chan []byte
	done    chan struct{}
	remote  string
	once    sync.Once
}

const (
	sendBuffer    = 32
	pingInterval  = 30 * time.Second
	writeDeadline = 5 * time.Second
	readDeadline  = 65 * time.Second // > 2x pingInterval so we notice missed pongs
)

func newClient(conn *websocket.Conn, batchID uuid.UUID, remote string) *client {
	return &client{
		conn:    conn,
		batchID: batchID,
		send:    make(chan []byte, sendBuffer),
		done:    make(chan struct{}),
		remote:  remote,
	}
}

// markDone signals every dispatch caller to skip this client. Safe to call
// multiple times.
func (c *client) markDone() {
	c.once.Do(func() { close(c.done) })
}

// writePump is the only goroutine that writes to the socket. Receives payloads
// on the send channel and keeps the connection alive with pings.
func (c *client) writePump(ctx context.Context) error {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case payload, ok := <-c.send:
			if !ok {
				return nil
			}
			wctx, cancel := context.WithTimeout(ctx, writeDeadline)
			err := c.conn.Write(wctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return err
			}
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, writeDeadline)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

// readPump drains incoming frames (we don't accept client messages beyond
// close/ping/pong). Returns when the peer disconnects or deadline expires.
func (c *client) readPump(ctx context.Context) error {
	for {
		rctx, cancel := context.WithTimeout(ctx, readDeadline)
		_, _, err := c.conn.Read(rctx)
		cancel()
		if err != nil {
			return err
		}
	}
}
