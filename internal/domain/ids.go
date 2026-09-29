package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// NewID returns a UUIDv7 (time-ordered, k-sortable). Used for every primary key
// in this project. Falls back to v4 only if the OS clock somehow fails — this
// is unreachable in practice but keeps the function total.
func NewID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

// ParseID parses a UUID string and wraps the error as ErrInvalidInput so the
// HTTP layer can map it to a 400.
func ParseID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: invalid uuid %q", ErrInvalidInput, s)
	}
	return id, nil
}
