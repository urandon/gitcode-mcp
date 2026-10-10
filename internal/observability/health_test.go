package observability

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func healthFixture(key, signal string, at time.Time) HealthEvidence {
	return HealthEvidence{Cohort: KnownRef("correlation", key), Origin: "sync", Collection: "wiki", Signal: signal, Reason: "operation_failed", At: at,
		Refs: Context{Job: KnownRef("job", "synthetic-private-job"), Cache: KnownRef("cache", "synthetic-private-path"), Repo: KnownRef("repo", "private/repository")}}
}
func TestHealthProjectionRecoveryRetryAndRestartAreAuthorityDerived(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	fail := healthFixture("same", "failed", at)
	retry := healthFixture("same", "retrying", at.Add(time.Second))
	retry.RetryAfter = at.Add(time.Minute)
	retry.LastSuccess = at.Add(-time.Hour)
	got := ProjectExecutionHealth(at.Add(10*time.Second), []HealthEvidence{retry, fail, fail})
	if got.State != "degraded" || got.Active != 1 || len(got.Incidents) != 1 || got.Incidents[0].State != "retrying" || !got.Incidents[0].FirstObservedAt.Equal(at) || got.Incidents[0].RetryAfter == nil || got.Incidents[0].LastSuccessAt == nil {
		t.Fatal("durable retry history lost")
	}
	success := healthFixture("same", "succeeded", at.Add(2*time.Second))
	recovered := ProjectExecutionHealth(at.Add(10*time.Second), []HealthEvidence{success, retry, fail})
	if recovered.State != "healthy" || recovered.Active != 0 || recovered.Recovered != 1 || recovered.Incidents[0].State != "recovered" || recovered.Incidents[0].RetryAfter != nil {
		t.Fatal("confirmed success did not recover incident")
	}
	// Restart and repeated reads are pure reconstruction, not new incidents.
	if again := ProjectExecutionHealth(at.Add(10*time.Second), []HealthEvidence{fail, retry, success, fail}); !reflect.DeepEqual(again, recovered) {
		t.Fatal("replay changed health history")
	}
	*recovered.Incidents[0].LastSuccessAt = at.Add(24 * time.Hour)
	if got := ProjectExecutionHealth(at, []HealthEvidence{fail, retry, success}); got.Incidents[0].LastSuccessAt.Equal(*recovered.Incidents[0].LastSuccessAt) {
		t.Fatal("returned DTO mutated source authority")
	}
}
func TestHealthProjectionFiniteSafeAndBounded(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	inputs := make([]HealthEvidence, MaxIncidents+9)
	for i := range inputs {
		inputs[i] = healthFixture(fmt.Sprint(i), "failed", at.Add(time.Duration(i)*time.Second))
	}
	out := ProjectExecutionHealth(at, inputs)
	if len(out.Incidents) != MaxIncidents || out.Active != len(inputs) || out.Omitted != 9 || out.Coverage != "partial" || out.State != "degraded" {
		t.Fatal("bounded history hid active failures")
	}
	for i, j := 0, len(inputs)-1; i < j; i, j = i+1, j-1 {
		inputs[i], inputs[j] = inputs[j], inputs[i]
	}
	if !reflect.DeepEqual(ProjectExecutionHealth(at, inputs), out) {
		t.Fatal("map/input iteration changed retained incidents")
	}
	data, _ := json.Marshal(out)
	for _, private := range []string{"synthetic-private-job", "synthetic-private-path", "private/repository"} {
		if strings.Contains(string(data), private) {
			t.Fatal("health leaked authority identity")
		}
	}
	invalid := healthFixture("x", "failed", at)
	invalid.Reason = "synthetic-private-error"
	if got := ProjectExecutionHealth(at, []HealthEvidence{invalid}); got.State != "unknown" || got.Coverage != "partial" || len(got.Incidents) != 0 {
		t.Fatal("invalid evidence fabricated healthy state")
	}
	if got := ProjectExecutionHealth(at, make([]HealthEvidence, MaxHealthEvidence+1)); got.State != "unknown" || got.Reason != "evidence_limit" {
		t.Fatal("unbounded source projection accepted")
	}
}
func TestHealthProjectionEqualTimestampCannotProveRecovery(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	out := ProjectExecutionHealth(at, []HealthEvidence{healthFixture("same", "succeeded", at), healthFixture("same", "failed", at)})
	if out.State != "degraded" || out.Incidents[0].State != "active" {
		t.Fatal("equal timestamps invented ordered recovery")
	}
}

func TestHealthProjectionInspectionPhaseAndCoverageAreClosed(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	unknown := healthFixture("unknown", "unknown", time.Time{})
	unknown.Origin, unknown.Phase, unknown.Reason = "repository_docs", "inspection", "source_inspection_unobserved"
	invalid := healthFixture("invalid", "failed", at)
	invalid.Phase = "synthetic-private-phase"
	for _, evidence := range [][]HealthEvidence{{unknown, invalid}, {invalid, unknown}} {
		got := ProjectExecutionHealth(at, evidence)
		if got.State != "unknown" || got.Coverage != "partial" || got.Reason != "invalid_authority_evidence" || len(got.Incidents) != 0 {
			t.Fatal("mixed invalid/missing inspection evidence became complete or nondeterministic")
		}
	}
	wrongOrigin := unknown
	wrongOrigin.Origin = "sync"
	if got := ProjectExecutionHealth(at, []HealthEvidence{wrongOrigin}); got.Reason != "invalid_authority_evidence" {
		t.Fatal("inspection phase accepted an unrelated execution origin")
	}
}
