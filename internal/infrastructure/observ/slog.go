package observ

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// ctxKey is a private type to prevent context-key collisions.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	notificationIDKey
)

// WithRequestID returns a copy of ctx with the request id attached.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts the request id from ctx or returns "".
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// WithNotificationID attaches a notification id to ctx.
func WithNotificationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, notificationIDKey, id)
}

// NotificationID extracts the notification id from ctx.
func NotificationID(ctx context.Context) string {
	if v, ok := ctx.Value(notificationIDKey).(string); ok {
		return v
	}
	return ""
}

// ContextHandler wraps a slog.Handler and adds correlation fields from ctx on every log.
type ContextHandler struct {
	slog.Handler
}

func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if id := NotificationID(ctx); id != "" {
		r.AddAttrs(slog.String("notification_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// NewLogger returns a JSON slog.Logger with correlation-ID support.
// service is "api" or "worker" — added as a persistent default attr.
func NewLogger(level, service string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     lvl,
		AddSource: false,
	})
	h := ContextHandler{Handler: base}
	return slog.New(h).With(slog.String("service", service))
}
