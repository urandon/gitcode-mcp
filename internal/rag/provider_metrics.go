package rag

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"gitcode-mcp/internal/observability"
)

// HTTP attempts are transport evidence, not successful embedding operations.
// Capture no input, endpoint, model, header, response or error text. The owner
// finishes exactly once per actual Client.Do invocation, after body release.
type embeddingMetricAttempt struct {
	metrics  *observability.Metrics
	started  time.Time
	finished bool
}

func startEmbeddingMetricAttempt(ctx context.Context) *embeddingMetricAttempt {
	metrics := observability.MetricsFromContext(ctx)
	if metrics == nil {
		return nil
	}
	metrics.AttachOrigin("provider")
	return &embeddingMetricAttempt{metrics: metrics, started: time.Now()}
}

func (a *embeddingMetricAttempt) finish(status int, err error, completed time.Time) {
	if a == nil || a.finished || completed.IsZero() {
		return
	}
	a.finished = true
	outcome := "succeeded"
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		outcome = "cancelled"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout():
		outcome = "timeout"
	case status == http.StatusTooManyRequests:
		outcome = "rate_limited"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		outcome = "forbidden"
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity || status == http.StatusRequestEntityTooLarge:
		outcome = "invalid"
	case status < 200 || status >= 300:
		outcome = "failed"
	case err != nil:
		outcome = "invalid"
	}
	labels := map[string]string{"outcome": outcome}
	a.metrics.Add("provider_outcomes", labels, 1)
	a.metrics.Observe("provider_duration", labels, completed.Sub(a.started).Seconds())
}
