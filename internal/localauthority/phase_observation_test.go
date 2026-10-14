//go:build darwin || linux

package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

func reopenWithPhaseObserver(t *testing.T, old *Coordinator, prepared *PreparedRegistry, dir string) (*Coordinator, *observability.Collector) {
	t.Helper()
	if old.Close(context.Background()) != nil {
		t.Fatal("phase fixture original owner did not join")
	}
	collector := observability.NewQualificationCollector(observability.Config{Directory: filepath.Join(privateTestDirectory(t), "phase-observations"), ManagedOutput: true})
	ctx := observability.WithObservation(context.Background(), collector, observability.Context{})
	c, err := newStrictCoordinator(ctx, prepared, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("phase fixture observed owner unavailable")
	}
	c.jobsStarted = true
	t.Cleanup(func() {
		if c.Close(context.Background()) != nil || !collector.Close(context.Background(), true) {
			t.Error("phase fixture owners did not join")
		}
	})
	return c, collector
}

func phaseEvidence(t *testing.T, c *Coordinator, collector *observability.Collector, resume func() bool) observability.Page {
	t.Helper()
	if c.Close(context.Background()) != nil || !resume() || !collector.Close(context.Background(), true) {
		t.Fatal("phase proof owners did not join")
	}
	refs := observability.ProjectEntityRefs(observability.Context{Project: observability.KnownRef("project", string(testProject))})
	page, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityPhaseChanged, ProjectRef: refs.Project}, "", 200)
	if err != nil {
		t.Fatal("retained phase evidence unavailable")
	}
	return page
}

func TestLocalPhaseObservesActualIndexRecoveryAndRepairNotReplay(t *testing.T) {
	for _, phase := range []string{"index_job", "knowledge_recovery", "projection_repair"} {
		t.Run(phase, func(t *testing.T) {
			var c *Coordinator
			var collector *observability.Collector
			var run func()
			var dir string
			switch phase {
			case "index_job":
				c, _, dir, collector = observedProjectFixture(t)
				createIssue(t, c.projects[testProject].store, "synthetic-phase-source")
				run = func() {
					req, r := admittedIndexRecord(t, c, 1, "synthetic-phase-index")
					if c.runIndexRecord(c.projects[testProject], r) != nil {
						t.Fatal("actual observed index failed")
					}
					if replay, err := c.EnqueueIndex(context.Background(), req, testActor); err != nil || replay.State != "succeeded" {
						t.Fatal("observed index replay failed")
					}
					if c.runIndexRecord(c.projects[testProject], r) != nil {
						t.Fatal("settled observed index changed execution")
					}
				}
			case "knowledge_recovery":
				old, prepared, directory, operation, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
				dir = directory
				c, collector = reopenWithPhaseObserver(t, old, prepared, dir)
				run = func() {
					r := enqueueRecoveryFixture(t, c, operation)
					if c.runRecoveryRecord(c.projects[testProject], r) != nil {
						t.Fatal("actual observed recovery failed")
					}
					if replay, err := c.EnqueueRecovery(context.Background(), RecoveryJobRequest{testProject, operation.ID, operation.Generation, "local"}, testActor); err != nil || replay.Outcome != "retained" || replay.Job.State != "succeeded" {
						t.Fatal("observed recovery replay failed")
					}
					if c.runRecoveryRecord(c.projects[testProject], r) != nil {
						t.Fatal("settled observed recovery changed execution")
					}
				}
			case "projection_repair":
				old, prepared, directory, req, _, _ := repairApplyFixture(t)
				dir = directory
				c, collector = reopenWithPhaseObserver(t, old, prepared, dir)
				run = func() {
					if view, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err != nil || view.State != "confirmed" || view.Replayed {
						t.Fatal("actual observed repair failed")
					}
					if view, err := c.ApplyProjectionRepair(context.Background(), req, testActor); err != nil || !view.Replayed {
						t.Fatal("observed repair replay failed")
					}
				}
			}
			resume, err := collector.PauseWriterForQualification(context.Background())
			if err != nil {
				t.Fatal("phase fixture actual idle boundary unavailable")
			}
			defer resume()
			run()
			page := phaseEvidence(t, c, collector, resume)
			states := []string{"queued", "running", "succeeded"}
			if phase == "projection_repair" {
				states = []string{"prepared", "quarantined", "confirmed"}
			}
			if len(page.Events) != len(states) || page.Coverage.Dropped != 0 {
				t.Fatal("actual phase transitions were lost or invented")
			}
			for i, event := range page.Events {
				if event.Phase != phase || event.Outcome != states[i] || event.ProjectRef == "" || event.CacheRef != "" || event.RepoRef != "" || event.JobRef != "" {
					t.Fatal("phase evidence changed identity or proof")
				}
			}
			body, _ := json.Marshal(page)
			for _, private := range []string{dir, string(testProject), testActor.ID, "synthetic-phase", "catalog.json", "quarantine-"} {
				if strings.Contains(string(body), private) {
					t.Fatal("phase evidence disclosed protected state")
				}
			}
		})
	}
}

func TestLocalPhaseBufferIsBoundedAndEmitsOnlyAfterTurnRelease(t *testing.T) {
	c, _, _, collector := observedProjectFixture(t)
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("bounded phase idle boundary unavailable")
	}
	defer resume()
	_, ctx, release, err := c.begin(context.Background(), testProject)
	if err != nil {
		t.Fatal("bounded phase source turn unavailable")
	}
	defer release()
	// Hold the actual collector read mutex during capture. An early Emit or
	// overflow-loss report would leave a counted drop before source release.
	held := &heldProjectObservationRead{Context: context.Background(), entered: make(chan struct{}), resume: make(chan struct{})}
	var readOnce sync.Once
	releaseRead := func() { readOnce.Do(func() { close(held.resume) }) }
	defer releaseRead()
	done := make(chan error, 1)
	go func() { _, err := collector.QueryContext(held, observability.Filter{}, "", 200); done <- err }()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("phase control reader did not own actual collector mutex")
	}
	for i := 0; i < maxPhaseObservations+1; i++ {
		capturePhasePublication(ctx, testProject, "index_job", "running").confirm()
	}
	releaseRead()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("phase control reader failed after release")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("phase control reader did not join")
	}
	if collector.Coverage().Dropped != 0 {
		t.Fatal("phase buffer emitted while owning source turn")
	}
	release()
	release() // the same owned turn cannot duplicate the buffered boundaries
	page := phaseEvidence(t, c, collector, resume)
	if len(page.Events) != maxPhaseObservations || page.Coverage.Dropped != 1 {
		t.Fatal("phase buffer exceeded bounds or retried loss")
	}
	// No observer means no extra sink, queue, file or retained context capture.
	plain := &coordinatedProject{store: &Store{manifest: authority.Manifest{ID: testProject}}}
	if _, b := observePhases(context.Background(), plain); b != nil {
		t.Fatal("unobserved owner constructed a phase buffer")
	}
}

func TestLocalPhasePublicationProofSurvivesLostAcknowledgement(t *testing.T) {
	for _, phase := range []string{"index_job", "knowledge_recovery", "projection_repair"} {
		for _, cut := range []string{"before", "rename", "ack"} {
			t.Run(phase+"/"+cut, func(t *testing.T) {
				var c *Coordinator
				var collector *observability.Collector
				var run func() error
				var setFault func(func(string) error)
				prefix, preparedState := "index", "queued"
				armed := phase != "index_job"
				switch phase {
				case "index_job":
					c, _, _, collector = observedProjectFixture(t)
					createIssue(t, c.projects[testProject].store, "synthetic-phase-cut-source")
					setFault = func(f func(string) error) { c.projects[testProject].indexJobs.fault = f }
					run = func() error {
						_, err := c.EnqueueIndex(context.Background(), IndexJobRequest{testProject, 1, "synthetic-phase-cut-index", "local"}, testActor)
						return err
					}
				case "knowledge_recovery":
					old, prepared, dir, operation, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
					c, collector = reopenWithPhaseObserver(t, old, prepared, dir)
					prefix = "jobs"
					setFault = func(f func(string) error) { c.projects[testProject].jobs.fault = f }
					run = func() error {
						_, err := c.EnqueueRecovery(context.Background(), RecoveryJobRequest{testProject, operation.ID, operation.Generation, "local"}, testActor)
						return err
					}
				case "projection_repair":
					old, prepared, dir, req, _, _ := repairApplyFixture(t)
					c, collector = reopenWithPhaseObserver(t, old, prepared, dir)
					prefix, preparedState = "repair", "prepared"
					setFault = func(f func(string) error) { c.projects[testProject].repair.fault = f }
					run = func() error { _, err := c.ApplyProjectionRepair(context.Background(), req, testActor); return err }
				}
				at := prefix + "_state_acknowledgement"
				if cut == "rename" {
					at = prefix + "_state_rename"
				}
				if cut == "before" {
					at = prefix + "_stage_ready"
					if phase == "knowledge_recovery" {
						at = "jobs_stage_directory"
					}
				}
				hit := false
				stop := errors.New("synthetic-private-phase-publication-cut")
				setFault(func(where string) error {
					// Empty initial index-ledger provisioning is not job admission.
					if !armed && where == "index_state_acknowledgement" {
						armed = true
						return nil
					}
					if armed && where == at {
						hit = true
						return stop
					}
					return nil
				})
				resume, err := collector.PauseWriterForQualification(context.Background())
				if err != nil {
					t.Fatal("phase cut idle boundary unavailable")
				}
				defer resume()
				if err := run(); !hit || !errors.Is(err, stop) {
					t.Fatal("phase observation changed the actual publication fault")
				}
				setFault(nil)
				page := phaseEvidence(t, c, collector, resume)
				wantEvents, wantLoss := 0, uint64(0)
				if cut == "ack" {
					wantEvents = 1
				} else if cut == "rename" {
					wantLoss = 1
				}
				if len(page.Events) != wantEvents || page.Coverage.Dropped != wantLoss {
					t.Fatal("phase proof, uncertainty or rollback was invented")
				}
				if wantEvents == 1 && (page.Events[0].Phase != phase || page.Events[0].Outcome != preparedState) {
					t.Fatal("lost phase acknowledgement changed confirmed proof")
				}
			})
		}
	}
}

func TestLocalPhaseJoinedReleaseKeepsCanonicalProofAcrossClose(t *testing.T) {
	c, _, _, collector := observedProjectFixture(t)
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("joined source proof idle boundary unavailable")
	}
	defer resume()
	capture := &canonicalObservation{project: testProject}
	ctx := context.WithValue(context.Background(), canonicalObservationKey{}, capture)
	s, call, release, err := c.begin(ctx, testProject)
	if err != nil {
		t.Fatal("joined source proof turn unavailable")
	}
	defer release()
	req := request(t, "issue.create", "synthetic-joined-phase-source", authority.IssuePayload{Title: "Joined source proof", State: "open"})
	receipt, err := s.Mutate(call, req, testActor)
	if err != nil || receipt.Validate() != nil || capture.state != 2 {
		t.Fatal("actual joined source proof unavailable")
	}
	closed := make(chan error, 1)
	go func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- c.Close(closeCtx)
	}()
	select {
	case <-c.lifetime.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("actual owner close did not cancel admission")
	}
	select {
	case <-closed:
		t.Fatal("owner close bypassed admitted source release")
	default:
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal("joined source owner close failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("joined source owner close did not finish")
	}
	if !resume() || !collector.Close(context.Background(), true) {
		t.Fatal("joined source observations did not seal")
	}
	refs := observability.ProjectEntityRefs(observability.Context{Project: observability.KnownRef("project", string(testProject))})
	page, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityCommitted, ProjectRef: refs.Project}, "", 200)
	if err != nil || len(page.Events) != 1 || page.Events[0].Outcome != "committed" || page.Coverage.Dropped != 0 {
		t.Fatal("owner close lost or invented canonical commit proof")
	}
}

func TestLocalPhaseCaptureRejectsForeignAndNonCatalogInputAndAdmissionCancellation(t *testing.T) {
	c, _, _, collector := observedProjectFixture(t)
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("phase rejection idle boundary unavailable")
	}
	defer resume()
	_, ctx, release, err := c.begin(context.Background(), testProject)
	if err != nil {
		t.Fatal("phase rejection turn unavailable")
	}
	defer release()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	foreign := authority.UUID("22222222-2222-4222-8222-222222222222")
	for _, proof := range []*phasePublication{
		capturePhasePublication(ctx, foreign, "index_job", "running"),
		capturePhasePublication(ctx, testProject, "synthetic-private-phase", "running"),
		capturePhasePublication(ctx, testProject, "index_job", "synthetic-private-outcome"),
		capturePhasePublication(ctx, testProject, "projection_repair", "running"),
	} {
		if proof != nil {
			t.Fatal("phase capture accepted foreign or non-catalog authority")
		}
	}
	if _, err := c.EnqueueIndex(cancelled, IndexJobRequest{testProject, 0, "synthetic-cancelled-phase-admission", "local"}, testActor); !errors.Is(err, context.Canceled) {
		t.Fatal("phase observation changed actual pre-admission cancellation")
	}
	release()
	page := phaseEvidence(t, c, collector, resume)
	if len(page.Events) != 0 || page.Coverage.Dropped != 0 {
		t.Fatal("rejected phase capture invented proof or loss")
	}
}

func TestLocalPhaseCancellationRetryAndLateProofRemainExecutionOwned(t *testing.T) {
	for _, phase := range []string{"index_job", "knowledge_recovery"} {
		t.Run(phase, func(t *testing.T) {
			var c *Coordinator
			var collector *observability.Collector
			var run func()
			if phase == "index_job" {
				c, _, _, collector = observedProjectFixture(t)
				createIssue(t, c.projects[testProject].store, "synthetic-phase-cancel-source")
				run = func() {
					_, r := admittedIndexRecord(t, c, 1, "synthetic-phase-cancel-index")
					action := IndexJobAction{testProject, r.Selection.BuildID, r.Generation, "local"}
					cancelled, err := c.CancelIndex(context.Background(), action, testActor)
					if err != nil || cancelled.State != "cancelled" {
						t.Fatal("observed queued index cancellation failed")
					}
					if _, err := c.CancelIndex(context.Background(), action, testActor); err != nil {
						t.Fatal("observed index cancellation replay failed")
					}
					action.ExpectedGeneration = cancelled.Generation
					if retried, err := c.RetryIndex(context.Background(), action, testActor); err != nil || retried.State != "queued" {
						t.Fatal("observed index retry failed")
					}
					if _, err := c.RetryIndex(context.Background(), action, testActor); err != nil {
						t.Fatal("observed index retry replay failed")
					}
					hit := false
					c.projects[testProject].indexTurnFault = func(at string) error {
						if at == "index_turn_complete" && !hit {
							hit = true
							running := indexControlFixture(t, c, r.Selection.BuildID)
							v, err := c.CancelIndex(context.Background(), IndexJobAction{testProject, r.Selection.BuildID, running.Generation, "local"}, testActor)
							if err != nil || v.State != "cancelling" {
								t.Fatal("late observed index cancellation failed")
							}
						}
						return nil
					}
					if err := c.runIndexRecord(c.projects[testProject], r); err != nil || !hit || indexControlFixture(t, c, r.Selection.BuildID).State != "succeeded" {
						t.Fatal("late cancellation replaced exact index publication proof")
					}
				}
			} else {
				old, prepared, dir, operation, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
				c, collector = reopenWithPhaseObserver(t, old, prepared, dir)
				run = func() {
					r := enqueueRecoveryFixture(t, c, operation)
					action := RecoveryJobAction{testProject, operation.ID, r.View.Generation, "local"}
					cancelled, err := c.CancelRecovery(context.Background(), action, testActor)
					if err != nil || cancelled.State != "cancelled" {
						t.Fatal("observed queued recovery cancellation failed")
					}
					if _, err := c.CancelRecovery(context.Background(), action, testActor); err != nil {
						t.Fatal("observed recovery cancellation replay failed")
					}
					action.ExpectedGeneration = cancelled.Generation
					if retried, err := c.RetryRecovery(context.Background(), action, testActor); err != nil || retried.State != "queued" {
						t.Fatal("observed recovery retry failed")
					}
					if _, err := c.RetryRecovery(context.Background(), action, testActor); err != nil {
						t.Fatal("observed recovery retry replay failed")
					}
					hit := false
					c.projects[testProject].store.fault = func(at string) error {
						if at == "knowledge_document_settled" && !hit {
							hit = true
							running := recoveryControl(t, c, operation)
							v, err := c.CancelRecovery(context.Background(), RecoveryJobAction{testProject, operation.ID, running.Generation, "local"}, testActor)
							if err != nil || v.State != "cancelling" {
								t.Fatal("late observed recovery cancellation failed")
							}
						}
						return nil
					}
					if err := c.runRecoveryRecord(c.projects[testProject], r); err != nil || !hit || recoveryControl(t, c, operation).State != "succeeded" {
						t.Fatal("late cancellation replaced exact canonical source proof")
					}
				}
			}
			resume, err := collector.PauseWriterForQualification(context.Background())
			if err != nil {
				t.Fatal("cancellation phase idle boundary unavailable")
			}
			defer resume()
			run()
			page := phaseEvidence(t, c, collector, resume)
			want := []string{"queued", "cancelled", "queued", "running", "cancelling", "succeeded"}
			if len(page.Events) != len(want) || page.Coverage.Dropped != 0 {
				t.Fatal("phase cancellation/retry replay invented or lost transitions")
			}
			// Recovery keeps the source turn while cancellation independently
			// commits its control ledger. Cancelling can therefore be observed
			// before the worker's buffered running proof. Observation admission
			// order must never be interpreted as durable job generation order.
			counts := map[string]int{}
			for _, outcome := range want {
				counts[outcome]++
			}
			for _, event := range page.Events {
				if event.Phase != phase || counts[event.Outcome] == 0 {
					t.Fatal("phase evidence replaced execution-owned cancellation/proof")
				}
				counts[event.Outcome]--
			}
			for _, remaining := range counts {
				if remaining != 0 {
					t.Fatal("phase evidence lost a confirmed cancellation/proof boundary")
				}
			}
		})
	}
}
