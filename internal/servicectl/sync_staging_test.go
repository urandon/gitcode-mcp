package servicectl

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSyncStageEnvelope() SyncStageEnvelope {
	return SyncStageEnvelope{
		JobID: "job-1", CacheUUID: "cache-uuid", CacheSchema: 19, CachePath: "/private/cache.db", RegistrationID: "registration-1",
		RepoID: "owner/repo", BindingFingerprint: "sha256:test-binding", Collection: "issues", IdempotencyKey: "sync-1",
		RecordCount: 2, Payload: json.RawMessage(`{"items":[{"body":"private source body"}]}`),
	}
}

func TestSyncStageJournalPersistsChecksummedPrivateEnvelope(t *testing.T) {
	runtimeDir := t.TempDir()
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{})
	created, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !validStageID(created.StageID) || created.Checksum == "" || created.ByteCount != int64(len(created.Payload)) {
		t.Fatalf("created stage = %+v", created)
	}
	path := filepath.Join(runtimeDir, "sync-stages", created.StageID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
	restarted := NewSyncStageJournal(runtimeDir, SyncStageLimits{})
	loaded, err := restarted.Load(created.StageID)
	if err != nil {
		t.Fatalf("Load after restart: %v", err)
	}
	if string(loaded.Payload) != string(created.Payload) || loaded.State.Phase != SyncStageStaged {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestSyncStageJournalRejectsCorruptionAndTraversal(t *testing.T) {
	runtimeDir := t.TempDir()
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{})
	created, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := filepath.Join(runtimeDir, "sync-stages", created.StageID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	data = []byte(strings.Replace(string(data), "private source body", "tampered source body", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := journal.Load(created.StageID); !errors.Is(err, ErrSyncStageCorrupt) {
		t.Fatalf("Load corruption error = %v", err)
	}
	if _, err := journal.Load("../jobs"); !errors.Is(err, ErrSyncStageCorrupt) {
		t.Fatalf("Load traversal error = %v", err)
	}
}

func TestSyncStageRecoveryQuarantinesCorruptionWithoutBlockingValidStage(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	runtimeDir := t.TempDir()
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{MaxAge: time.Hour})
	journal.now = func() time.Time { return now }
	corrupt, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	validEnvelope := testSyncStageEnvelope()
	validEnvelope.IdempotencyKey = "sync-valid"
	valid, err := journal.Create(validEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(runtimeDir, "sync-stages", corrupt.StageID+".json")
	data, err := os.ReadFile(corruptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte(strings.Replace(string(data), "private source body", "tampered source body", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	stages, rejections, err := journal.ListForRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 || stages[0].StageID != valid.StageID {
		t.Fatalf("recoverable stages=%+v", stages)
	}
	if len(rejections) != 1 || rejections[0].StageRef != publicStageRef(corrupt.StageID) || rejections[0].Reason != "corrupt_stage" {
		t.Fatalf("rejections=%+v", rejections)
	}
	quarantined := filepath.Join(runtimeDir, "sync-stages", corrupt.StageID+".rejected")
	if info, err := os.Stat(quarantined); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("quarantined info=%+v err=%v", info, err)
	}
	if _, err := os.Stat(corruptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt active sidecar still exists: %v", err)
	}
	if err := os.Chtimes(quarantined, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, err := journal.GC()
	if err != nil || removed != 1 {
		t.Fatalf("GC removed=%d err=%v", removed, err)
	}
	if _, err := journal.Load(valid.StageID); err != nil {
		t.Fatalf("valid nonterminal stage removed: %v", err)
	}
}

func TestSyncStageJournalEnforcesBoundsBeforePersistence(t *testing.T) {
	runtimeDir := t.TempDir()
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{MaxBytes: 8, MaxRecords: 1, MaxAge: time.Hour})
	envelope := testSyncStageEnvelope()
	if _, err := journal.Create(envelope); !errors.Is(err, ErrSyncStageBound) {
		t.Fatalf("Create error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "sync-stages")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage directory should not exist, stat error = %v", err)
	}
}

func TestSyncStageJournalEnforcesAggregateBudgetAndIdempotentReplay(t *testing.T) {
	runtimeDir := t.TempDir()
	envelope := testSyncStageEnvelope()
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{
		MaxBytes: int64(len(envelope.Payload)) + 1, MaxRecords: 10,
		MaxTotalBytes: int64(len(envelope.Payload)) + 1, MaxTotalRecords: 2, MaxStages: 1,
	})
	first, err := journal.Create(envelope)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := journal.Create(envelope)
	if err != nil || replayed.StageID != first.StageID {
		t.Fatalf("idempotent replay=%+v err=%v", replayed, err)
	}
	second := envelope
	second.IdempotencyKey = "sync-2"
	if _, err := journal.Create(second); !errors.Is(err, ErrSyncStageBound) {
		t.Fatalf("aggregate bound error=%v", err)
	}
	stages, err := journal.List()
	if err != nil || len(stages) != 1 || stages[0].StageID != first.StageID {
		t.Fatalf("stages=%+v err=%v", stages, err)
	}
}

func TestSyncStageCreateRunsTerminalGCBeforeCapacityCheck(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	envelope := testSyncStageEnvelope()
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{
		MaxAge: time.Hour, MaxStages: 10,
		MaxTotalBytes:   int64(len(envelope.Payload)) + 1,
		MaxTotalRecords: envelope.RecordCount,
	})
	journal.now = func() time.Time { return now }
	first, err := journal.Create(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.UpdateState(first.StageID, SyncStageState{Phase: SyncStageCommitted, FetchedAt: now, StagedAt: now, CommittedAt: now}); err != nil {
		t.Fatal(err)
	}
	second := testSyncStageEnvelope()
	second.IdempotencyKey = "after-production-gc"
	created, err := journal.Create(second)
	if err != nil {
		t.Fatalf("Create after production GC: %v", err)
	}
	if _, err := journal.Load(first.StageID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed terminal stage retained in active capacity: %v", err)
	}
	if _, err := journal.Load(created.StageID); err != nil {
		t.Fatalf("new stage missing: %v", err)
	}
}

func TestSyncStageJournalKeepsOnlyLatestWorkflowCheckpointUntilTerminalSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{MaxAge: time.Hour})
	journal.now = func() time.Time { return now }
	firstEnvelope := testSyncStageEnvelope()
	firstEnvelope.Workflow = &SyncStageWorkflow{Collections: []string{"issues", "wiki"}, Current: 0, ProviderMode: "fixture"}
	first, err := journal.Create(firstEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	firstState := first.State
	firstState.Phase, firstState.CommittedAt = SyncStageCommitted, now
	if first, err = journal.UpdateState(first.StageID, firstState); err != nil {
		t.Fatal(err)
	}
	if removed, err := journal.GC(); err != nil || removed != 0 {
		t.Fatalf("latest committed workflow checkpoint removed=%d err=%v", removed, err)
	}

	secondEnvelope := testSyncStageEnvelope()
	secondEnvelope.Collection = "wiki"
	secondEnvelope.IdempotencyKey = "sync-wiki"
	secondEnvelope.Workflow = &SyncStageWorkflow{Collections: []string{"issues", "wiki"}, Current: 1, ProviderMode: "fixture"}
	second, err := journal.Create(secondEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := journal.GC(); err != nil || removed != 1 {
		t.Fatalf("committed predecessor GC removed=%d err=%v", removed, err)
	}
	if _, err := journal.Load(first.StageID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed predecessor retained after successor became durable: %v", err)
	}
	secondState := second.State
	secondState.Phase, secondState.CommittedAt = SyncStageCommitted, now
	if _, err := journal.UpdateState(second.StageID, secondState); err != nil {
		t.Fatal(err)
	}
	if err := journal.RemoveJobStages(second.JobID); err != nil {
		t.Fatal(err)
	}
	if stages, err := journal.List(); err != nil || len(stages) != 0 {
		t.Fatalf("terminal workflow snapshot cleanup stages=%+v err=%v", stages, err)
	}
}

func TestSyncStageRetainedTerminalEvidenceConsumesAggregateBudget(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{MaxAge: time.Hour, MaxStages: 1})
	journal.now = func() time.Time { return now }
	first, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.UpdateState(first.StageID, SyncStageState{Phase: SyncStageRejected, FetchedAt: now, StagedAt: now, TerminalReason: "test"}); err != nil {
		t.Fatal(err)
	}
	second := testSyncStageEnvelope()
	second.IdempotencyKey = "retained-terminal-budget"
	if _, err := journal.Create(second); !errors.Is(err, ErrSyncStageBound) {
		t.Fatalf("retained rejected stage bypassed aggregate budget: %v", err)
	}
	journal.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := journal.Create(second); err != nil {
		t.Fatalf("expired rejected stage did not release aggregate budget: %v", err)
	}
}

func TestSyncStageRetainedCommittedWorkflowCheckpointConsumesAggregateBudget(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	envelope := testSyncStageEnvelope()
	envelope.Workflow = &SyncStageWorkflow{Collections: []string{"issues", "wiki"}, Current: 0, ProviderMode: "fixture"}
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{
		MaxAge: time.Hour, MaxStages: 1,
		MaxTotalBytes: int64(len(envelope.Payload)) + 1, MaxTotalRecords: envelope.RecordCount,
	})
	journal.now = func() time.Time { return now }
	first, err := journal.Create(envelope)
	if err != nil {
		t.Fatal(err)
	}
	state := first.State
	state.Phase, state.CommittedAt = SyncStageCommitted, now
	if _, err := journal.UpdateState(first.StageID, state); err != nil {
		t.Fatal(err)
	}
	second := testSyncStageEnvelope()
	second.JobID, second.IdempotencyKey = "job-2", "retained-committed-budget"
	if _, err := journal.Create(second); !errors.Is(err, ErrSyncStageBound) {
		t.Fatalf("retained committed workflow checkpoint bypassed aggregate budget: %v", err)
	}
	journal.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := journal.Create(second); err != nil {
		t.Fatalf("expired workflow checkpoint did not release aggregate budget: %v", err)
	}
}

func TestSyncStageStateRetryMutationPreservesChecksumAndPayload(t *testing.T) {
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{})
	created, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	retryAt := time.Now().UTC().Add(time.Minute)
	updated, err := journal.UpdateState(created.StageID, SyncStageState{
		Phase: SyncStageWaitingCommit, Attempt: 1, RetryBudget: 4,
		RetryAfter: retryAt, BlockerClass: "cache_busy", BlockingOp: "rag-index",
		FetchedAt: created.State.FetchedAt, StagedAt: created.State.StagedAt,
	})
	if err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if updated.Checksum != created.Checksum || string(updated.Payload) != string(created.Payload) || updated.State.Phase != SyncStageWaitingCommit {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestSyncStagePublicViewCannotExposePayloadOrFilesystemState(t *testing.T) {
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{})
	created, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, err := json.Marshal(created.PublicView())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	public := string(data)
	for _, forbidden := range []string{"private source body", "payload", "checksum", "idempotency", "sync-stages", "cache.db", "job_id", string(os.PathSeparator) + "tmp"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("public view contains %q: %s", forbidden, public)
		}
	}
	for _, required := range []string{`"stage_ref":"stage-`, `"cache_ref":"cache-`, `"phase":"staged"`, `"fetched":2`, `"staged":2`} {
		if !strings.Contains(public, required) {
			t.Fatalf("public view missing %q: %s", required, public)
		}
	}
}

func TestAdminJobSyncStageProjectionIsSemanticAndPublicSafe(t *testing.T) {
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{})
	created, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	state, retry := nextSyncCommitRetry(created.StageID, created.State, time.Now().UTC())
	if !retry {
		t.Fatal("expected retry")
	}
	created.State = state
	public := created.PublicView()
	job := Job{ID: "job-1", Type: SyncJobType, CacheUUID: created.CacheUUID, RepoID: created.RepoID, Status: JobStatusRunning, CreatedAt: created.CreatedAt, UpdatedAt: state.UpdatedAt, SyncStage: &public}
	data, err := json.Marshal(adminJobObservation(job))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	contract := string(data)
	for _, want := range []string{`"sync_stage":{`, `"phase":"waiting_commit"`, `"fetched":2`, `"staged":2`, `"attempt":1`, `"retry_budget":6`, `"blocker_class":"cache_busy"`} {
		if !strings.Contains(contract, want) {
			t.Fatalf("contract missing %q: %s", want, contract)
		}
	}
	for _, forbidden := range []string{"private source body", "payload", "checksum", "idempotency", "sync-stages"} {
		if strings.Contains(contract, forbidden) {
			t.Fatalf("contract contains %q: %s", forbidden, contract)
		}
	}
}

func TestSyncStageGCOnlyRemovesExpiredTerminalStages(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{MaxAge: time.Hour})
	journal.now = func() time.Time { return now }
	live, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatalf("Create live: %v", err)
	}
	terminalEnvelope := testSyncStageEnvelope()
	terminalEnvelope.IdempotencyKey = "sync-terminal"
	terminal, err := journal.Create(terminalEnvelope)
	if err != nil {
		t.Fatalf("Create terminal: %v", err)
	}
	if _, err := journal.UpdateState(live.StageID, SyncStageState{Phase: SyncStageWaitingCommit, FetchedAt: now, StagedAt: now}); err != nil {
		t.Fatalf("Update live: %v", err)
	}
	if _, err := journal.UpdateState(terminal.StageID, SyncStageState{Phase: SyncStageCommitted, FetchedAt: now, StagedAt: now, CommittedAt: now}); err != nil {
		t.Fatalf("Update terminal: %v", err)
	}
	journal.now = func() time.Time { return now.Add(2 * time.Hour) }
	removed, err := journal.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := journal.Load(live.StageID); err != nil {
		t.Fatalf("live stage removed: %v", err)
	}
	if _, err := journal.Load(terminal.StageID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal stage still present: %v", err)
	}
}

func TestSyncCommitRetryIsDeterministicCappedAndBudgeted(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	state := SyncStageState{Phase: SyncStageStaged, RetryBudget: 4}
	previousDelay := time.Duration(0)
	for attempt := 1; attempt <= 4; attempt++ {
		first, retry := nextSyncCommitRetry("stage-00112233445566778899aabb", state, now)
		second, secondRetry := nextSyncCommitRetry("stage-00112233445566778899aabb", state, now)
		if !retry || !secondRetry || !first.RetryAfter.Equal(second.RetryAfter) {
			t.Fatalf("attempt %d retry is not deterministic: %+v %+v", attempt, first, second)
		}
		delay := first.RetryAfter.Sub(now)
		if delay < previousDelay || delay > defaultSyncCommitMaxDelay+defaultSyncCommitMaxDelay/4 {
			t.Fatalf("attempt %d delay = %s, previous = %s", attempt, delay, previousDelay)
		}
		previousDelay = delay
		state = first
	}
	exhausted, retry := nextSyncCommitRetry("stage-00112233445566778899aabb", state, now)
	if retry || exhausted.Phase != SyncStageRejected || exhausted.TerminalReason != "commit_retry_budget_exhausted" || !exhausted.RetryAfter.IsZero() {
		t.Fatalf("exhausted = %+v retry=%t", exhausted, retry)
	}
}

func TestCancelledStageRetainsPayloadAndBecomesOneTerminalEnvelope(t *testing.T) {
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{})
	created, err := journal.Create(testSyncStageEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	rejected := rejectCancelledSyncStage(journal, created)
	if rejected.State.Phase != SyncStageRejected || rejected.State.TerminalReason != "cancelled" || !rejected.State.RetryAfter.IsZero() {
		t.Fatalf("cancelled stage=%+v", rejected.State)
	}
	loaded, err := journal.Load(created.StageID)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Payload) != string(created.Payload) || loaded.Checksum != created.Checksum {
		t.Fatal("cancellation mutated the retained staged response")
	}
	stages, err := journal.List()
	if err != nil || len(stages) != 1 || stages[0].State.TerminalReason != "cancelled" {
		t.Fatalf("terminal stages=%+v err=%v", stages, err)
	}
}

func TestSyncStageJournalRemoveCollectionStagesKeepsSiblingRecoveryAuthority(t *testing.T) {
	root := t.TempDir()
	journal := NewSyncStageJournal(root, SyncStageLimits{})
	base := SyncStageEnvelope{
		JobID: "job-000001", CacheUUID: "cache-a", CacheSchema: 1, CachePath: filepath.Join(root, "cache.db"),
		RegistrationID: "reg-a", RepoID: "owner/repo", BindingFingerprint: "binding-a", Checkpoint: "complete",
		RecordCount: 1, Payload: json.RawMessage(`{}`), State: SyncStageState{Phase: SyncStageCommitted},
	}
	issues := base
	issues.Collection, issues.IdempotencyKey = "issues", "issues-commit"
	if _, err := journal.Create(issues); err != nil {
		t.Fatal(err)
	}
	wiki := base
	wiki.Collection, wiki.IdempotencyKey = "wiki", "wiki-commit"
	if _, err := journal.Create(wiki); err != nil {
		t.Fatal(err)
	}
	if err := journal.RemoveCollectionStages(base.JobID, "issues"); err != nil {
		t.Fatal(err)
	}
	stages, err := journal.List()
	if err != nil || len(stages) != 1 || stages[0].Collection != "wiki" {
		t.Fatalf("stages=%+v err=%v", stages, err)
	}
}
