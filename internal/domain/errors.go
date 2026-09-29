package domain

import "errors"

// Sentinel errors the HTTP layer maps to specific status codes. Infrastructure
// adapters wrap these with %w so errors.Is works across the stack.
var (
	ErrInvalidInput        = errors.New("invalid input")
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrIdempotencyPending  = errors.New("idempotency in progress")
	ErrIdempotencyMismatch = errors.New("idempotency key reused with different body")
	ErrTerminal            = errors.New("terminal state")
	ErrBatchTooLarge       = errors.New("batch size exceeds limit")
	ErrRateLimited         = errors.New("rate limited")
	ErrProviderTransient   = errors.New("provider transient error")
	ErrProviderTerminal    = errors.New("provider terminal error")
	ErrCircuitOpen         = errors.New("circuit breaker open")
	ErrUnauthorized        = errors.New("unauthorized")
)
