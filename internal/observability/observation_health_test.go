package observability

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestObservationLossDoesNotAlterExecutionHealth(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	evidence := []HealthEvidence{healthFixture("same", "failed", at), healthFixture("same", "succeeded", at.Add(time.Second))}
	execution := ProjectExecutionHealth(at, evidence)
	coverage := Coverage{State: "ready", OutputState: "managed"}
	metrics := MetricsSnapshot{State: "ready", SampledAt: at}
	if got := ProjectObservationHealth(coverage, metrics); got.State != "ready" {
		t.Fatal("ready observation unavailable")
	}
	coverage.State, coverage.Dropped, coverage.LossEpoch = "partial", 3, 1
	coverage.Reason = "synthetic-private-error"
	obs := ProjectObservationHealth(coverage, metrics)
	if obs.State != "partial" || obs.DroppedEvents != 3 || obs.Reason != "ledger_loss" {
		t.Fatal("ledger loss hidden")
	}
	if !reflect.DeepEqual(execution, ProjectExecutionHealth(at, evidence)) || execution.State != "healthy" {
		t.Fatal("telemetry loss altered durable health")
	}
	data, _ := json.Marshal(obs)
	if strings.Contains(string(data), coverage.Reason) {
		t.Fatal("unreviewed observation reason leaked")
	}
}

func TestObservationHealthAvailabilityAndSampleOwnership(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, tc := range []struct {
		coverage Coverage
		metrics  MetricsSnapshot
		want     string
	}{
		{Coverage{}, MetricsSnapshot{}, "unavailable"},
		{Coverage{State: "initializing", OutputState: "managed"}, MetricsSnapshot{State: "ready"}, "initializing"},
		{Coverage{State: "ready", OutputState: "legacy_unbounded"}, MetricsSnapshot{State: "ready", SampledAt: at}, "partial"},
		{Coverage{State: "ready", OutputState: "managed"}, MetricsSnapshot{State: "partial", SampledAt: at, Dropped: 1}, "partial"},
		{Coverage{State: "ready", OutputState: "managed"}, MetricsSnapshot{State: "ready", SampledAt: at}, "ready"},
	} {
		if got := ProjectObservationHealth(tc.coverage, tc.metrics); got.State != tc.want {
			t.Fatal("observation readiness differs")
		}
	}
	metrics := MetricsSnapshot{State: "ready", SampledAt: at}
	got := ProjectObservationHealth(Coverage{State: "ready", OutputState: "managed"}, metrics)
	*got.MetricsSampledAt = at.Add(time.Hour)
	if !metrics.SampledAt.Equal(at) {
		t.Fatal("health DTO mutated metric source")
	}
}
