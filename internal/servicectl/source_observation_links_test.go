package servicectl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func TestTwoSourceInspectionIncidentsLinkOnlyOwnRetainedEvents(t *testing.T) {
	m, store, firstPath, reg, firstID := inspectionBoundaryFixture(t)
	secondPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(filepath.Dir(firstPath), "second-repository"), "synthetic-private-second-source")
	entry := m.entries[reg]
	second, ok, err := m.RegisterRepositoryDocsSource(context.Background(), entry.CacheUUID, entry.RepoID, secondPath, "")
	if err != nil || !ok || second.RepositoryDocs == nil {
		t.Fatal("second source fixture unavailable")
	}
	secondID := second.RepositoryDocs.SourceRegistrationID
	if firstID == secondID {
		t.Fatal("distinct authoritative sources collided")
	}
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	at := time.Unix(1700000000, 0).UTC()
	m.now = func() time.Time { return at }
	run := func(id string) {
		t.Helper()
		ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
		defer flush()
		// Select the same private authority bundle used by the real reconcile
		// owner. Do not synthesize failure health or infer identity from a cohort.
		snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
		source := m.sources[reg][id]
		state := source.State
		snapshot.RepositoryDocs = &state
		snapshot.repositoryPath = source.RepositoryPath
		snapshot.repositoryProfile = source.Profile
		snapshot.repositorySourceID = source.State.SourceRegistrationID
		m.reconcileRepositoryDocsState(ctx, store, &snapshot, at)
		m.finishReconcileEntry(ctx, reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, Job{}, at)
	}
	for i, p := range []string{firstPath, secondPath} {
		if os.Rename(p, p+"-parked") != nil {
			t.Fatal("actual source failure fixture unavailable")
		}
		at = at.Add(time.Minute)
		run([]string{firstID, secondID}[i])
	}
	health := m.ExecutionHealth(at)
	if health.Active != 2 || len(health.Incidents) != 2 || health.Coverage != "retained" {
		t.Fatal("two authoritative source inspection failures not retained independently")
	}
	sealJobObservation(t, c)
	seen := map[string]bool{}
	for _, incident := range health.Incidents {
		if incident.SourceRef == "" || seen[incident.SourceRef] || incident.RegistrationRef != expectedObservationRef("registration", reg) {
			t.Fatal("incident lost distinct source reference")
		}
		seen[incident.SourceRef] = true
		p, err := c.QueryContext(context.Background(), observability.Filter{SourceRef: incident.SourceRef}, "", 200)
		if err != nil || p.HasMore || len(p.Events) != 1 {
			t.Fatal("source incident did not resolve its own transition")
		}
		e := p.Events[0]
		if e.Code != observability.MaintenanceStateChanged || e.SourceRef != incident.SourceRef || e.Outcome != "degraded" || e.MaintenanceScope != "repository_docs" {
			t.Fatal("incident link crossed source boundary")
		}
	}
	for _, id := range []string{firstID, secondID} {
		if !seen[expectedObservationRef("source", id)] {
			t.Fatal("source and incident reference algorithms diverged")
		}
	}
	listed := cloneMaintenanceEntry(m.entries[reg])
	documentation := repositoryDocumentationObservation(context.Background(), store, entry.RepoID, &listed, 0)
	if len(documentation.Sources) != 2 {
		t.Fatal("two-source Admin bridge missing listed source authority")
	}
	for _, source := range documentation.Sources {
		if source.ObservationRefs == nil || source.ObservationRefs.Source != expectedObservationRef("source", source.SourceID) || !seen[source.ObservationRefs.Source] {
			t.Fatal("listed source and incident reference bridge diverged")
		}
	}
	if documentation.ObservationRefs == nil || documentation.ObservationRefs.Source != expectedObservationRef("source", documentation.SourceID) {
		t.Fatal("selected source reference bridge diverged")
	}
	reloaded := NewMaintenanceManager(m.manager, NewJobManager(""), m.path)
	if reloaded.Load() != nil || !reflect.DeepEqual(health, reloaded.ExecutionHealth(at)) {
		t.Fatal("restart changed retained source incident links")
	}
	data, _ := json.Marshal(health)
	for _, private := range []string{firstPath, secondPath, firstID, secondID, reg, entry.RepoID, "synthetic-private"} {
		if strings.Contains(string(data), private) {
			t.Fatal("source incident leaked private authority")
		}
	}
	if len(m.jobs.jobs) != 0 {
		t.Fatal("inspection observation admitted work")
	}
}
