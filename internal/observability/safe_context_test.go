package observability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSafeContextOnlyReturnedFiniteEvidenceAndOpaqueRefs(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	admitFixture(t, c, RecoveryFinished, Context{Job: KnownRef("job", "synthetic-private-job")})
	flush(t, c, true)
	at := time.Unix(1700000000, 0).UTC()
	health := ProjectExecutionHealth(at, []HealthEvidence{healthFixture("cohort", "failed", at)})
	r := &Reader{Collector: c, Now: func() time.Time { return at }, Health: func(context.Context, time.Time) (ExecutionHealth, error) { return health, nil }}
	s, err := r.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Events(context.Background(), EventsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if s.CopyContext == nil || s.CopyContext.Schema != 1 || s.CopyContext.ExecutionState != "degraded" || s.CopyContext.Incident != nil || s.CopyContext.Event != nil {
		t.Fatal("summary copy context lost independent finite state")
	}
	for _, selection := range []ContextSelection{{}, {EventID: p.Events[0].EventID}, {IncidentRef: health.Incidents[0].IncidentRef}} {
		copy, err := BuildSafeContext(context.Background(), s, &p, selection)
		if err != nil {
			t.Fatal(err)
		}
		data, err := MarshalSafeContext(context.Background(), copy)
		if err != nil || len(data) > MaxSafeCopyBytes {
			t.Fatal("copy context violated byte ceiling")
		}
		for _, private := range []string{"synthetic-private", "private/repository", "config_reference", "effective_cache_path", "resources", "incidents"} {
			if strings.Contains(string(data), private) {
				t.Fatal("copy context leaked broad/private input")
			}
		}
		if selection.EventID != "" && (copy.Event == nil || copy.Event.EventID != selection.EventID) {
			t.Fatal("selected event missing")
		}
		if selection.IncidentRef != "" && (copy.Incident == nil || copy.Incident.IncidentRef != selection.IncidentRef) {
			t.Fatal("selected retained incident missing")
		}
	}
	bad := p
	bad.Events = append([]Event(nil), p.Events...)
	bad.Events[0].Message = "synthetic-private-error"
	if _, err := BuildSafeContext(context.Background(), s, &bad, ContextSelection{EventID: bad.Events[0].EventID}); err == nil {
		t.Fatal("copy accepted arbitrary event message")
	}
	badSummary := s
	badSummary.ExecutionHealth.Incidents = append([]Incident(nil), health.Incidents...)
	badSummary.ExecutionHealth.Incidents[0].Reason = "synthetic-private-error"
	if _, err := BuildSafeContext(context.Background(), badSummary, nil, ContextSelection{IncidentRef: health.Incidents[0].IncidentRef}); err == nil {
		t.Fatal("copy accepted arbitrary incident reason")
	}
	for _, selectBad := range []ContextSelection{{EventID: "synthetic-private"}, {EventID: p.Events[0].EventID, IncidentRef: health.Incidents[0].IncidentRef}, {EventID: strings.Repeat("f", 32)}} {
		if _, err := BuildSafeContext(context.Background(), s, &p, selectBad); err == nil {
			t.Fatal("invalid/not-retained copy selection accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BuildSafeContext(ctx, s, &p, ContextSelection{}); !errors.Is(err, context.Canceled) {
		t.Fatal("copy ignored cancellation")
	}
	if ProjectEntityRefs(Context{}) != nil {
		t.Fatal("absent authoritative refs were fabricated")
	}
	data, _ := json.Marshal(s.CopyContext)
	if len(data) > MaxSafeCopyBytes {
		t.Fatal("baseline copy overflows")
	}
}
