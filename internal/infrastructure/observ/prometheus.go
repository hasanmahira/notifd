package observ

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics bundles every counter/histogram/gauge the service exports.
// All metrics are registered against a single registry so main can hand that
// registry to the /metrics handler without touching globals.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPRequestDuration *prometheus.HistogramVec
	NotificationCreated *prometheus.CounterVec
	NotificationSent    *prometheus.CounterVec
	NotificationRetry   *prometheus.CounterVec
	NotificationDLQ     *prometheus.CounterVec
	ProviderLatency     *prometheus.HistogramVec
	RateLimitDeny       *prometheus.CounterVec
	CircuitBreakerState *prometheus.GaugeVec
	QueueDepth          *prometheus.GaugeVec
	WebSocketConns      prometheus.Gauge
	WebSocketMessages   prometheus.Counter
}

// NewMetrics registers all metrics against a fresh registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{Registry: reg}

	m.HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "notifd",
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route", "status"})

	m.NotificationCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "notifd",
		Name:      "notifications_created_total",
		Help:      "Total notifications accepted by the API, partitioned by channel and priority.",
	}, []string{"channel", "priority"})

	m.NotificationSent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "notifd",
		Name:      "notifications_sent_total",
		Help:      "Total delivery outcomes at the worker, partitioned by channel and terminal status.",
	}, []string{"channel", "status"})

	m.NotificationRetry = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "notifd",
		Name:      "notifications_retry_total",
		Help:      "Total retry attempts for notifications, partitioned by channel.",
	}, []string{"channel"})

	m.NotificationDLQ = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "notifd",
		Name:      "notifications_dead_letter_total",
		Help:      "Total notifications moved to the dead_letter/archived state.",
	}, []string{"channel"})

	m.ProviderLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "notifd",
		Name:      "provider_request_duration_seconds",
		Help:      "Provider HTTP call latency in seconds.",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
	}, []string{"provider", "outcome"})

	m.RateLimitDeny = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "notifd",
		Name:      "ratelimit_deny_total",
		Help:      "Number of rate-limit denials per channel.",
	}, []string{"channel"})

	m.CircuitBreakerState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "notifd",
		Name:      "circuit_breaker_state",
		Help:      "0=closed, 1=half-open, 2=open.",
	}, []string{"provider"})

	m.QueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "notifd",
		Name:      "queue_depth",
		Help:      "Current queue depth per Asynq queue name, sampled periodically.",
	}, []string{"queue", "state"})

	m.WebSocketConns = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "notifd",
		Name:      "websocket_active_connections",
		Help:      "Current number of open WebSocket subscriber connections.",
	})

	m.WebSocketMessages = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "notifd",
		Name:      "websocket_messages_sent_total",
		Help:      "Total messages pushed over all WebSocket connections.",
	})

	reg.MustRegister(
		m.HTTPRequestDuration,
		m.NotificationCreated,
		m.NotificationSent,
		m.NotificationRetry,
		m.NotificationDLQ,
		m.ProviderLatency,
		m.RateLimitDeny,
		m.CircuitBreakerState,
		m.QueueDepth,
		m.WebSocketConns,
		m.WebSocketMessages,
	)
	return m
}

// usecase.MetricsRecorder impls. Kept on *Metrics so the wiring in main.go can
// pass the same bundle into Service, Sender, and the HTTP layer.

func (m *Metrics) IncCreated(channel, priority string) {
	m.NotificationCreated.WithLabelValues(channel, priority).Inc()
}

func (m *Metrics) IncSent(channel, status string) {
	m.NotificationSent.WithLabelValues(channel, status).Inc()
}

func (m *Metrics) IncRetry(channel string) {
	m.NotificationRetry.WithLabelValues(channel).Inc()
}

func (m *Metrics) IncDeadLetter(channel string) {
	m.NotificationDLQ.WithLabelValues(channel).Inc()
}

func (m *Metrics) IncRateLimitDeny(channel string) {
	m.RateLimitDeny.WithLabelValues(channel).Inc()
}

// ObserveProviderLatency is the minimal surface the provider package needs.
// Separate method name so the provider can keep its own tiny port interface
// rather than depend on *Metrics.
func (m *Metrics) ObserveProviderLatency(providerName, outcome string, seconds float64) {
	m.ProviderLatency.WithLabelValues(providerName, outcome).Observe(seconds)
}
