//go:build darwin || linux

package observability

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalAuthorityPhaseCatalogFiltersReplayAndCopy(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	project := KnownRef("project", "synthetic-private-phase-project")
	correlation := KnownRef("correlation", "synthetic-phase-boundary")
	pairs := map[string][]string{
		"index_job":          {"queued", "running", "cancelling", "cancelled", "succeeded", "failed", "interrupted"},
		"knowledge_recovery": {"queued", "running", "cancelling", "cancelled", "succeeded", "failed", "interrupted"},
		"projection_repair":  {"prepared", "quarantined", "confirmed"},
		"runtime":            {"serving", "closing", "stopped"},
	}
	for phase, outcomes := range pairs {
		for _, outcome := range outcomes {
			filter := Filter{Code: LocalAuthorityPhaseChanged, ProjectRef: project.value, Phase: phase, Outcome: outcome}
			if _, err := ValidateFilter(filter); err != nil {
				t.Fatal("valid local phase pair omitted from finite filters")
			}
			admitFixture(t, c, LocalAuthorityPhaseChanged, Context{Project: project, Correlation: correlation, ExecutionDetails: ExecutionDetails{Phase: phase, Outcome: outcome}})
		}
	}
	for _, bad := range []Filter{
		{Code: LocalAuthorityPhaseChanged, ProjectRef: project.value, Phase: "index_job", Outcome: "confirmed"},
		{Code: LocalAuthorityPhaseChanged, ProjectRef: project.value, Phase: "projection_repair", Outcome: "running"},
		{Code: LocalAuthorityPhaseChanged, ProjectRef: project.value, Phase: "document_commit", Outcome: "committed"},
		{Code: LocalAuthorityPhaseChanged, ProjectRef: project.value, Phase: "runtime", Outcome: "running"},
		{Code: LocalAuthorityPhaseChanged, ProjectRef: project.value, JobRef: KnownRef("job", "synthetic-job").value},
	} {
		if _, err := ValidateFilter(bad); err == nil {
			t.Fatal("local phase filter admitted an invalid pair or remote bridge")
		}
	}
	flush(t, c, true)
	filter := Filter{Code: LocalAuthorityPhaseChanged, ProjectRef: project.value}
	page, err := c.Query(filter, "", 200)
	if err != nil || len(page.Events) != 20 {
		t.Fatal("confirmed phase catalog evidence missing")
	}
	summary, err := (&Reader{Collector: c}).Summary(context.Background())
	if err != nil || summary.ExecutionHealth.State != "unknown" {
		t.Fatal("historical phases invented current health")
	}
	copy, err := BuildSafeContext(context.Background(), summary, &page, ContextSelection{EventID: page.Events[0].EventID})
	if err != nil || copy.Event == nil || copy.Event.ProjectRef != project.value {
		t.Fatal("safe copy lost true project phase")
	}
	body, _ := MarshalSafeContext(context.Background(), copy)
	if strings.Contains(string(body), "synthetic-private") {
		t.Fatal("phase copy disclosed raw owner")
	}
	next := New(Config{Directory: path, ManagedOutput: true})
	ready(t, next)
	flush(t, next, true)
	reloaded, err := next.Query(filter, "", 200)
	if err != nil || len(reloaded.Events) != len(page.Events) || reloaded.Events[0] != page.Events[0] {
		t.Fatal("phase ledger replay changed retained proof")
	}
}

func TestLocalAuthorityScopeFiltersReplayAndSafeCopy(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	private := "synthetic-private-admitted-project"
	ctx := WithObservation(context.Background(), c, Context{Cache: KnownRef("cache", "unrelated-cache"), Repo: KnownRef("repo", "unrelated-repo"), Correlation: KnownRef("correlation", "untrusted-outer-correlation")})
	o := BindLocalProject(ctx, private)
	// Test fixture admission may retry; the production observer never retries.
	refs := Context{Project: o.project, Correlation: KnownRef("correlation", "synthetic-confirmed-boundary"), ExecutionDetails: ExecutionDetails{Phase: "authority_commit", Outcome: "committed"}}
	admitFixture(t, c, LocalAuthorityCommitted, refs)
	refs.Phase = "document_commit"
	admitFixture(t, c, LocalAuthorityCommitted, refs)
	flush(t, c, true)
	filter := Filter{ProjectRef: o.project.value, Code: LocalAuthorityCommitted}
	p, err := c.Query(filter, "", 200)
	if err != nil || len(p.Events) != 2 {
		t.Fatal("project scope lost confirmed commit events")
	}
	for _, e := range p.Events {
		if !e.valid() || e.ProjectRef != o.project.value || e.CacheRef != "" || e.RepoRef != "" || e.JobRef != "" || e.RegistrationRef != "" {
			t.Fatal("project event fabricated remote authority")
		}
	}
	other := filter
	other.ProjectRef = KnownRef("project", "different-admitted-project").value
	if _, err := c.Query(other, p.Cursor, 200); err == nil {
		t.Fatal("project filter change reused an old cursor")
	}
	for _, bad := range []Filter{{ProjectRef: private}, {ProjectRef: o.project.value, Code: Boot}, {ProjectRef: o.project.value, CacheRef: KnownRef("cache", "cache").value}, {ProjectRef: o.project.value, Phase: "fetch"}} {
		if _, err := ValidateFilter(bad); err == nil {
			t.Fatal("project filter admitted invalid or mixed scope")
		}
	}
	summary, err := (&Reader{Collector: c}).Summary(context.Background())
	if err != nil || summary.ExecutionHealth.State != "unknown" {
		t.Fatal("commit events invented current execution health")
	}
	copy, err := BuildSafeContext(context.Background(), summary, &p, ContextSelection{EventID: p.Events[0].EventID})
	if err != nil || copy.Event == nil || copy.Event.ProjectRef != o.project.value {
		t.Fatal("safe copy dropped known project identity")
	}
	body, _ := MarshalSafeContext(context.Background(), copy)
	if strings.Contains(string(body), private) || strings.Contains(string(body), "unrelated-") {
		t.Fatal("project evidence leaked raw authority or caller labels")
	}
	// Same durable ledger reload preserves the historical project identity.
	next := New(Config{Directory: path, ManagedOutput: true})
	ready(t, next)
	flush(t, next, true)
	reloaded, err := next.Query(filter, "", 200)
	if err != nil || len(reloaded.Events) != 2 || reloaded.Events[0] != p.Events[0] {
		t.Fatal("ledger replay lost exact project evidence")
	}
}

func TestLocalAuthorityObserverLossIsNonblockingAndScopeIsClosed(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	o := BindLocalProject(WithObservation(context.Background(), c, Context{}), "synthetic-private-owner")
	c.mu.Lock()
	o.SourceCommitted()
	o.DocumentCommitted()
	c.mu.Unlock()
	if c.Coverage().Dropped != 2 {
		t.Fatal("actual admission loss was omitted or retried")
	}
	base := Context{Project: o.project, Correlation: KnownRef("correlation", "owned-boundary"), ExecutionDetails: ExecutionDetails{Phase: "authority_commit", Outcome: "committed"}}
	for _, change := range []string{"cache", "job", "raw-project", "phase", "failure", "collection"} {
		bad := base
		switch change {
		case "cache":
			bad.Cache = KnownRef("cache", "cache")
		case "job":
			bad.Job = KnownRef("job", "job")
		case "raw-project":
			bad.Project = Ref{"synthetic-private-owner"}
		case "phase":
			bad.Phase = "synthetic-private-phase"
		case "failure":
			bad.FailureClass = "operation_failed"
		case "collection":
			bad.Collection = "issues"
		}
		if c.Emit(LocalAuthorityCommitted, bad) {
			t.Fatal("closed local event scope accepted unrelated dimensions")
		}
	}
	if c.Emit(Boot, base) || ProjectEntityRefs(Context{Project: o.project, Cache: KnownRef("cache", "cache")}) != nil {
		t.Fatal("local identity became a lifecycle or remote bridge")
	}
	refs := ProjectEntityRefs(Context{Project: o.project})
	if refs == nil || refs.Project != o.project.value || refs.Cache != "" {
		t.Fatal("known project bridge changed identity")
	}
	BindLocalProject(context.Background(), "synthetic-private-owner").SourceCommitted()
	flush(t, c, true)
	p, err := c.Query(Filter{ProjectRef: o.project.value}, "", 200)
	if err != nil || len(p.Events) != 0 || p.Coverage.Dropped != 2 {
		t.Fatal("loss or invalid input manufactured a confirmed commit")
	}
	body, _ := json.Marshal(p)
	if strings.Contains(string(body), "synthetic-private") {
		t.Fatal("rejected project input entered retained evidence")
	}
}
