//go:build darwin || linux

package observability

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEventFilterClosedCatalogAndCompatibility(t *testing.T) {
	valid := []Filter{
		{Code: ProviderAttempt, Severity: "info", Phase: "fetch", Collection: "wiki", Outcome: "timeout", FailureClass: "timeout"},
		{Code: JobTerminal, Outcome: "failed", FailureClass: "timeout"},
		{Code: MaintenanceStateChanged, SourceRef: KnownRef("source", "a").value, Outcome: "degraded", FailureClass: "repository_docs"},
		{Code: SyncStageChanged, StageRef: KnownRef("stage", "a").value, Outcome: "waiting_commit", FailureClass: "writer_busy"},
		{Code: MaintenanceStateChanged, PreviousRegistrationRef: KnownRef("registration", "a").value, Outcome: "identity_unresolved"},
		{Since: "2026-01-01T00:00:00.000Z", Until: "2026-01-08T00:00:00Z"},
		// Legacy lifecycle evidence can retain either stream.
		{Code: RecoveryFailed, Stream: "stdout"},
		{Code: RecoveryFinished, Stream: "stderr"},
		{Code: JobTerminal, Stream: "stdout"}, {Code: WriterContended, Stream: "stderr"},
	}
	for _, f := range valid {
		if _, err := ValidateFilter(f); err != nil {
			t.Fatal("catalog-valid filter rejected")
		}
	}
	invalid := []Filter{
		{Code: JobTerminal, Stream: "stderr"}, {Code: JobAdmitted, Stream: "stderr"}, {Code: WriterContended, Stream: "stdout"},
		{Code: ProviderAttempt, Outcome: "succeeded", FailureClass: "timeout"}, {Code: ProviderAttempt, Phase: "embedding", Collection: "wiki"},
		{Code: JobTerminal, Outcome: "timeout"}, {Code: Boot, Outcome: "succeeded"}, {Code: JobTerminal, SourceRef: KnownRef("source", "a").value},
		{SourceRef: KnownRef("source", "a").value, StageRef: KnownRef("stage", "a").value},
		{Code: AdminRequestCompleted, JobRef: KnownRef("job", "a").value},
		{Component: "jobs", Code: ProviderAttempt}, {Code: ProviderAttempt, Severity: "error"},
		{Phase: "synthetic-private-phase"}, {Collection: "synthetic-private-collection"}, {Outcome: "synthetic-private-outcome"}, {FailureClass: "synthetic-private-failure"},
		{JobRef: KnownRef("source", "a").value}, {RepoRef: "https://private.invalid/example"},
		{Since: "2026-01-01T00:00:00Z"}, {Until: "2026-01-01T00:00:00Z"},
		{Since: "2026-01-01T00:00:00+00:00", Until: "2026-01-01T00:00:00Z"},
		{Since: "2026-01-02T00:00:00Z", Until: "2026-01-01T00:00:00Z"},
		{Since: "2026-01-01T00:00:00Z", Until: "2026-01-08T00:00:00.000000001Z"},
		{Since: "bad", Until: ""},
	}
	for _, f := range invalid {
		if _, err := ValidateFilter(f); err == nil {
			t.Fatal("unsupported or conflicting filter accepted")
		}
	}
	for _, e := range filterCatalogShapes {
		f := Filter{Stream: e.Stream, Code: e.Code, Component: e.Component, Severity: e.Severity, Phase: e.Phase, Collection: e.Collection, Outcome: e.Outcome, FailureClass: e.FailureClass, SourceRef: e.SourceRef, StageRef: e.StageRef, PreviousRegistrationRef: e.PreviousRegistrationRef}
		if _, err := ValidateFilter(f); err != nil {
			t.Fatal("admission shape and filter catalog drifted")
		}
	}
}

func TestEventDetailValuesFollowExecutionDomainsAndAreIsolated(t *testing.T) {
	values := EventDetailValues()
	for key, expected := range map[string][]string{
		"maintenance_scope":      {"registration", "repository_docs", "identity_resolution"},
		"previous_state":         {"absent", "unknown", "enrolled", "ready", "registered", "not_indexed", "identity_conflict", "cache_clone_conflict", "identity_unresolved"},
		"previous_failure_class": {"identity", "cache", "embedding", "repository_docs", "operation_failed"},
		"work_kind":              workKinds, "admin_route": adminRoutes,
	} {
		for _, want := range expected {
			found := false
			for _, got := range values[key] {
				found = found || got == want
			}
			if !found {
				t.Fatal("admitted execution detail omitted")
			}
		}
	}
	for _, value := range values["previous_state"] {
		if !maintenanceState(value) && !maintenanceSourceState(value) && !maintenanceIdentityPredecessor(value) && value != "absent" && value != "unknown" {
			t.Fatal("unadmitted predecessor exposed")
		}
	}
	for _, value := range values["previous_failure_class"] {
		if !maintenanceFailure(value) {
			t.Fatal("unadmitted previous failure exposed")
		}
	}
	values["work_kind"][0] = "synthetic-private"
	delete(values, "previous_state")
	if reflect.DeepEqual(values, EventDetailValues()) || workKinds[0] == "synthetic-private" {
		t.Fatal("caller mutated the catalog")
	}
}

func TestEventCursorBindsEverySelectorBeforeObservation(t *testing.T) {
	c := New(Config{Directory: testDirectory(t)})
	ready(t, c)
	flush(t, c, true)
	page, err := c.Query(Filter{}, "", 1)
	if err != nil {
		t.Fatal("initial query unavailable")
	}
	changes := []Filter{
		{Stream: "stdout"}, {Severity: "info"}, {Component: "daemon"}, {Code: Boot}, {Phase: "fetch"}, {Collection: "wiki"}, {Outcome: "failed"}, {FailureClass: "timeout"},
		{JobRef: KnownRef("job", "a").value}, {RegistrationRef: KnownRef("registration", "a").value}, {CacheRef: KnownRef("cache", "a").value}, {RepoRef: KnownRef("repo", "a").value},
		{CorrelationRef: KnownRef("correlation", "a").value}, {StageRef: KnownRef("stage", "a").value}, {SourceRef: KnownRef("source", "a").value}, {PreviousRegistrationRef: KnownRef("registration", "a").value},
		{Since: "2026-01-01T00:00:00Z", Until: "2026-01-02T00:00:00Z"},
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range changes {
		_, err := c.QueryContext(context.Background(), f, page.Cursor, 1)
		var q QueryError
		if !errors.As(err, &q) || q.Code != "invalid_cursor" || q.Reason != "filter_changed" {
			t.Fatal("cursor did not bind every selector before collector access")
		}
	}
	for _, token := range []string{"!", strings.Repeat("a", 513)} {
		_, err := c.QueryContext(context.Background(), Filter{}, token, 1)
		var q QueryError
		if !errors.As(err, &q) || q.Code != "invalid_cursor" || q.Reason != "malformed" {
			t.Fatal("malformed cursor observed collector")
		}
	}
}

func TestEventFilterNormalizationAndInclusiveTime(t *testing.T) {
	f := Filter{Stream: " STDOUT ", Severity: " INFO ", Component: " PROVIDER ", Since: "2026-01-01T00:00:00.000Z", Until: "2026-01-01T01:00:00.000Z"}
	n, hash, err := normalizeFilter(f)
	if err != nil || n.Stream != "stdout" || n.Since != "2026-01-01T00:00:00Z" {
		t.Fatal("canonical normalization changed")
	}
	_, canonicalHash, err := normalizeFilter(n)
	if err != nil || hash != canonicalHash {
		t.Fatal("equivalent selectors changed cursor binding")
	}
	e := Event{Stream: "stdout", Severity: "info", Component: "provider", OccurredAt: parseFilterTime(n.Since)}
	if !n.match(e) {
		t.Fatal("inclusive lower time boundary lost")
	}
	e.OccurredAt = parseFilterTime(n.Until)
	if !n.match(e) {
		t.Fatal("inclusive upper time boundary lost")
	}
	e.OccurredAt = e.OccurredAt.Add(time.Nanosecond)
	if n.match(e) {
		t.Fatal("time bound admitted outside event")
	}
	for _, field := range []string{"JobRef", "RegistrationRef", "CacheRef", "RepoRef", "CorrelationRef", "StageRef", "SourceRef", "PreviousRegistrationRef", "Phase", "Collection", "Outcome", "FailureClass"} {
		selector := Filter{}
		event := Event{}
		reflect.ValueOf(&selector).Elem().FieldByName(field).SetString("match")
		if selector.match(event) {
			t.Fatal("nonmatching selector ignored")
		}
		reflect.ValueOf(&event).Elem().FieldByName(field).SetString("match")
		if !selector.match(event) {
			t.Fatal("matching selector not projected")
		}
	}
}
