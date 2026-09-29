package provider

import (
	"errors"
	"log/slog"
	"time"

	"github.com/sony/gobreaker"
)

// ErrBreakerOpen is returned by the breaker when the circuit is OPEN.
// WebhookClient translates this to domain.ErrCircuitOpen.
var ErrBreakerOpen = errors.New("circuit breaker open")

// NewBreaker returns a sony/gobreaker wrapped with our desired thresholds:
//   - 5 consecutive failures OR >50% failure rate over the window -> trip.
//   - 60s open -> half-open -> probe -> closed or re-open.
func NewBreaker(name string, log *slog.Logger) *Breaker2 {
	settings := gobreaker.Settings{
		Name:        name,
		MaxRequests: 1,                // half-open probes allowed at a time
		Interval:    60 * time.Second, // rolling window for counters
		Timeout:     60 * time.Second, // open -> half-open
		ReadyToTrip: func(c gobreaker.Counts) bool {
			if c.ConsecutiveFailures >= 5 {
				return true
			}
			if c.Requests < 10 {
				return false
			}
			return float64(c.TotalFailures)/float64(c.Requests) > 0.5
		},
		OnStateChange: func(n string, from, to gobreaker.State) {
			log.Warn("circuit breaker state change",
				slog.String("name", n),
				slog.String("from", from.String()),
				slog.String("to", to.String()),
			)
		},
	}
	return &Breaker2{cb: gobreaker.NewCircuitBreaker(settings)}
}

// Breaker2 adapts gobreaker's generic CircuitBreaker to our `Breaker` port shape.
// (The suffix avoids a name clash with the interface; the concrete type is internal.)
type Breaker2 struct {
	cb *gobreaker.CircuitBreaker
}

func (b *Breaker2) Execute(fn func() (any, error)) (any, error) {
	res, err := b.cb.Execute(func() (any, error) { return fn() })
	if errors.Is(err, gobreaker.ErrOpenState) {
		return nil, ErrBreakerOpen
	}
	return res, err
}

// State returns the current breaker state string for metric labeling.
func (b *Breaker2) State() string {
	return b.cb.State().String()
}
