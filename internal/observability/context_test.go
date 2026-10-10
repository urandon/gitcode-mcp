package observability

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMetricContextPreservesExecutionCancellationAndDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	parent, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	metrics := NewMetrics(strings.Repeat("c", 32), time.Now().UTC())
	ctx := WithMetrics(parent, metrics)
	if MetricsFromContext(ctx) != metrics || WithMetrics(parent, nil) != parent {
		t.Fatal("concrete observation context changed identity")
	}
	if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatal("observation changed deadline")
	}
	child, stop := context.WithCancel(ctx)
	defer stop()
	cancel()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("observation disconnected execution cancellation")
	}
	if MetricsFromContext(child) != metrics || MetricsFromContext(context.Background()) != nil {
		t.Fatal("trusted sink inheritance leaked into unrelated execution")
	}
}
