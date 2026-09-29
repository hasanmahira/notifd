package usecase

import (
	"log/slog"
	"time"
)

// MetricsRecorder is the minimal observability surface the Service and Sender
// need. Concrete impls live in internal/infrastructure/observ. A nil recorder
// is supported for tests — use NoopRecorder{} where a value is required.
type MetricsRecorder interface {
	IncCreated(channel, priority string)
	IncSent(channel, status string)
	IncRetry(channel string)
	IncDeadLetter(channel string)
	IncRateLimitDeny(channel string)
}

// NoopRecorder is a zero-overhead MetricsRecorder for tests.
type NoopRecorder struct{}

func (NoopRecorder) IncCreated(string, string) {}
func (NoopRecorder) IncSent(string, string)    {}
func (NoopRecorder) IncRetry(string)           {}
func (NoopRecorder) IncDeadLetter(string)      {}
func (NoopRecorder) IncRateLimitDeny(string)   {}

// Service is the single entry point for every HTTP handler. Dependencies are
// injected via constructor; no package-level globals. Every method takes a
// context and returns the concrete domain types.
type Service struct {
	repo      Repository
	pub       Publisher
	templates TemplateRepository // optional; nil is fine when the API doesn't use templates
	metrics   MetricsRecorder
	log       *slog.Logger

	// now is injectable for deterministic tests.
	now func() time.Time
}

type ServiceDeps struct {
	Repo      Repository
	Pub       Publisher
	Templates TemplateRepository
	Metrics   MetricsRecorder
	Log       *slog.Logger
	Now       func() time.Time
}

func NewService(deps ServiceDeps) *Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Metrics == nil {
		deps.Metrics = NoopRecorder{}
	}
	return &Service{
		repo:      deps.Repo,
		pub:       deps.Pub,
		templates: deps.Templates,
		metrics:   deps.Metrics,
		log:       deps.Log,
		now:       deps.Now,
	}
}
