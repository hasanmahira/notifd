package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/usecase"
)

// WebhookClient posts rendered notifications to the simulated webhook.site.
// Behaviors:
//   - HTTP 202 + JSON body -> success (messageId + status persisted)
//   - 4xx (except 429)     -> terminal fail (ErrProviderTerminal)
//   - 5xx / 429 / timeout  -> transient (ErrProviderTransient)
//   - Retry-After honored for 429/503.
type WebhookClient struct {
	url     string
	hc      *http.Client
	log     *slog.Logger
	breaker Breaker
	metrics LatencyRecorder
}

// LatencyRecorder is the tiny subset of observ.Metrics the provider needs.
// Extracted so the package's import graph stays leaf-ward.
type LatencyRecorder interface {
	ObserveProviderLatency(providerName, outcome string, seconds float64)
}

// noopLatency is the default when no recorder is supplied (e.g. in tests).
type noopLatency struct{}

func (noopLatency) ObserveProviderLatency(string, string, float64) {}

type Breaker interface {
	Execute(fn func() (any, error)) (any, error)
}

type Config struct {
	URL     string
	Timeout time.Duration
}

func New(cfg Config, breaker Breaker, log *slog.Logger, metrics LatencyRecorder) *WebhookClient {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	}
	if metrics == nil {
		metrics = noopLatency{}
	}
	return &WebhookClient{
		url: cfg.URL,
		hc: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
		log:     log,
		breaker: breaker,
		metrics: metrics,
	}
}

// payload matches the mock provider's expected request shape.
type payload struct {
	To      string          `json:"to"`
	Channel string          `json:"channel"`
	Content json.RawMessage `json:"content"`
}

// response matches the mock provider's expected 202 body.
type response struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// Send implements usecase.Provider.
func (c *WebhookClient) Send(ctx context.Context, n domain.Notification) (usecase.ProviderResult, error) {
	body, err := json.Marshal(payload{
		To:      string(n.UserNotification.Recipient),
		Channel: string(n.Message.Channel),
		Content: json.RawMessage(n.Message.Content),
	})
	if err != nil {
		return usecase.ProviderResult{}, fmt.Errorf("%w: marshal: %s", domain.ErrProviderTerminal, err.Error())
	}

	callResult, err := c.breaker.Execute(func() (any, error) {
		return c.do(ctx, body)
	})
	if err != nil {
		if errors.Is(err, ErrBreakerOpen) {
			return usecase.ProviderResult{}, fmt.Errorf("%w: circuit open", domain.ErrCircuitOpen)
		}
		return usecase.ProviderResult{}, err
	}
	res, _ := callResult.(usecase.ProviderResult)
	return res, nil
}

func (c *WebhookClient) do(ctx context.Context, body []byte) (usecase.ProviderResult, error) {
	start := time.Now()
	outcome := "error"
	defer func() {
		c.metrics.ObserveProviderLatency("webhook", outcome, time.Since(start).Seconds())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return usecase.ProviderResult{}, fmt.Errorf("%w: new request: %s", domain.ErrProviderTerminal, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		c.log.Debug("provider http error", slog.String("url", c.url), slog.Any("err", err))
		return usecase.ProviderResult{}, fmt.Errorf("%w: %s", domain.ErrProviderTransient, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	outcome = http.StatusText(resp.StatusCode)

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return usecase.ProviderResult{}, fmt.Errorf("%w: read body: %s", domain.ErrProviderTransient, err.Error())
	}

	switch {
	case resp.StatusCode == http.StatusAccepted:
		var out response
		if err := json.Unmarshal(raw, &out); err != nil {
			return usecase.ProviderResult{}, fmt.Errorf("%w: parse 202 body: %s", domain.ErrProviderTerminal, err.Error())
		}
		ts, _ := time.Parse(time.RFC3339, out.Timestamp)
		return usecase.ProviderResult{MessageID: out.MessageID, Status: out.Status, Timestamp: ts}, nil

	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		ra := retryAfter(resp)
		msg := fmt.Sprintf("provider %d (retry-after=%s): %s", resp.StatusCode, ra, snippet(raw))
		return usecase.ProviderResult{}, fmt.Errorf("%w: %s", domain.ErrProviderTransient, msg)

	default: // 4xx non-429
		msg := fmt.Sprintf("provider %d: %s", resp.StatusCode, snippet(raw))
		return usecase.ProviderResult{}, fmt.Errorf("%w: %s", domain.ErrProviderTerminal, msg)
	}
}

func retryAfter(resp *http.Response) time.Duration {
	h := resp.Header.Get("Retry-After")
	if h == "" {
		return 0
	}
	// seconds form
	if n, err := strconv.Atoi(h); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	// HTTP-date form
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

func snippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n]) + "...(truncated)"
	}
	return string(b)
}
