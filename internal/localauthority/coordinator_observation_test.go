//go:build darwin || linux

package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

func observedProjectFixture(t *testing.T) (*Coordinator, *PreparedRegistry, string, *observability.Collector) {
	t.Helper()
	s, registry, dir := readyDocumentStore(t)
	if s.Close() != nil {
		t.Fatal("observed source setup did not join")
	}
	prepared, err := registry.PrepareRuntime()
	if err != nil {
		t.Fatal("observed source capability unavailable")
	}
	collector := observability.NewQualificationCollector(observability.Config{Directory: filepath.Join(privateTestDirectory(t), "observations"), ManagedOutput: true})
	ctx := observability.WithObservation(context.Background(), collector, observability.Context{Cache: observability.KnownRef("cache", "unrelated-outer-cache")})
	c, err := newStrictCoordinator(ctx, prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("observed source coordinator unavailable")
	}
	c.jobsStarted = true
	t.Cleanup(func() {
		if c.Close(context.Background()) != nil || prepared.Close() != nil {
			t.Error("observed source owner did not join")
		}
		if !collector.Close(context.Background(), true) {
			t.Error("observation writer did not join")
		}
	})
	return c, prepared, dir, collector
}

func TestCoordinatorObservesOnlyNewConfirmedSourceAndGitCommits(t *testing.T) {
	c, prepared, dir, collector := observedProjectFixture(t)
	ctx := context.Background()
	resume, err := collector.PauseWriterForQualification(ctx)
	if err != nil {
		t.Fatal("observation fixture did not reach actual idle boundary")
	}
	defer resume()
	issue := request(t, "issue.create", "synthetic-private-observed-source", authority.IssuePayload{Title: "Synthetic private observed title", Body: "synthetic-private-source-bytes", State: "open"})
	first, err := c.Mutate(ctx, issue, testActor)
	if err != nil || first.Replayed {
		t.Fatal("observed source did not commit")
	}
	if replay, err := c.Mutate(ctx, issue, testActor); err != nil || !replay.Replayed {
		t.Fatal("observed source replay failed")
	}
	doc := documentRequest(t, "document.create", "synthetic-private-observed-document", authority.EmptyKnowledgeRef, authority.Citation{}, "# synthetic-private-Git-only-bytes")
	written, err := c.MutateDocument(ctx, doc, testActor)
	if err != nil || written.Receipt.Replayed {
		t.Fatal("observed Git document did not commit")
	}
	if replay, err := c.MutateDocument(ctx, doc, testActor); err != nil || !replay.Receipt.Replayed {
		t.Fatal("observed document replay failed")
	}
	bad := issue
	bad.Payload = json.RawMessage(`{"title":"different","body":"different","state":"open"}`)
	if _, err := c.Mutate(ctx, bad, testActor); err == nil {
		t.Fatal("conflicting original key was admitted")
	}
	unknown := issue
	unknown.Intent.ProjectID = "22222222-2222-4222-8222-222222222222"
	if _, err := c.Mutate(ctx, unknown, testActor); err == nil {
		t.Fatal("unknown project was admitted")
	}
	interrupted, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Mutate(interrupted, issue, testActor); !errors.Is(err, context.Canceled) {
		t.Fatal("source cancellation was changed by observation")
	}
	if _, err := c.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: first.Citation.EntityID, Operation: "get"}); err != nil {
		t.Fatal("observed canonical read failed")
	}
	if c.Close(ctx) != nil {
		t.Fatal("observed owner did not join")
	}
	if !resume() {
		t.Fatal("actual commit observation window expired")
	}
	// Reopened owner replay is not a new throughput event either.
	nextCtx := observability.WithObservation(ctx, collector, observability.Context{})
	next, err := newStrictCoordinator(nextCtx, prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("observed owner reopen failed")
	}
	next.jobsStarted = true
	defer next.Close(ctx)
	if replay, err := next.Mutate(ctx, issue, testActor); err != nil || !replay.Replayed {
		t.Fatal("reopened observed source replay failed")
	}
	if next.Close(ctx) != nil || !collector.Close(ctx, true) {
		t.Fatal("observed owners did not join")
	}
	known := observability.ProjectEntityRefs(observability.Context{Project: observability.KnownRef("project", string(testProject))})
	page, err := collector.Query(observability.Filter{ProjectRef: known.Project}, "", 200)
	if err != nil || len(page.Events) != 2 {
		t.Fatal("new commits were lost, duplicated or invented")
	}
	phases, correlations := map[string]bool{}, map[string]bool{}
	for _, event := range page.Events {
		if event.Code != observability.LocalAuthorityCommitted || event.ProjectRef != known.Project || event.CacheRef != "" || event.RepoRef != "" || event.JobRef != "" || event.Outcome != "committed" {
			t.Fatal("observed source acquired false remote identity")
		}
		phases[event.Phase], correlations[event.CorrelationRef] = true, true
	}
	if !phases["authority_commit"] || !phases["document_commit"] || len(correlations) != 2 {
		t.Fatal("confirmed commit phases lost their owned correlation")
	}
	body, _ := json.Marshal(page)
	for _, secret := range []string{dir, string(testProject), string(first.Citation.EntityID), string(written.Receipt.Citation.EntityID), testActor.ID, issue.Intent.IdempotencyKey, doc.Intent.IdempotencyKey, "synthetic-private", "unrelated-outer-cache"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("committed event exposed protected source or authority")
		}
	}
}

type heldProjectObservationRead struct {
	context.Context
	checks          atomic.Int32
	entered, resume chan struct{}
}

func (c *heldProjectObservationRead) Err() error {
	if c.checks.Add(1) == 2 {
		close(c.entered)
		<-c.resume
	}
	return c.Context.Err()
}

func TestCoordinatorCommitSurvivesActualObservationAdmissionLoss(t *testing.T) {
	c, _, _, collector := observedProjectFixture(t)
	// Join the real writer's boot boundary, then let it run normally. The held
	// QueryContext below owns the real observation read mutex, not a fake sink.
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil || !resume() {
		t.Fatal("observation writer idle boundary unavailable")
	}
	held := &heldProjectObservationRead{Context: context.Background(), entered: make(chan struct{}), resume: make(chan struct{})}
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(held.resume)
		}
	}
	defer release()
	done := make(chan error, 1)
	go func() { _, err := collector.QueryContext(held, observability.Filter{}, "", 200); done <- err }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("actual observation reader did not acquire its lock")
	}
	req := request(t, "issue.create", "synthetic-observation-drop", authority.IssuePayload{Title: "Committed despite loss", Body: "synthetic-private-loss-body", State: "open"})
	result, err := c.Mutate(context.Background(), req, testActor)
	if err != nil || result.Replayed || result.Validate() != nil {
		t.Fatal("observation contention changed actual canonical commit")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("held observation reader failed after release")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held observation reader did not join")
	}
	if collector.Coverage().Dropped != 1 {
		t.Fatal("actual observation drop was omitted or retried")
	}
	if replay, err := c.Mutate(context.Background(), req, testActor); err != nil || !replay.Replayed || replay.Citation != result.Citation {
		t.Fatal("observation loss changed original replay")
	}
	if read, err := c.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: result.Citation.EntityID, Operation: "get"}); err != nil || len(read.Entities) != 1 {
		t.Fatal("observation loss changed canonical data")
	}
}

func TestCoordinatorObservationUsesCommitProofNotCallerAcknowledgement(t *testing.T) {
	for _, cut := range []string{"source_ack", "document_settled", "document_ack", "source_barrier", "document_barrier", "source_rollback", "document_resume"} {
		t.Run(cut, func(t *testing.T) {
			c, _, _, collector := observedProjectFixture(t)
			resume, err := collector.PauseWriterForQualification(context.Background())
			if err != nil {
				t.Fatal("commit proof fixture idle boundary unavailable")
			}
			defer resume()
			stop := errors.New("synthetic-private-commit-fault")
			document := strings.HasPrefix(cut, "document")
			armed := false
			c.projects[testProject].store.fault = func(phase string) error {
				if phase == "knowledge_document_bound" {
					armed = true
				}
				if cut == "source_ack" && phase == "acknowledgement" || cut == "document_settled" && phase == "knowledge_document_settled" || cut == "document_ack" && phase == "knowledge_document_acknowledgement" || cut == "source_barrier" && phase == "database_sync" || cut == "document_barrier" && armed && phase == "database_sync" || cut == "source_rollback" && phase == "generation" || cut == "document_resume" && phase == "knowledge_document_bound" {
					return stop
				}
				return nil
			}
			issue := request(t, "issue.create", "synthetic-private-proof-source", authority.IssuePayload{Title: "Proof source", Body: "synthetic-private-proof-bytes", State: "open"})
			doc := documentRequest(t, "document.create", "synthetic-private-proof-document", authority.EmptyKnowledgeRef, authority.Citation{}, "# synthetic-private-proof-document-bytes")
			if document {
				_, err = c.MutateDocument(context.Background(), doc, testActor)
			} else {
				_, err = c.Mutate(context.Background(), issue, testActor)
			}
			if !errors.Is(err, stop) {
				t.Fatal("observation changed the original commit fault")
			}
			c.projects[testProject].store.fault = nil
			if cut != "source_rollback" {
				if document {
					replayed, err := c.MutateDocument(context.Background(), doc, testActor)
					if err != nil || !replayed.Receipt.Replayed {
						t.Fatal("committed document fault lost original-key replay")
					}
				} else {
					replayed, err := c.Mutate(context.Background(), issue, testActor)
					if err != nil || !replayed.Replayed {
						t.Fatal("committed source fault lost original-key replay")
					}
				}
			}
			if !resume() || c.Close(context.Background()) != nil || !collector.Close(context.Background(), true) {
				t.Fatal("commit proof observation owners did not join")
			}
			known := observability.ProjectEntityRefs(observability.Context{Project: observability.KnownRef("project", string(testProject))})
			page, err := collector.Query(observability.Filter{ProjectRef: known.Project}, "", 200)
			if err != nil {
				t.Fatal("commit proof evidence unavailable")
			}
			confirmed := cut == "source_ack" || cut == "document_settled" || cut == "document_ack" || cut == "document_resume"
			wantEvents, wantLoss := 0, uint64(0)
			if confirmed {
				wantEvents = 1
			} else if cut != "source_rollback" {
				wantLoss = 1
			}
			if len(page.Events) != wantEvents || page.Coverage.Dropped != wantLoss {
				t.Fatal("commit proof, uncertainty or replay manufactured observations")
			}
			if confirmed && (page.Events[0].Outcome != "committed" || (page.Events[0].Phase == "document_commit") != document) {
				t.Fatal("confirmed proof acquired an unrelated phase")
			}
		})
	}
}
