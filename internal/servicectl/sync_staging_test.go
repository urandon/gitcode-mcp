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
		CacheUUID: "cache-uuid", CacheSchema: 19, RegistrationID: "registration-1",
		RepoID: "owner/repo", Collection: "issues", IdempotencyKey: "sync-1",
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
	for _, forbidden := range []string{"private source body", "payload", "checksum", "idempotency", "sync-stages", string(os.PathSeparator) + "tmp"} {
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
