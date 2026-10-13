package localauthority

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func deferredIndexCoordinator(t *testing.T) (*Coordinator, *PreparedRegistry, string) {
	t.Helper()
	prepared, _, dir := preparedFixture(t)
	c, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("deferred index coordinator unavailable")
	}
	c.jobsStarted = true // fixture owns manual joined execution
	t.Cleanup(func() {
		if c.Close(context.Background()) != nil {
			t.Error("index coordinator did not join")
		}
	})
	return c, prepared, dir
}

func admittedIndexRecord(t *testing.T, c *Coordinator, sequence uint64, key string) (IndexJobRequest, indexJobRecord) {
	t.Helper()
	req := IndexJobRequest{ProjectID: testProject, Sequence: sequence, IdempotencyKey: key, WriteMode: "local"}
	v, err := c.EnqueueIndex(context.Background(), req, testActor)
	if err != nil || v.Validate() != nil || v.State != "queued" {
		t.Fatal("explicit index admission failed")
	}
	r, found, err := c.projects[testProject].indexJobs.retained(context.Background(), v.JobID)
	if err != nil || !found {
		t.Fatal("durable index admission selector missing")
	}
	return req, r
}

func indexControlFixture(t *testing.T, c *Coordinator, id authority.UUID) IndexJobView {
	t.Helper()
	v, err := c.IndexJob(context.Background(), testProject, id)
	if err != nil || v.Validate() != nil {
		t.Fatal("protected index view unavailable")
	}
	return v
}

func TestIndexCoordinatorIdempotencyExactPublicationAndRestart(t *testing.T) {
	c, prepared, dir := deferredIndexCoordinator(t)
	ctx := context.Background()
	p := c.projects[testProject]
	created := createIssue(t, p.store, "synthetic-index-canonical")
	req, r := admittedIndexRecord(t, c, 1, "synthetic-index-request")
	if _, err := os.Lstat(filepath.Join(dir, projectionDirectory)); !os.IsNotExist(err) {
		t.Fatal("job admission built a hidden index")
	}
	if err := c.runIndexRecord(p, r); err != nil {
		t.Fatal("owned index execution failed")
	}
	v := indexControlFixture(t, c, r.Selection.BuildID)
	if v.State != "succeeded" || !v.Published || v.Attempts != 1 || v.Sequence != 1 || v.CatalogGeneration != 1 {
		t.Fatal("index fabricated completed source or semantic work")
	}
	proof, found, err := p.projection.indexProof(ctx, r.Selection)
	if err != nil || !found || proof.Entries != 1 {
		t.Fatal("job success lacks exact catalog evidence")
	}
	replay, err := c.EnqueueIndex(ctx, req, Actor{"synthetic later actor"})
	if err != nil || !reflect.DeepEqual(v, replay) {
		t.Fatal("index caller replay changed original intent or result")
	}
	if err := c.runIndexRecord(p, r); err != nil {
		t.Fatal("completed job replay changed execution")
	}
	bad := req
	bad.Sequence++
	if _, err := c.EnqueueIndex(ctx, bad, testActor); !authorityConflict(err) {
		t.Fatal("changed same-key index intent did not conflict")
	}
	read, err := c.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: created.Citation.EntityID, Operation: "get"})
	if err != nil || len(read.Entities) != 1 {
		t.Fatal("derived index changed canonical citation")
	}
	if c.Close(ctx) != nil {
		t.Fatal("completed index owner did not join")
	}
	// Qualification observes retained state before dispatch, rather than racing
	// the restart worker's legitimate bounded metadata admission conflict.
	next, err := newStrictCoordinator(ctx, prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("index owner did not reopen")
	}
	defer next.Close(ctx)
	next.jobsStarted = true
	if !reflect.DeepEqual(v, indexControlFixture(t, next, v.JobID)) {
		t.Fatal("restart lost confirmed index identity")
	}
}

func TestIndexCoordinatorSegmentedTurnsPreserveOriginalSnapshot(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	ctx := context.Background()
	var first authority.MutationReceipt
	for i := 0; i <= projectionStepBudget; i++ {
		r, err := c.Mutate(ctx, request(t, "issue.create", fmt.Sprintf("synthetic-index-row-%d", i), authority.IssuePayload{Title: "Synthetic index row", Body: "original stable content", State: "open"}), testActor)
		if err != nil {
			t.Fatal("canonical segmented fixture mutation failed")
		}
		if i == 0 {
			first = r
		}
	}
	_, r := admittedIndexRecord(t, c, projectionStepBudget+1, "synthetic-index-segmented")
	turns := 0
	p.indexTurnFault = func(phase string) error {
		if phase != "index_turn_complete" {
			t.Fatal("unknown index turn phase")
		}
		turns++
		if turns == 1 {
			_, err := c.Mutate(ctx, request(t, "issue.update", "synthetic-between-index-turns", authority.IssuePayload{Title: "Synthetic index row", Body: "changed between owned turns", State: "open"}, first), testActor)
			if err != nil {
				t.Fatal("index worker retained the source lane between turns")
			}
		}
		return nil
	}
	if err := c.runIndexRecord(p, r); err != nil || turns != 2 {
		t.Fatal("bounded segmented job failed")
	}
	catalog, err := p.projection.view(ctx)
	if err != nil || catalog == nil || catalog.Sequence != projectionStepBudget+1 || len(catalog.Entries) != projectionStepBudget+1 {
		t.Fatal("intervening write silently retargeted fixed intake")
	}
	page, err := p.store.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Kind: authority.Issue, Limit: 100})
	if err != nil || page.Sequence != projectionStepBudget+2 {
		t.Fatal("between-turn source mutation not retained")
	}
	stale := false
	for _, current := range page.Entities {
		if current.Entity.Citation.EntityID != first.Citation.EntityID {
			continue
		}
		for _, old := range catalog.Entries {
			if old.Citation.EntityID == first.Citation.EntityID {
				stale = old.Citation.Revision == 1 && old.Digest != current.ContentDigest
			}
		}
	}
	// UUID ordering can put the changed row beyond this bounded current page.
	if !stale && page.NextCursor != "" {
		page, err = p.store.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Kind: authority.Issue, Limit: 100, Cursor: page.NextCursor})
		if err != nil {
			t.Fatal("segmented comparison continuation failed")
		}
		for _, current := range page.Entities {
			if current.Entity.Citation.EntityID == first.Citation.EntityID {
				for _, old := range catalog.Entries {
					if old.Citation.EntityID == first.Citation.EntityID {
						stale = old.Citation.Revision == 1 && old.Digest != current.ContentDigest
					}
				}
			}
		}
	}
	if !stale {
		t.Fatal("fixed index intake did not preserve original digest/citation")
	}
}

func TestIndexCoordinatorCancelRetryAndPublicationReconciliation(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	ctx := context.Background()
	createIssue(t, p.store, "synthetic-index-cancel-source")
	_, r := admittedIndexRecord(t, c, 1, "synthetic-index-cancel")
	cancelled, err := c.CancelIndex(ctx, IndexJobAction{testProject, r.Selection.BuildID, 1, "local"}, testActor)
	if err != nil || cancelled.State != "cancelled" || cancelled.Attempts != 0 {
		t.Fatal("queued index cancellation invented an attempt")
	}
	if c.runIndexRecord(p, r) != nil {
		t.Fatal("cancelled job refused safe proof-only inspection")
	}
	if catalog, err := p.projection.view(ctx); err != nil || catalog != nil {
		t.Fatal("cancelled queued index published")
	}
	retried, err := c.RetryIndex(ctx, IndexJobAction{testProject, r.Selection.BuildID, cancelled.Generation, "local"}, testActor)
	if err != nil || retried.State != "queued" {
		t.Fatal("explicit index retry failed")
	}
	if c.runIndexRecord(p, r) != nil {
		t.Fatal("original retried selection failed")
	}
	if v := indexControlFixture(t, c, r.Selection.BuildID); v.State != "succeeded" || v.Attempts != 1 {
		t.Fatal("retry fabricated source mutation or duplicate attempt")
	}
	// Exact catalog publication with failed final job acknowledgement is proved
	// on reopen, not redone or guessed from a recent timestamp.
	createIssue(t, p.store, "synthetic-index-reconcile-source")
	_, next := admittedIndexRecord(t, c, 2, "synthetic-index-reconcile")
	p.indexJobs.fault = func(phase string) error {
		if phase == "index_state_acknowledgement" {
			for _, job := range p.indexJobs.state.Jobs {
				if job.Selection.BuildID == next.Selection.BuildID && job.State == "succeeded" {
					return context.Canceled
				}
			}
		}
		return nil
	}
	if err := c.runIndexRecord(p, next); err == nil || p.indexJobs.fence != IndexJobsUncertain {
		t.Fatal("ambiguous terminal intent became acknowledged")
	}
	if err := p.indexJobs.close(); err != nil {
		t.Fatal("uncertain metadata owner did not close")
	}
	q, err := openIndexQueue(ctx, p.store)
	if err != nil {
		t.Fatal("qualified terminal intent reopen failed")
	}
	p.indexJobs = q
	if err := c.runIndexRecord(p, next); err != nil {
		t.Fatal("exact completed catalog did not reconcile")
	}
	v := indexControlFixture(t, c, next.Selection.BuildID)
	if v.State != "succeeded" || v.Attempts != 1 || v.CatalogGeneration != 2 {
		t.Fatal("reconciliation duplicated completed index work")
	}
}

func TestIndexCoordinatorWorkerCancellationAndShutdownJoinKeepWriterOwned(t *testing.T) {
	c, prepared, _, dir := coordinatorFixture(t)
	p := c.projects[testProject]
	ctx := context.Background()
	createIssue(t, p.store, "synthetic-index-worker-source")
	entered, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(unblock) })
	p.projection.fault = func(phase string) error {
		if phase == "projection_stage_write" {
			close(entered)
			<-unblock
		}
		return nil
	}
	req := IndexJobRequest{testProject, 1, "synthetic-index-worker-cancel", "local"}
	view, err := c.EnqueueIndex(ctx, req, testActor)
	if err != nil {
		t.Fatal("actual worker admission failed")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("actual index worker did not reach publication")
	}
	running := indexControlFixture(t, c, view.JobID)
	if running.State != "running" {
		t.Fatal("actual attempt not durably running")
	}
	cancelled, err := c.CancelIndex(ctx, IndexJobAction{testProject, view.JobID, running.Generation, "local"}, testActor)
	if err != nil || cancelled.State != "cancelling" {
		t.Fatal("running cancel control blocked behind source turn")
	}
	closing, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := c.Close(closing); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown claimed a stalled index worker joined")
	}
	if writer, err := prepared.OpenWriter(ctx, dir, testProject); err == nil {
		_ = writer.Close()
		t.Fatal("shutdown released authority under a live worker")
	}
	once.Do(func() { close(unblock) })
	if c.Close(ctx) != nil {
		t.Fatal("index worker did not join after release")
	}
	next, err := newStrictCoordinator(ctx, prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("joined cancelled worker failed owner restart")
	}
	defer next.Close(ctx)
	next.jobsStarted = true
	v := indexControlFixture(t, next, view.JobID)
	if v.State != "cancelled" || v.Attempts != 1 {
		t.Fatal("restart erased cancellation or fabricated retry")
	}
}

func TestIndexCoordinatorActualWorkerWakesOnOwnedProjectionRelease(t *testing.T) {
	c, prepared, dir := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	ctx := context.Background()
	createIssue(t, p.store, "synthetic-index-release-source")
	_, r := admittedIndexRecord(t, c, 1, "synthetic-index-release")
	terminal := make(chan struct{})
	var once sync.Once
	p.indexJobs.fault = func(phase string) error {
		if phase == "index_state_acknowledgement" {
			for _, job := range p.indexJobs.state.Jobs {
				if job.Selection.BuildID == r.Selection.BuildID && job.State == "succeeded" {
					once.Do(func() { close(terminal) })
				}
			}
		}
		return nil
	}
	p.projection.mu.Lock()
	locked := true
	defer func() {
		if locked {
			p.projection.mu.Unlock()
		}
	}()
	turnReleased := p.turn.changed()
	c.mu.Lock()
	c.jobsStarted = false
	c.mu.Unlock()
	if c.activateRecoveryWorkers() != nil {
		t.Fatal("actual index worker dispatch failed")
	}
	select {
	case <-turnReleased:
	case <-time.After(5 * time.Second):
		t.Fatal("contended worker did not release source admission")
	}
	v := indexControlFixture(t, c, r.Selection.BuildID)
	if v.State != "queued" || v.Attempts != 0 {
		t.Fatal("projection contention fabricated an actual attempt")
	}
	p.projection.mu.Unlock()
	locked = false
	select {
	case <-terminal:
	case <-time.After(5 * time.Second):
		t.Fatal("owned projection release did not wake retained work")
	}
	if c.Close(ctx) != nil {
		t.Fatal("released worker did not join")
	}
	next, err := newStrictCoordinator(ctx, prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("released worker result did not reopen")
	}
	defer next.Close(ctx)
	next.jobsStarted = true
	v = indexControlFixture(t, next, r.Selection.BuildID)
	if v.State != "succeeded" || v.Attempts != 1 {
		t.Fatal("release wake duplicated work or lost its result")
	}
}

func TestIndexCoordinatorCancellationDoesNotProbeBusySourceSQL(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	createIssue(t, p.store, "synthetic-index-busy-sql")
	_, r := admittedIndexRecord(t, c, 1, "synthetic-index-cancel-busy-sql")
	p.store.mu.Lock()
	v, err := c.CancelIndex(context.Background(), IndexJobAction{testProject, r.Selection.BuildID, 1, "local"}, testActor)
	p.store.mu.Unlock()
	if err != nil || v.State != "cancelled" || v.Attempts != 0 {
		t.Fatal("index cancellation was coupled to the worker-held source SQL mutex")
	}
}
