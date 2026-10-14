//go:build darwin || linux

package localauthority

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

func runtimeObservationFixture(t *testing.T) (*Coordinator, *observability.Collector, func() bool) {
	return runtimeObservationCohortFixture(t, false)
}

func runtimeObservationCohortFixture(t *testing.T, secondProject bool) (*Coordinator, *observability.Collector, func() bool) {
	t.Helper()
	s, registry, dir := readyDocumentStore(t)
	if s.Close() != nil {
		t.Fatal("runtime observation source did not join")
	}
	bindings := []ProjectBinding{{testProject, dir}}
	if secondProject {
		manifest := testManifest()
		manifest.ID = authority.UUID("22222222-2222-4222-8222-222222222222")
		other := privateTestDirectory(t)
		store, err := Initialize(context.Background(), registry, other, manifest)
		if err != nil || store.Close() != nil {
			t.Fatal("second actual cohort source unavailable")
		}
		bindings = append(bindings, ProjectBinding{manifest.ID, other})
	}
	prepared, err := registry.PrepareRuntime()
	if err != nil {
		t.Fatal("runtime observation ownership unavailable")
	}
	collector := observability.NewQualificationCollector(observability.Config{Directory: filepath.Join(privateTestDirectory(t), "runtime-events"), ManagedOutput: true})
	ctx := observability.WithObservation(context.Background(), collector, observability.Context{})
	c, err := newStrictCoordinator(ctx, prepared, bindings, false)
	if err != nil {
		t.Fatal("runtime observation coordinator unavailable")
	}
	c.jobsStarted = true
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("runtime observation collector idle boundary unavailable")
	}
	t.Cleanup(func() {
		// A close-failure control deliberately retains the actual error. Cleanup
		// joins the owner without reclassifying its tested terminal outcome.
		_ = c.Close(context.Background())
		resume()
		if prepared.Close() != nil || !collector.Close(context.Background(), true) {
			t.Error("runtime observation fixture did not join")
		}
	})
	return c, collector, resume
}

func runtimeEvidence(t *testing.T, collector *observability.Collector, resume func() bool) observability.Page {
	t.Helper()
	if !resume() || !collector.Close(context.Background(), true) {
		t.Fatal("runtime observations did not seal")
	}
	refs := observability.ProjectEntityRefs(observability.Context{Project: observability.KnownRef("project", string(testProject))})
	page, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityPhaseChanged, ProjectRef: refs.Project}, "", 200)
	if err != nil {
		t.Fatal("runtime evidence unavailable")
	}
	return page
}

func TestRuntimeObservationExplicitServingReplacementAndJoinedClose(t *testing.T) {
	c, collector, resume := runtimeObservationFixture(t)
	first, err := c.AdmitServing(context.Background())
	if err != nil {
		t.Fatal("actual serving admission unavailable")
	}
	first.Seal()
	first.Close()
	if !c.Available() {
		t.Fatal("listener exit incorrectly closed writer")
	}
	second, err := c.AdmitServing(context.Background())
	if err != nil {
		t.Fatal("explicit joined replacement unavailable")
	}
	second.Close()
	if c.Close(context.Background()) != nil || c.Close(context.Background()) != nil {
		t.Fatal("actual repeated close failed")
	}
	page := runtimeEvidence(t, collector, resume)
	want := []string{"serving", "serving", "closing", "stopped"}
	if len(page.Events) != len(want) || page.Coverage.Dropped != 0 {
		t.Fatal("runtime boundaries were lost, duplicated or invented")
	}
	for i, event := range page.Events {
		if event.Phase != "runtime" || event.Outcome != want[i] || event.RepoRef != "" || event.JobRef != "" || event.CacheRef != "" {
			t.Fatal("runtime event changed admitted scope or actual boundary")
		}
	}
}

func TestRuntimeObservationCallerDeadlineDoesNotDecideJoinedStop(t *testing.T) {
	c, collector, resume := runtimeObservationFixture(t)
	_, _, release, err := c.begin(context.Background(), testProject)
	if err != nil {
		t.Fatal("actual owned operation unavailable")
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if !errors.Is(c.Close(ctx), context.DeadlineExceeded) {
		t.Fatal("bounded close did not preserve caller deadline")
	}
	select {
	case <-c.done:
		t.Fatal("shutdown bypassed admitted operation")
	default:
	}
	release()
	if c.Close(context.Background()) != nil {
		t.Fatal("eventual joined shutdown failed")
	}
	page := runtimeEvidence(t, collector, resume)
	if len(page.Events) != 2 || page.Events[0].Outcome != "closing" || page.Events[1].Outcome != "stopped" || page.Coverage.Dropped != 0 {
		t.Fatal("caller timeout suppressed or fabricated final owner outcome")
	}
}

func TestRuntimeObservationPendingServingPublicationJoinsShutdown(t *testing.T) {
	c, collector, resume := runtimeObservationFixture(t)
	lease, publication, err := c.admitServing(context.Background())
	if err != nil || publication == nil {
		t.Fatal("actual serving publication admission unavailable")
	}
	defer publication.publish()
	lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if !errors.Is(c.Close(ctx), context.DeadlineExceeded) {
		t.Fatal("shutdown overtook admitted serving publication")
	}
	select {
	case <-c.done:
		t.Fatal("shutdown sealed before pending runtime proof joined")
	default:
	}
	publication.publish()
	publication.publish() // one boundary, never another Done or event
	if c.Close(context.Background()) != nil {
		t.Fatal("pending runtime publication did not release shutdown")
	}
	page := runtimeEvidence(t, collector, resume)
	counts := map[string]int{}
	for _, event := range page.Events {
		counts[event.Outcome]++
	}
	// Closing and a previously admitted serving proof may be delivered out of
	// order. Neither is current health; stopped must join both publications.
	if len(page.Events) != 3 || counts["serving"] != 1 || counts["closing"] != 1 || counts["stopped"] != 1 || page.Events[2].Outcome != "stopped" || page.Coverage.Dropped != 0 {
		t.Fatal("pending runtime proof was lost or mistaken for current health")
	}
}

func TestRuntimeObservationCohortCloseFailureNeverClaimsStopped(t *testing.T) {
	c, collector, resume := runtimeObservationCohortFixture(t, true)
	// Exercise an actual OS close failure on the second owner's retained
	// descriptor. No fixture callback or synthesized Close result is used.
	s := c.order[1].store
	s.mu.Lock()
	err := s.lease.file.Close()
	s.mu.Unlock()
	if err != nil {
		t.Fatal("actual retained descriptor failure control unavailable")
	}
	if c.Close(context.Background()) == nil || c.Close(context.Background()) == nil {
		t.Fatal("actual close failure was hidden or changed on retry")
	}
	page := runtimeEvidence(t, collector, resume)
	if len(page.Events) != 1 || page.Events[0].Outcome != "closing" || page.Coverage.Dropped != 2 {
		t.Fatal("partial cohort close invented stopped or omitted partial coverage")
	}
}

func TestRuntimeObservationRejectedAndUnobservedAdmissionStayQuiet(t *testing.T) {
	c, collector, resume := runtimeObservationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lease, err := c.AdmitServing(ctx); !errors.Is(err, context.Canceled) || lease != nil {
		t.Fatal("cancelled serving boundary was admitted")
	}
	lease, err := c.AdmitServing(context.Background())
	if err != nil {
		t.Fatal("serving control admission unavailable")
	}
	if duplicate, err := c.AdmitServing(context.Background()); err == nil || duplicate != nil {
		t.Fatal("duplicate active serving boundary admitted")
	}
	lease.Close()
	if c.Close(context.Background()) != nil {
		t.Fatal("observed quiet admission owner did not join")
	}
	if page := runtimeEvidence(t, collector, resume); len(page.Events) != 3 || page.Coverage.Dropped != 0 {
		t.Fatal("rejected serving admission invented proof or loss")
	}
	unobserved, _, _ := deferredIndexCoordinator(t)
	other, publication, err := unobserved.admitServing(context.Background())
	if err != nil || publication != nil {
		t.Fatal("unobserved owner allocated a publication")
	}
	other.Close()
	if unobserved.Close(context.Background()) != nil {
		t.Fatal("unobserved owner retained publication lifetime")
	}
}

func TestRuntimeObservationConcurrentCloseConservesCanonicalProofOrLoss(t *testing.T) {
	c, collector, resume := runtimeObservationFixture(t)
	capture := &canonicalObservation{project: testProject}
	ctx := context.WithValue(context.Background(), canonicalObservationKey{}, capture)
	store, call, release, err := c.begin(ctx, testProject)
	if err != nil {
		t.Fatal("concurrent runtime proof turn unavailable")
	}
	defer release()
	req := request(t, "issue.create", "synthetic-runtime-concurrent-proof", authority.IssuePayload{Title: "Concurrent runtime proof", State: "open"})
	receipt, err := store.Mutate(call, req, testActor)
	if err != nil || receipt.Validate() != nil {
		t.Fatal("concurrent runtime control did not commit actual source")
	}
	closed := make(chan error, 1)
	go func() { closed <- c.Close(context.Background()) }()
	select {
	case <-c.lifetime.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent runtime shutdown did not seal admission")
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal("concurrent runtime shutdown failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent runtime shutdown did not join")
	}
	if !resume() || !collector.Close(context.Background(), true) {
		t.Fatal("concurrent runtime observations did not seal")
	}
	refs := observability.ProjectEntityRefs(observability.Context{Project: observability.KnownRef("project", string(testProject))})
	page, err := collector.Query(observability.Filter{ProjectRef: refs.Project}, "", 200)
	if err != nil || uint64(len(page.Events))+page.Coverage.Dropped != 3 {
		t.Fatal("concurrent confirmed boundaries were neither retained nor counted lost")
	}
	reader, err := OpenReadOnly(context.Background(), store.directory, testProject)
	if err != nil {
		t.Fatal("concurrent runtime source readback unavailable")
	}
	defer reader.Close()
	read, err := reader.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "get", EntityID: receipt.Citation.EntityID})
	if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != receipt.Citation {
		t.Fatal("observation contention changed confirmed canonical source")
	}
}
