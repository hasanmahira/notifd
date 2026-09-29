package queue

import (
	"context"
	"log/slog"
	"time"

	"github.com/hasanmahira/notifd/internal/config"
	"github.com/hasanmahira/notifd/internal/infrastructure/observ"
	"github.com/hibiken/asynq"
)

// StartDepthSampler polls Asynq's Inspector every `interval` and publishes
// per-queue depth gauges for Prometheus. Returns a stop func that blocks until
// the sampler goroutine exits.
func StartDepthSampler(ctx context.Context, rd config.RedisConfig, m *observ.Metrics, log *slog.Logger, interval time.Duration) func() {
	insp := asynq.NewInspector(asynq.RedisClientOpt{Addr: rd.Addr, Password: rd.Password, DB: rd.DB})
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer func() { _ = insp.Close() }()
		if interval <= 0 {
			interval = 5 * time.Second
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sample(insp, m, log)
			}
		}
	}()

	return func() {
		<-done
	}
}

func sample(insp *asynq.Inspector, m *observ.Metrics, log *slog.Logger) {
	queues := []string{"high", "default", "low"}
	for _, q := range queues {
		info, err := insp.GetQueueInfo(q)
		if err != nil {
			log.Debug("queue info unavailable", slog.String("queue", q), slog.Any("err", err))
			continue
		}
		m.QueueDepth.WithLabelValues(q, "pending").Set(float64(info.Pending))
		m.QueueDepth.WithLabelValues(q, "active").Set(float64(info.Active))
		m.QueueDepth.WithLabelValues(q, "scheduled").Set(float64(info.Scheduled))
		m.QueueDepth.WithLabelValues(q, "retry").Set(float64(info.Retry))
		m.QueueDepth.WithLabelValues(q, "archived").Set(float64(info.Archived))
	}
}
