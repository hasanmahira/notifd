package domain

import "fmt"

// Priority maps to a queue name. See docs/queue-priority.md (or README) for the
// weighted-round-robin rationale (6/3/1).
type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

func (p Priority) Valid() bool {
	switch p {
	case PriorityHigh, PriorityNormal, PriorityLow:
		return true
	}
	return false
}

// Queue returns the Asynq queue name for this priority. Worker server is
// configured with matching weights in the queue package.
func (p Priority) Queue() string {
	switch p {
	case PriorityHigh:
		return "high"
	case PriorityLow:
		return "low"
	default:
		return "default"
	}
}

func ParsePriority(s string) (Priority, error) {
	p := Priority(s)
	if !p.Valid() {
		return "", fmt.Errorf("%w: priority %q", ErrInvalidInput, s)
	}
	return p, nil
}
