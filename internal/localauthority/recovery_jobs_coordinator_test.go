package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

// Deferred workers make state transitions deterministic. Public job controls
// are enabled separately; tests explicitly invoke the worker under its turn.
func recoveryCoordinatorFixture(t *testing.T, cut string) (*Coordinator, *PreparedRegistry, string, documentOperation, authority.DocumentMutationRequest) {
	t.Helper()
	s, registry, directory, req, operation := selectedRecoveryFixture(t, "update", cut, 1)
	if s.Close() != nil {
		t.Fatal("original recovery owner did not close")
	}
	prepared, err := registry.PrepareRuntime()
	if err != nil {
		t.Fatal("recovery runtime preparation failed")
	}
	c, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, directory}}, false)
	if err != nil || c.projects[testProject].jobs == nil {
		t.Fatal("deferred recovery coordinator failed")
	}
	c.jobsStarted = true
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if c.Close(ctx) != nil {
			t.Error("recovery coordinator did not join")
		}
		if prepared.Close() != nil {
			t.Error("recovery runtime preparation did not close")
		}
	})
	return c, prepared, directory, operation, req
}

func enqueueRecoveryFixture(t *testing.T, c *Coordinator, o documentOperation) recoveryJobRecord {
	t.Helper()
	a, err := c.EnqueueRecovery(context.Background(), RecoveryJobRequest{testProject, o.ID, o.Generation, "local"}, testActor)
	if err != nil || a.Validate() != nil || a.Outcome != "queued" {
		t.Fatal("explicit recovery admission failed")
	}
	q := c.projects[testProject].jobs
	records, err := q.records(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatal("recovery admission did not retain one original selector")
	}
	return records[0]
}

func recoveryControl(t *testing.T, c *Coordinator, o documentOperation) RecoveryJobView {
	t.Helper()
	v, err := c.RecoveryJob(context.Background(), testProject, o.ID)
	if err != nil || v.Validate() != nil {
		t.Fatal("protected recovery state unavailable")
	}
	return v
}

func TestRecoveryJobCoordinatorOriginalReceiptAndReadOnlyReplay(t *testing.T) {
	c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
	r := enqueueRecoveryFixture(t, c, o)
	p := c.projects[testProject]
	if c.runRecoveryRecord(p, r) != nil {
		t.Fatal("selected recovery job did not finish")
	}
	v := recoveryControl(t, c, o)
	if v.State != "succeeded" || v.Attempts != 1 || v.Reconciled || v.Receipt == nil || v.Receipt.Replayed || v.Receipt.OperationID != o.ID || v.Receipt.IntentHash != o.Intent {
		t.Fatal("job substituted original result or fabricated attempts")
	}
	before := knowledgeFootprint(t, p.store)
	rejectRecoveryWrites(t, p.store)
	a, err := c.EnqueueRecovery(context.Background(), RecoveryJobRequest{testProject, o.ID, 3, "local"}, Actor{"later caller"})
	if err != nil || a.Validate() != nil || a.Outcome != "retained" || !reflect.DeepEqual(v, *a.Job) {
		t.Fatal("retained admission changed original scope/result")
	}
	if c.runRecoveryRecord(p, r) != nil || !reflect.DeepEqual(before, knowledgeFootprint(t, p.store)) {
		t.Fatal("retained success caused source publication")
	}
	page, err := c.RecoveryJobs(context.Background(), RecoveryJobsRequest{ProjectID: testProject, Limit: 1})
	if err != nil || page.Validate() != nil || len(page.Jobs) != 1 || page.NextCursor != "" {
		t.Fatal("bounded protected jobs page unavailable")
	}
	page.Jobs[0].Receipt.IntentHash = "changed"
	if recoveryControl(t, c, o).Receipt.IntentHash != o.Intent {
		t.Fatal("read result exposed mutable internal receipt")
	}
}

func TestRecoveryJobCoordinatorSettledTargetIsProofNotJob(t *testing.T) {
	c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_settled")
	p := c.projects[testProject]
	rejectRecoveryWrites(t, p.store)
	a, err := c.EnqueueRecovery(context.Background(), RecoveryJobRequest{testProject, o.ID, o.Generation, "local"}, testActor)
	if err != nil || a.Validate() != nil || a.Outcome != "already_settled" || a.Job != nil || a.Receipt.OperationID != o.ID {
		t.Fatal("settled target fabricated new execution history")
	}
	records, err := p.jobs.records(context.Background())
	if err != nil || len(records) != 0 {
		t.Fatal("proof-only admission enqueued source work")
	}
}

func TestRecoveryJobCoordinatorCancelledReplayAndOriginalCallerSettlement(t *testing.T) {
	c, _, _, o, req := recoveryCoordinatorFixture(t, "knowledge_document_bound")
	r := enqueueRecoveryFixture(t, c, o)
	p := c.projects[testProject]
	v, err := c.CancelRecovery(context.Background(), RecoveryJobAction{testProject, o.ID, r.View.Generation, "local"}, testActor)
	if err != nil || v.State != "cancelled" || v.Attempts != 0 {
		t.Fatal("queued cancellation did not durably stop work")
	}
	before := knowledgeFootprint(t, p.store)
	a, err := c.EnqueueRecovery(context.Background(), RecoveryJobRequest{testProject, o.ID, o.Generation, "local"}, testActor)
	if err != nil || a.Outcome != "retained" || a.Job.State != "cancelled" || c.runRecoveryRecord(p, r) != nil || !reflect.DeepEqual(before, knowledgeFootprint(t, p.store)) {
		t.Fatal("cancelled admission replay resumed source effects")
	}
	if _, err = c.MutateDocument(context.Background(), req, testActor); err != nil {
		t.Fatal("explicit original caller could not settle its own operation")
	}
	if c.runRecoveryRecord(p, r) != nil {
		t.Fatal("original caller settlement did not reconcile")
	}
	v = recoveryControl(t, c, o)
	if v.State != "succeeded" || !v.Reconciled || v.Attempts != 0 || v.Receipt.OperationID != o.ID {
		t.Fatal("proof reconciliation fabricated worker execution")
	}
}

func TestRecoveryJobWorkerReconcilesLaterCallerWithoutRetry(t *testing.T) {
	c, _, _, o, req := recoveryCoordinatorFixture(t, "knowledge_document_bound")
	r := enqueueRecoveryFixture(t, c, o)
	if _, err := c.CancelRecovery(context.Background(), RecoveryJobAction{testProject, o.ID, r.View.Generation, "local"}, testActor); err != nil {
		t.Fatal("automatic-worker cancelled fixture failed")
	}
	c.jobsStarted = false
	if c.activateRecoveryWorkers() != nil {
		t.Fatal("automatic worker activation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, err := c.MutateDocument(ctx, req, testActor)
		if err == nil {
			break
		}
		if !authorityConflict(err) || ctx.Err() != nil {
			t.Fatal("explicit original caller settlement failed")
		}
		select {
		case <-ctx.Done():
			t.Fatal("original caller turn did not become available")
		case <-time.After(time.Millisecond):
		}
	}
	for {
		v, err := c.RecoveryJob(ctx, testProject, o.ID)
		if err == nil && v.State == "succeeded" {
			if !v.Reconciled || v.Attempts != 0 || v.Receipt == nil || v.Receipt.OperationID != o.ID {
				t.Fatal("automatic reconciliation fabricated source execution")
			}
			break
		}
		if err != nil && !authorityConflict(err) {
			t.Fatal("automatic reconciliation became unavailable")
		}
		select {
		case <-ctx.Done():
			t.Fatal("idle worker missed original caller settlement")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRecoveryJobCoordinatorRunningCancellationDoesNotWaitOnEffectTurn(t *testing.T) {
	c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
	r := enqueueRecoveryFixture(t, c, o)
	p := c.projects[testProject]
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.store.fault = func(at string) error {
		if at == "knowledge_ref_file" {
			once.Do(func() { close(entered) })
			<-resume
			return context.Canceled
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.runRecoveryRecord(p, r) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("source effect checkpoint unavailable")
	}
	active := recoveryControl(t, c, o)
	v, err := c.CancelRecovery(context.Background(), RecoveryJobAction{testProject, o.ID, active.Generation, "local"}, testActor)
	if err != nil || v.State != "cancelling" || v.Attempts != 1 {
		close(resume)
		<-done
		t.Fatal("running cancellation was blocked behind source turn")
	}
	close(resume)
	if err = <-done; err != nil {
		t.Fatal("cancelled source call did not join/classify")
	}
	p.store.fault = nil
	v = recoveryControl(t, c, o)
	if v.State != "cancelled" || v.Attempts != 1 || v.Receipt != nil {
		t.Fatal("pending canonical operation falsely reported success")
	}
	v, err = c.RetryRecovery(context.Background(), RecoveryJobAction{testProject, o.ID, v.Generation, "local"}, testActor)
	if err != nil || v.State != "queued" || v.Attempts != 1 || c.runRecoveryRecord(p, r) != nil {
		t.Fatal("distinct explicit retry could not resume original operation")
	}
	if v = recoveryControl(t, c, o); v.State != "succeeded" || v.Attempts != 2 {
		t.Fatal("retry lost bounded actual execution count")
	}
}

func TestRecoveryJobCoordinatorOldCancelCannotStopNewAttempt(t *testing.T) {
	c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
	r := enqueueRecoveryFixture(t, c, o)
	p := c.projects[testProject]
	oldCtx, oldCancel := context.WithCancel(context.Background())
	defer oldCancel()
	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	if !c.setRecoveryAttempt(p, o.ID, r.View.Generation+1, oldCancel) || !c.setRecoveryAttempt(p, o.ID, r.View.Generation+4, newCancel) {
		t.Fatal("synthetic attempt handle unavailable")
	}
	c.cancelRecoveryAttempt(p, o.ID, r.View.Generation+1)
	c.clearRecoveryAttempt(p, o.ID, r.View.Generation+1)
	if oldCtx.Err() != nil || newCtx.Err() != nil {
		t.Fatal("stale cancellation targeted another generation")
	}
	c.cancelRecoveryAttempt(p, o.ID, r.View.Generation+4)
	if newCtx.Err() != context.Canceled {
		t.Fatal("matching attempt cancellation did not propagate")
	}
	c.clearRecoveryAttempt(p, o.ID, r.View.Generation+4)
}

func TestRecoveryJobCoordinatorCancelSignalsOnlyPublishedIntent(t *testing.T) {
	for _, phase := range []string{"jobs_stage_write", "jobs_stage_file", "jobs_stage_directory", "jobs_state_rename", "jobs_state_directory", "jobs_state_acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
			enqueueRecoveryFixture(t, c, o)
			p := c.projects[testProject]
			r, err := p.jobs.startAttempt(context.Background(), o.ID)
			if err != nil {
				t.Fatal("synthetic running attempt failed")
			}
			effect, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !c.setRecoveryAttempt(p, o.ID, r.View.Generation, cancel) {
				t.Fatal("synthetic active attempt unavailable")
			}
			p.jobs.fault = func(at string) error {
				if at == phase {
					return Error{DurabilityUnavailable}
				}
				return nil
			}
			_, err = c.CancelRecovery(context.Background(), RecoveryJobAction{testProject, o.ID, r.View.Generation, "local"}, testActor)
			if err == nil {
				t.Fatal("cancel publication fault not reached")
			}
			published := phase == "jobs_state_rename" || phase == "jobs_state_directory" || phase == "jobs_state_acknowledgement"
			if (effect.Err() == context.Canceled) != published {
				t.Fatal("cancel signal did not respect final publication boundary")
			}
			if p.jobs.state.Jobs[0].View.State != "running" {
				t.Fatal("unacknowledged cancellation changed admitted memory")
			}
			c.clearRecoveryAttempt(p, o.ID, r.View.Generation)
		})
	}
}

func TestRecoveryJobCoordinatorEffectContentionIsNotAnAttempt(t *testing.T) {
	c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
	r := enqueueRecoveryFixture(t, c, o)
	p := c.projects[testProject]
	_, _, release, err := c.begin(context.Background(), testProject)
	if err != nil {
		t.Fatal("synthetic effect turn unavailable")
	}
	err = c.runRecoveryRecord(p, r)
	var busy recoveryWait
	if !errors.As(err, &busy) || busy.changed == nil || recoveryControl(t, c, o).Attempts != 0 {
		release()
		t.Fatal("turn contention fabricated a recovery attempt")
	}
	select {
	case <-busy.changed:
		release()
		t.Fatal("unrelated metadata read woke busy source turn")
	default:
	}
	release()
	select {
	case <-busy.changed:
	case <-time.After(time.Second):
		t.Fatal("real effect turn release did not wake recovery")
	}
	if c.runRecoveryRecord(p, r) != nil || recoveryControl(t, c, o).Attempts != 1 {
		t.Fatal("authorized recovery failed after real turn release")
	}
}

func TestRecoveryJobCoordinatorRestartPreservesCancellation(t *testing.T) {
	for _, state := range []string{"queued", "running", "cancelling", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			c, prepared, directory, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
			r := enqueueRecoveryFixture(t, c, o)
			p := c.projects[testProject]
			if state == "running" || state == "cancelling" {
				var err error
				r, err = p.jobs.startAttempt(context.Background(), o.ID)
				if err != nil {
					t.Fatal("synthetic pre-crash attempt failed")
				}
			}
			if state == "cancelling" || state == "cancelled" {
				if _, err := c.CancelRecovery(context.Background(), RecoveryJobAction{testProject, o.ID, r.View.Generation, "local"}, testActor); err != nil {
					t.Fatal("pre-restart cancellation failed")
				}
			}
			before := knowledgeFootprint(t, p.store)
			if c.Close(context.Background()) != nil {
				t.Fatal("original coordinator did not quiesce")
			}
			next, err := newStrictCoordinator(context.Background(), prepared, []ProjectBinding{{testProject, directory}}, false)
			if err != nil {
				t.Fatal("verified recovery owner restart failed")
			}
			defer next.Close(context.Background())
			next.jobsStarted = true
			p = next.projects[testProject]
			records, err := p.jobs.records(context.Background())
			if err != nil || len(records) != 1 || records[0].View.State != state {
				t.Fatal("restart erased original durable job state")
			}
			if next.runRecoveryRecord(p, records[0]) != nil {
				t.Fatal("restart recovery classification failed")
			}
			v := recoveryControl(t, next, o)
			if state == "cancelled" || state == "cancelling" {
				if v.State != "cancelled" || !reflect.DeepEqual(before, knowledgeFootprint(t, p.store)) {
					t.Fatal("restart resumed cancelled source effects")
				}
			} else if v.State != "succeeded" || v.Attempts != records[0].View.Attempts+1 {
				t.Fatal("authorized restart lost original receipt/attempt count")
			}
		})
	}
}

func TestRecoveryJobCoordinatorSuccessMetadataFailureIsNotSourceRetry(t *testing.T) {
	for _, phase := range []string{"jobs_stage_write", "jobs_stage_file", "jobs_stage_directory", "jobs_state_rename", "jobs_state_directory", "jobs_state_acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
			r := enqueueRecoveryFixture(t, c, o)
			p := c.projects[testProject]
			// queued -> running is durable; the third snapshot is terminal success.
			p.jobs.fault = func(at string) error {
				if at == phase && p.jobs.state.Generation == 3 {
					return Error{DurabilityUnavailable}
				}
				return nil
			}
			if c.runRecoveryRecord(p, r) == nil || p.jobs.fence != JobsUncertain {
				t.Fatal("terminal metadata ambiguity was acknowledged")
			}
			_, readErr := c.RecoveryJob(context.Background(), testProject, o.ID)
			requireKnowledgeError(t, readErr, JobsUncertain)
			createIssue(t, p.store, "synthetic-tracker-after-terminal-job-failure")
			o2, err := loadDocumentOperation(context.Background(), p.store.db, testProject, o.Key)
			if err != nil || o2.Phase != "settled" || o2.Result.Receipt.OperationID != o.ID {
				t.Fatal("metadata failure lost confirmed source operation")
			}
			before := knowledgeFootprint(t, p.store)
			if p.jobs.close() != nil {
				t.Fatal("uncertain metadata owner did not close")
			}
			q, err := newRecoveryQueue(context.Background(), p.store, nil)
			if err != nil {
				t.Fatal("terminal metadata failed verified reopen")
			}
			p.jobs = q
			rejectRecoveryWrites(t, p.store)
			if c.runRecoveryRecord(p, r) != nil {
				t.Fatal("verified terminal owner could not reconcile source proof")
			}
			v := recoveryControl(t, c, o)
			if v.State != "succeeded" || v.Attempts != 1 || c.runRecoveryRecord(p, r) != nil || !reflect.DeepEqual(before, knowledgeFootprint(t, p.store)) {
				t.Fatal("terminal acknowledgement retry repeated source effect")
			}
		})
	}
}

func TestReleaseMutexBroadcastEpochs(t *testing.T) {
	var m releaseMutex
	m.Lock()
	a, b := m.changed(), m.changed()
	if a != b || m.TryLock() {
		t.Fatal("mutex did not expose current held epoch")
	}
	m.Unlock()
	for _, waiter := range []<-chan struct{}{a, b} {
		select {
		case <-waiter:
		default:
			t.Fatal("release failed to broadcast to every waiter")
		}
	}
	select {
	case <-m.changed():
		t.Fatal("new epoch was already closed")
	default:
	}
}

func TestRecoveryJobsPageCursorPinsGenerationAndDigest(t *testing.T) {
	c, _, _, o, _ := recoveryCoordinatorFixture(t, "knowledge_document_bound")
	r := enqueueRecoveryFixture(t, c, o)
	q := c.projects[testProject].jobs
	// Cursor bytes are built from the same closed protected contract; a valid
	// historical cursor cannot silently continue through a changed snapshot.
	cur := recoveryJobsCursor{testProject, q.state.Generation, q.fileHash, o.ID}
	body, _ := json.Marshal(cur)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	page, err := c.RecoveryJobs(context.Background(), RecoveryJobsRequest{testProject, 1, encoded})
	if err != nil || len(page.Jobs) != 0 || page.Validate() != nil {
		t.Fatal("valid pinned cursor did not resolve deterministically")
	}
	if _, err = c.CancelRecovery(context.Background(), RecoveryJobAction{testProject, o.ID, r.View.Generation, "local"}, testActor); err != nil {
		t.Fatal("cursor invalidation fixture failed")
	}
	_, err = c.RecoveryJobs(context.Background(), RecoveryJobsRequest{testProject, 1, encoded})
	if !authorityConflict(err) {
		t.Fatal("changed generation did not invalidate cursor")
	}
	for _, cursor := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte(`{"untrusted":true}`))} {
		_, err = c.RecoveryJobs(context.Background(), RecoveryJobsRequest{testProject, 1, cursor})
		var typed authority.Error
		if !errors.As(err, &typed) || typed.Code != authority.InvalidContract {
			t.Fatal("untrusted cursor entered protected query")
		}
	}
}

func TestReleaseMutexCannotPublishEpochBeforeRealRelease(t *testing.T) {
	var m releaseMutex
	m.Lock()
	original := m.changed()
	m.unlockWithCheckpoint(func() {
		if m.changed() != original || !m.TryLock() {
			t.Fatal("release published an unwakeable epoch before unlocking")
		}
		// The next holder can acquire in this window. It still observes the
		// old epoch, whose closure is a harmless wake, not a lost release.
	})
	current := m.changed()
	select {
	case <-current:
		t.Fatal("next held epoch was already released")
	default:
	}
	m.Unlock()
	select {
	case <-current:
	default:
		t.Fatal("next holder's release notification was lost")
	}
}
