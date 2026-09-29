package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hasanmahira/notifd/internal/domain"
)

// ErrorBody is the JSON shape for every 4xx/5xx response.
type ErrorBody struct {
	Error     string   `json:"error"`
	Message   string   `json:"message"`
	RequestID string   `json:"request_id,omitempty"`
	Details   []string `json:"details,omitempty"`
}

// writeError maps a domain/sentinel error to an HTTP status + ErrorBody.
// Anything unknown becomes 500 and the user-facing Message is sanitized so we
// don't leak pgx / redis / stack trace detail to clients.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, errTooLarge):
		status, code = http.StatusRequestEntityTooLarge, "payload_too_large"
	case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrBatchTooLarge):
		status, code = http.StatusBadRequest, "invalid_input"
	case errors.Is(err, domain.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrTerminal):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, domain.ErrIdempotencyMismatch):
		status, code = http.StatusConflict, "idempotency_key_reused_different_body"
	case errors.Is(err, domain.ErrIdempotencyPending):
		status, code = http.StatusConflict, "idempotency_in_progress"
	case errors.Is(err, domain.ErrUnauthorized):
		status, code = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, domain.ErrRateLimited):
		status, code = http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, domain.ErrCircuitOpen), errors.Is(err, domain.ErrProviderTransient):
		status, code = http.StatusServiceUnavailable, "upstream_unavailable"
	}

	msg := err.Error()
	// 5xx bodies go out with a generic message — the original error is still
	// logged at the middleware's access log + slog chain.
	if status >= http.StatusInternalServerError {
		msg = "an internal error occurred; see logs"
	}

	body := ErrorBody{Error: code, Message: msg}
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusConflict && errors.Is(err, domain.ErrIdempotencyPending) {
		w.Header().Set("Retry-After", "2")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
