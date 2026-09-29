package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/infrastructure/auth"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hasanmahira/notifd/internal/usecase"
)

// requestID middleware reads/creates X-Request-ID and injects it into ctx so
// slog.ContextHandler pulls it into every log line.
func requestIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if id == "" {
				id = uuid.NewString()
			}
			w.Header().Set("X-Request-ID", id)
			ctx := observ.WithRequestID(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// accessLogMiddleware emits a structured log line per request with timing, and
// records the latency into the Prometheus histogram when metrics are wired.
func accessLogMiddleware(log *slog.Logger, m *observ.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			dur := time.Since(start)
			log.LogAttrs(r.Context(), slog.LevelInfo, "http access",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Duration("duration", dur),
				slog.String("remote", r.RemoteAddr),
			)
			if m != nil {
				route := chi.RouteContext(r.Context()).RoutePattern()
				if route == "" {
					route = r.URL.Path
				}
				m.HTTPRequestDuration.
					WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).
					Observe(dur.Seconds())
			}
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) {
	s.status = c
	s.ResponseWriter.WriteHeader(c)
}

// Unwrap lets http.ResponseController reach the underlying writer so WebSocket
// Hijack and http/2 Flush work through our middleware wrapper.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// authMiddleware checks the Authorization: Bearer <jwt> header.
// /healthz, /readyz, /metrics are public (wired in router.go before this).
func authMiddleware(verifier auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				writeError(w, r, fmt.Errorf("%w: missing bearer token", domain.ErrUnauthorized))
				return
			}
			claims, err := verifier.Verify(token)
			if err != nil {
				writeError(w, r, err)
				return
			}
			ctx := auth.WithClaims(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		// fallback: ?token=... (used by the WebSocket handshake)
		return r.URL.Query().Get("token")
	}
	const p = "Bearer "
	if strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

// idempotencyMiddleware guards POSTs that mutate state. Behavior:
//   - first request with a key -> proceed, cache body hash and response
//   - replay with same key + same body -> return cached response
//   - replay with same key + different body -> 409 ErrIdempotencyMismatch
//   - replay while original is still in flight -> 409 Retry-After: 2 (after 5s poll)
//
// Body-hash check is crypto-strength (SHA-256) so truncation or near-collisions
// can't let a different payload sneak through.
func idempotencyMiddleware(store usecase.IdempotencyStore, log *slog.Logger) func(http.Handler) http.Handler {
	const ttl = 24 * time.Hour
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				writeError(w, r, fmt.Errorf("%w: Idempotency-Key required", domain.ErrInvalidInput))
				return
			}

			// Buffer the body once so we can both hash it and let the handler read it.
			body, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				writeError(w, r, fmt.Errorf("%w: reading body: %s", domain.ErrInvalidInput, err.Error()))
				return
			}
			hash := sha256Hex(body)
			r.Body = io.NopCloser(bytes.NewReader(body))

			claimed, existing, err := store.Claim(r.Context(), key, hash, ttl)
			if err != nil {
				// Redis outage: degrade — DB unique constraint is the durable backstop.
				log.Warn("idempotency claim failed; allowing through", slog.String("err", err.Error()))
				next.ServeHTTP(w, r)
				return
			}
			if !claimed {
				if existing != "" && existing != hash {
					writeError(w, r, fmt.Errorf("%w", domain.ErrIdempotencyMismatch))
					return
				}
				replayCached(w, r, store, key, log)
				return
			}

			// capture downstream response to cache
			capture := &responseCapture{ResponseWriter: w, buf: &bytes.Buffer{}, status: http.StatusOK}
			next.ServeHTTP(capture, r)

			// Cache the response against a ctx that survives the client
			// disconnecting. Without this, a client hanging up after POSTing
			// would leave the idempotency claim key pinned on :pending for
			// 24 h — any retry in that window would poll 5 s then 409.
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
			defer cancel()
			if err := store.SaveResponse(saveCtx, key, capture.status, capture.buf.Bytes(), ttl); err != nil {
				log.Warn("idempotency save failed", slog.String("err", err.Error()))
			}
		})
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type responseCapture struct {
	http.ResponseWriter
	buf         *bytes.Buffer
	status      int
	wroteHeader bool
}

func (c *responseCapture) WriteHeader(s int) {
	if c.wroteHeader {
		return
	}
	c.status = s
	c.wroteHeader = true
	c.ResponseWriter.WriteHeader(s)
}

func (c *responseCapture) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	_, _ = c.buf.Write(p)
	return c.ResponseWriter.Write(p)
}

func replayCached(w http.ResponseWriter, r *http.Request, store usecase.IdempotencyStore, key string, log *slog.Logger) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body, pending, err := store.LookupResponse(r.Context(), key)
		if err == nil && !pending {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Idempotent-Replayed", "true")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			log.Warn("idempotency lookup error; allowing through", slog.String("err", err.Error()))
			writeError(w, r, fmt.Errorf("%w: %s", domain.ErrIdempotencyPending, "internal"))
			return
		}
		if time.Now().After(deadline) {
			writeError(w, r, fmt.Errorf("%w: waited 5s for first request to complete", domain.ErrIdempotencyPending))
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// bodyLimitMiddleware returns 413 for oversized payloads.
func bodyLimitMiddleware(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// decodeJSON's error is mapped by the caller via writeError; checkMaxBytes
// lifts MaxBytesError into a 413 response so oversized bodies don't get
// misreported as 400 invalid_input.
func checkMaxBytes(err error) bool {
	var mbErr *http.MaxBytesError
	return errors.As(err, &mbErr)
}

// decodeJSON is a small helper for handlers. Discriminates body-too-large from
// JSON-invalid so the HTTP layer can choose 413 vs 400.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if checkMaxBytes(err) {
			return fmt.Errorf("%w: body too large", errTooLarge)
		}
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: empty body", domain.ErrInvalidInput)
		}
		return fmt.Errorf("%w: %s", domain.ErrInvalidInput, err.Error())
	}
	return nil
}

// errTooLarge is an HTTP-layer-only sentinel so errors.go can map it to 413.
var errTooLarge = errors.New("payload too large")

// Ensures ctx imported.
var _ = context.Background
