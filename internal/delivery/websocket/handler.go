package websocket

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/infrastructure/auth"
)

// Handler is the HTTP entry point for /v1/ws/batch/{batch_id}.
//
// Auth order of preference:
//  1. Sec-WebSocket-Protocol: bearer, <jwt>   (browsers can set this)
//  2. Authorization: Bearer <jwt>             (non-browser clients)
//  3. ?token=<jwt>                            (last resort; logged as
//     query-string so treat as
//     short-lived)
type Handler struct {
	hub             *Hub
	verifier        auth.Verifier
	log             *slog.Logger
	originAllowList []string
}

// Config exposes knobs for dev vs prod. OriginAllowList is ignored in dev
// (InsecureSkipVerify on) but enforced otherwise.
type Config struct {
	Env         string
	OriginHosts []string
}

func NewHandler(h *Hub, v auth.Verifier, log *slog.Logger, cfg Config) *Handler {
	return &Handler{hub: h, verifier: v, log: log, originAllowList: cfg.OriginHosts}
}

func (h *Handler) Subscribe(w http.ResponseWriter, r *http.Request) {
	token, fromSubprotocol := extractToken(r)
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	if _, err := h.verifier.Verify(token); err != nil {
		h.log.Debug("ws auth rejected", slog.Any("err", err))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	batchID, err := domain.ParseID(chi.URLParam(r, "batch_id"))
	if err != nil {
		http.Error(w, "invalid batch_id", http.StatusBadRequest)
		return
	}

	acceptOpts := &websocket.AcceptOptions{
		OriginPatterns: h.originAllowList,
	}
	if fromSubprotocol {
		// Echo the subprotocol back so the handshake is RFC-conformant.
		acceptOpts.Subprotocols = []string{"bearer"}
	}
	if len(h.originAllowList) == 0 {
		// Dev convenience: same-origin / localhost / wscat produce no Origin
		// header. Production config should always set OriginHosts.
		acceptOpts.InsecureSkipVerify = true
	}
	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		h.log.Warn("ws accept", slog.Any("err", err))
		return
	}

	c := newClient(conn, batchID, r.RemoteAddr)
	h.hub.add(batchID, c)
	defer func() {
		c.markDone()
		h.hub.removeClient(c)
		// No close(send): dispatch may still hold a stale reference. Channel
		// is GC'd when client is unreferenced.
	}()

	// Split context so writePump and readPump share the same lifetime.
	ctx := r.Context()

	// Run read/write pumps concurrently; return when either ends.
	errs := make(chan error, 2)
	go func() { errs <- c.writePump(ctx) }()
	go func() { errs <- c.readPump(ctx) }()
	first := <-errs

	_ = conn.Close(closeCodeFor(first), closeReasonFor(first))
	if !isExpectedClose(first) {
		h.log.Debug("ws closed", slog.Any("err", first))
	}
}

// extractToken tries all three auth carriers in order of preference. The
// second return reports whether the token came from the Sec-WebSocket-Protocol
// header so the caller can echo the subprotocol back in the handshake.
func extractToken(r *http.Request) (token string, fromSubprotocol bool) {
	// 1. Sec-WebSocket-Protocol: "bearer, <jwt>"
	if sp := r.Header.Get("Sec-WebSocket-Protocol"); sp != "" {
		parts := strings.Split(sp, ",")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "bearer" {
			return strings.TrimSpace(parts[1]), true
		}
	}
	// 2. Authorization: Bearer <jwt>
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):]), false
	}
	// 3. ?token=<jwt> — convenience for wscat / curl demos
	if t := r.URL.Query().Get("token"); t != "" {
		return t, false
	}
	return "", false
}

func closeCodeFor(err error) websocket.StatusCode {
	if err == nil {
		return websocket.StatusNormalClosure
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return websocket.StatusPolicyViolation
	}
	return websocket.StatusNormalClosure
}

func closeReasonFor(err error) string {
	if err == nil {
		return ""
	}
	if len(err.Error()) > 120 {
		return err.Error()[:120]
	}
	return err.Error()
}

func isExpectedClose(err error) bool {
	if err == nil {
		return true
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code == websocket.StatusNormalClosure ||
			ce.Code == websocket.StatusGoingAway
	}
	return false
}
