package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestIndexRejectedCancellationDoesNotStopAuthorizedWorker(t *testing.T) {
	for _, phase := range []string{"index_stage_write", "index_stage_file", "index_namespace_root", "index_state_rename", "index_state_directory"} {
		t.Run(phase, func(t *testing.T) {
			c, prepared, _, dir := coordinatorFixture(t)
			p := c.projects[testProject]
			ctx := context.Background()
			createIssue(t, p.store, "synthetic-rejected-index-cancel")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			p.projection.fault = func(at string) error {
				if at == "projection_stage_write" {
					close(entered)
					<-release
				}
				return nil
			}
			v, err := c.EnqueueIndex(ctx, IndexJobRequest{testProject, 1, "synthetic-rejected-cancel-key", "local"}, testActor)
			if err != nil {
				t.Fatal("actual cancel-authority fixture admission failed")
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("actual worker did not reach held publication")
			}
			running := indexControlFixture(t, c, v.JobID)
			q := p.indexJobs
			if phase == "index_stage_write" || phase == "index_state_rename" {
				q.fault = func(at string) error {
					if at == phase {
						return context.Canceled
					}
					return nil
				}
			} else {
				q.barrierFault = func(at string) error {
					if at == phase {
						return Error{DurabilityUnavailable}
					}
					return nil
				}
			}
			_, err = c.CancelIndex(ctx, IndexJobAction{testProject, v.JobID, running.Generation, "local"}, testActor)
			if err == nil {
				t.Fatal("cancel publication failure was acknowledged")
			}
			ambiguous := phase == "index_state_rename" || phase == "index_state_directory"
			if !ambiguous {
				retained := indexControlFixture(t, c, v.JobID)
				if !reflect.DeepEqual(retained, running) {
					t.Fatal("rejected cancel changed retained running authorization")
				}
			}
			q.barrierFault = nil
			terminal := make(chan struct{})
			var terminalOnce sync.Once
			q.fault = func(at string) error {
				if at == "index_state_acknowledgement" {
					for _, job := range q.state.Jobs {
						if job.Selection.BuildID == v.JobID && job.State == "succeeded" {
							terminalOnce.Do(func() { close(terminal) })
						}
					}
				}
				return nil
			}
			once.Do(func() { close(release) })
			if !ambiguous {
				select {
				case <-terminal:
				case <-time.After(5 * time.Second):
					t.Fatal("rejected cancel stopped otherwise-authorized worker")
				}
			}
			if c.Close(ctx) != nil {
				t.Fatal("cancel authority worker did not join")
			}
			next, err := newStrictCoordinator(ctx, prepared, []ProjectBinding{{testProject, dir}}, false)
			if err != nil {
				t.Fatal("cancel authority result did not reopen")
			}
			defer next.Close(ctx)
			next.jobsStarted = true
			r, found, err := next.projects[testProject].indexJobs.retained(ctx, v.JobID)
			if err != nil || !found {
				t.Fatal("original cancellation intent missing after reopen")
			}
			if ambiguous {
				if r.State != "cancelling" || !r.CancellationRequested {
					t.Fatal("ambiguous cancel lost durable cancellation fence")
				}
				if next.runIndexRecord(next.projects[testProject], r) != nil {
					t.Fatal("ambiguous cancellation did not reconcile without index publication")
				}
			}
			result := indexControlFixture(t, next, v.JobID)
			want := "succeeded"
			if ambiguous {
				want = "cancelled"
			}
			if result.State != want || result.Attempts != 1 {
				t.Fatal("cancel authority result contradicted actual publication boundary")
			}
		})
	}
}

func TestIndexTerminalPreRenameFailureFencesAndReconcilesExactCatalog(t *testing.T) {
	for _, phase := range []string{"index_stage_write", "index_stage_file", "index_stage_directory", "index_namespace_root"} {
		t.Run(phase, func(t *testing.T) {
			c, _, _ := deferredIndexCoordinator(t)
			p := c.projects[testProject]
			ctx := context.Background()
			createIssue(t, p.store, "synthetic-index-terminal-pre-rename")
			_, r := admittedIndexRecord(t, c, 1, "synthetic-terminal-pre-rename-key")
			failure := func(at string) error {
				if at == phase && p.projection.catalog != nil && p.projection.catalog.BuildID == r.Selection.BuildID {
					return Error{DurabilityUnavailable}
				}
				return nil
			}
			if phase == "index_stage_write" {
				p.indexJobs.fault = failure
			} else {
				p.indexJobs.barrierFault = failure
			}
			if err := c.runIndexRecord(p, r); err == nil {
				t.Fatal("terminal metadata failure was acknowledged")
			}
			if p.indexJobs.fence != IndexJobsUncertain && !p.indexJobs.terminalUncertain.Load() {
				t.Fatal("completed catalog left a healthy-looking abandoned job")
			}
			if _, err := c.IndexJob(ctx, testProject, r.Selection.BuildID); err == nil {
				t.Fatal("uncertain terminal job remained readable as running")
			}
			proof, found, err := p.projection.indexProof(ctx, r.Selection)
			if err != nil || !found || proof.Entries != 1 {
				t.Fatal("terminal ledger failure changed completed catalog")
			}
			if p.indexJobs.close() != nil {
				t.Fatal("uncertain terminal ledger did not close")
			}
			q, err := openIndexQueue(ctx, p.store)
			if err != nil {
				t.Fatal("terminal intent did not qualify on reopen")
			}
			p.indexJobs = q
			if c.runIndexRecord(p, r) != nil {
				t.Fatal("exact catalog failed proof-only reconciliation")
			}
			v := indexControlFixture(t, c, r.Selection.BuildID)
			if v.State != "succeeded" || v.Attempts != 1 || v.CatalogGeneration != 1 {
				t.Fatal("terminal reconciliation retried completed build")
			}
		})
	}
}

func TestIndexRetryWithAnotherActiveJobIsConflictNotCorruption(t *testing.T) {
	c, _, dir := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	ctx := context.Background()
	createIssue(t, p.store, "synthetic-index-active-conflict")
	_, a := admittedIndexRecord(t, c, 1, "synthetic-index-a")
	va, err := c.CancelIndex(ctx, IndexJobAction{testProject, a.Selection.BuildID, 1, "local"}, testActor)
	if err != nil {
		t.Fatal("inactive index fixture cancellation failed")
	}
	_, b := admittedIndexRecord(t, c, 1, "synthetic-index-b")
	before := cloneIndexSnapshot(p.indexJobs.state)
	file := filepath.Join(dir, indexJobsDirectory, "state.json")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("retained active-conflict bytes unavailable")
	}
	_, err = c.RetryIndex(ctx, IndexJobAction{testProject, a.Selection.BuildID, va.Generation, "local"}, testActor)
	if !authorityConflict(err) || p.indexJobs.fence != "" || !reflect.DeepEqual(before, p.indexJobs.state) {
		t.Fatal("normal active-job conflict was reported as corruption or changed state")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(body) != string(after) || indexControlFixture(t, c, b.Selection.BuildID).State != "queued" {
		t.Fatal("rejected retry changed the retained active job")
	}
}

func TestIndexCompletedBuilderWaitsForFinalPublicationLock(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	p := c.projects[testProject]
	for i := 0; i <= projectionStepBudget; i++ {
		createIssue(t, p.store, fmt.Sprintf("synthetic-final-publish-%d", i))
	}
	_, r := admittedIndexRecord(t, c, projectionStepBudget+1, "synthetic-final-publication-lock")
	turns := 0
	locked := false
	defer func() {
		if locked {
			p.projection.mu.Unlock()
		}
	}()
	p.indexTurnFault = func(phase string) error {
		turns++
		if turns == 1 {
			p.projection.mu.Lock()
			locked = true
		} else if turns == 2 {
			p.projection.mu.Unlock()
			locked = false
		}
		return nil
	}
	if c.runIndexRecord(p, r) != nil || turns != 3 {
		t.Fatal("ordinary catalog contention failed or rebuilt the completed candidate")
	}
	v := indexControlFixture(t, c, r.Selection.BuildID)
	if v.State != "succeeded" || v.Attempts != 1 || v.Sequence != projectionStepBudget+1 {
		t.Fatal("final publication wait fabricated another attempt or lost work")
	}
}

func TestIndexMetadataGuardsDuringCanonicalIdentityRevalidation(t *testing.T) {
	q, s, _ := indexQueueFixture(t)
	indexQueueEmpty(t, q)
	ctx := context.Background()
	manifest, instance, schema := manifestHash(s.manifest), s.instance, s.schema
	start, done := make(chan struct{}), make(chan error, 1)
	go func() {
		<-start
		for i := 0; i < 128; i++ {
			if err := q.verifyExpected(); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	for i := 0; i < 64; i++ {
		s.mu.Lock()
		err := s.verifyIdentity(ctx, testProject)
		s.mu.Unlock()
		if err != nil {
			t.Fatal("canonical immutable identity revalidation failed")
		}
	}
	if err := <-done; err != nil || manifestHash(s.manifest) != manifest || s.instance != instance || s.schema != schema {
		t.Fatal("metadata guard or canonical revalidation changed admitted identity")
	}
}

func TestIndexIdentityRevalidationCannotReplaceAdmittedManifest(t *testing.T) {
	_, s, _ := indexQueueFixture(t)
	original := manifestHash(s.manifest)
	changed := s.manifest
	changed.Network.ControlTransport = "loopback_admin"
	body, err := json.Marshal(changed)
	if err != nil || changed.Validate() != nil {
		t.Fatal("synthetic changed manifest control invalid")
	}
	s.mu.Lock()
	// Synthetic corruption bypasses then restores the existing SQL guard so
	// revalidation sees a valid schema with different identity bytes. Normal
	// product writes cannot perform this immutable-manifest update.
	var trigger string
	err = s.db.QueryRowContext(context.Background(), "SELECT sql FROM sqlite_master WHERE type='trigger' AND name='immutable_identity'").Scan(&trigger)
	if err == nil {
		_, err = s.db.ExecContext(context.Background(), "DROP TRIGGER immutable_identity")
	}
	if err == nil {
		_, err = s.db.ExecContext(context.Background(), "UPDATE project SET manifest=? WHERE singleton=1", string(body))
	}
	if err == nil {
		_, err = s.db.ExecContext(context.Background(), trigger)
	}
	if err != nil {
		s.mu.Unlock()
		t.Fatal("synthetic restored-schema manifest corruption control failed")
	}
	err = s.verifyIdentity(context.Background(), testProject)
	s.mu.Unlock()
	requireProjectionCode(t, err, Corrupt)
	if manifestHash(s.manifest) != original || s.manifest.Network.ControlTransport != "ipc_only" {
		t.Fatal("source revalidation relaxed the immutable admitted runtime policy")
	}
}

func TestIndexJoinedTerminalDeadlineSealsWithoutWaitingForHeldLocks(t *testing.T) {
	for _, held := range []string{"ledger", "projection"} {
		t.Run(held, func(t *testing.T) {
			c, _, _ := deferredIndexCoordinator(t)
			p := c.projects[testProject]
			createIssue(t, p.store, "synthetic-terminal-proof-deadline")
			_, r := admittedIndexRecord(t, c, 1, "synthetic-terminal-proof-deadline-key")
			p.indexProofBudget = 20 * time.Millisecond
			locked := false
			defer func() {
				if locked {
					if held == "ledger" {
						p.indexJobs.mu.Unlock()
					} else {
						p.projection.mu.Unlock()
					}
				}
			}()
			published := make(chan struct{})
			p.indexTurnFault = func(phase string) error {
				if held == "ledger" {
					p.indexJobs.mu.Lock()
				} else {
					p.projection.mu.Lock()
				}
				locked = true
				close(published)
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- c.runIndexRecord(p, r) }()
			select {
			case <-published:
			case <-time.After(5 * time.Second):
				t.Fatal("actual catalog did not reach joined proof boundary")
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("held terminal proof did not preserve its deadline")
				}
			case <-time.After(time.Second):
				t.Fatal("terminal uncertainty waited past its bounded proof deadline")
			}
			if !p.indexJobs.terminalUncertain.Load() {
				t.Fatal("joined terminal timeout left running controls healthy")
			}
			_, err := c.IndexJob(context.Background(), testProject, r.Selection.BuildID)
			requireProjectionCode(t, err, IndexJobsUncertain)
			if held == "ledger" {
				p.indexJobs.mu.Unlock()
			} else {
				p.projection.mu.Unlock()
			}
			locked = false
			proof, found, err := p.projection.indexProof(context.Background(), r.Selection)
			status, e := p.store.Status(context.Background())
			if err != nil || e != nil || !found || proof.Entries != 1 || status.AuthorityGeneration != 1 || status.Readiness != "ready" {
				t.Fatal("terminal deadline damaged completed catalog or canonical source")
			}
			if p.indexJobs.close() != nil {
				t.Fatal("sealed ledger did not close")
			}
			q, err := openIndexQueue(context.Background(), p.store)
			if err != nil {
				t.Fatal("sealed ledger did not qualify on owner reopen")
			}
			p.indexJobs, p.indexTurnFault = q, nil
			if c.runIndexRecord(p, r) != nil {
				t.Fatal("deadline outcome failed exact-catalog reconciliation")
			}
			v := indexControlFixture(t, c, r.Selection.BuildID)
			if v.State != "succeeded" || v.Attempts != 1 {
				t.Fatal("deadline recovery duplicated published index work")
			}
		})
	}
}
