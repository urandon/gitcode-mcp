package observability

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHealthSourceRefIsTrustedOpaqueAndOriginQualified(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	a := healthFixture("source-a", "failed", at)
	a.Origin, a.Phase, a.Reason = "repository_docs", "inspection", "source_unavailable"
	a.Refs.Source = KnownRef("source", "synthetic-private-source-a")
	b := a
	b.Cohort = KnownRef("correlation", "source-b")
	b.Refs.Source = KnownRef("source", "synthetic-private-source-b")
	out := ProjectExecutionHealth(at, []HealthEvidence{a, b})
	if len(out.Incidents) != 2 || out.Incidents[0].SourceRef == "" || out.Incidents[1].SourceRef == "" || out.Incidents[0].SourceRef == out.Incidents[1].SourceRef {
		t.Fatal("distinct source incidents have indistinguishable references")
	}
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "synthetic-private-source") {
		t.Fatal("health disclosed source authority")
	}
	for _, bad := range []HealthEvidence{func() HealthEvidence {
		e := a
		e.Refs.Source = KnownRef("job", "source-a")
		return e
	}(), func() HealthEvidence {
		e := healthFixture("other", "failed", at)
		e.Refs.Source = a.Refs.Source
		return e
	}()} {
		got := ProjectExecutionHealth(at, []HealthEvidence{bad})
		if got.Coverage != "partial" || got.Reason != "invalid_authority_evidence" || len(got.Incidents) != 0 {
			t.Fatal("invalid source evidence was accepted")
		}
	}
	a.Refs.Source = Ref{}
	out = ProjectExecutionHealth(at, []HealthEvidence{a})
	data, _ = json.Marshal(out)
	if len(out.Incidents) != 1 || out.Incidents[0].SourceRef != "" || strings.Contains(string(data), "source_ref") {
		t.Fatal("missing source authority fabricated a reference")
	}
}
