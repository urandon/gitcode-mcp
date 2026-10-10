package gitcode

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"gitcode-mcp/internal/observability"
)

// providerMetricAttempt observes one policy-loop Client.Do invocation and its
// bounded response transfer. It is not a TCP packet count or durable receipt.
// No endpoint, header, coordinate, body, or error string crosses this boundary.
type providerMetricAttempt struct {
	metrics  *observability.Metrics
	started  time.Time
	finished bool
}

func newProviderMetricAttempt(ctx context.Context) *providerMetricAttempt {
	return &providerMetricAttempt{metrics: observability.MetricsFromContext(ctx)}
}

func (a *providerMetricAttempt) start() {
	if a == nil || a.metrics == nil {
		return
	}
	a.started = time.Now()
	a.metrics.AttachOrigin("provider")
}

func (a *providerMetricAttempt) finish(status int, err error) {
	if a == nil || a.metrics == nil || a.started.IsZero() || a.finished {
		return
	}
	a.finished = true
	outcome := providerMetricOutcome(status, err)
	labels := map[string]string{"outcome": outcome}
	a.metrics.Add("provider_outcomes", labels, 1)
	a.metrics.Observe("provider_duration", labels, time.Since(a.started).Seconds())
}

func providerMetricOutcome(status int, err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	switch status {
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "forbidden"
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge:
		return "invalid"
	}
	if status < 200 || status >= 300 {
		return "failed"
	}
	if err != nil {
		return "invalid"
	}
	return "succeeded"
}
