package servicectl

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncCollectionRetryJournalPersistsPrivateStateAtomically(t *testing.T) {
	runtimeDir := t.TempDir()
	journal := newSyncCollectionRetryJournal(runtimeDir)
	retryAt := time.Unix(1_700_000_000, 0).UTC()
	base := syncCollectionRetryCheckpoint{
		JobID: "job-000001", Collection: "wiki", RemoteType: "wiki", Attempt: 1, RetryAt: retryAt,
		Request: syncCollectionRetryRequest{
			RepoID: "owner/repo", CacheUUID: "cache-a", RegistrationID: "registration-a",
			CachePath: "/private/cache.db", Issues: true, Wiki: true,
			CollectionPages: map[string]int{"wiki": 3},
		},
	}
	if err := journal.Upsert(base); err != nil {
		t.Fatal(err)
	}
	updated := base
	updated.Attempt, updated.RetryAt = 2, retryAt.Add(time.Minute)
	if err := journal.Upsert(updated); err != nil {
		t.Fatal(err)
	}
	if err := journal.Upsert(syncCollectionRetryCheckpoint{JobID: "job-000001", Collection: "issues", RemoteType: "issue", Attempt: 1, RetryAt: retryAt, Request: base.Request}); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := journal.List()
	if err != nil || len(checkpoints) != 2 {
		t.Fatalf("checkpoints=%+v err=%v", checkpoints, err)
	}
	if checkpoints[0].Collection != "issues" || checkpoints[1].Attempt != 2 {
		t.Fatalf("journal did not sort/upsert deterministically: %+v", checkpoints)
	}
	info, err := os.Stat(filepath.Join(runtimeDir, "sync-collection-retries.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private journal mode=%v", info.Mode().Perm())
	}
	roundTrip := checkpoints[1].Request.startRequest()
	if roundTrip.CachePath != "/private/cache.db" || roundTrip.collectionPages["wiki"] != 3 || !roundTrip.Issues || !roundTrip.Wiki {
		t.Fatalf("request round trip lost retry authority: %+v", roundTrip)
	}
	if err := journal.Remove("job-000001", "wiki"); err != nil {
		t.Fatal(err)
	}
	checkpoints, _ = journal.List()
	if len(checkpoints) != 1 || checkpoints[0].Collection != "issues" {
		t.Fatalf("targeted remove=%+v", checkpoints)
	}
	if err := journal.Remove("job-000001", ""); err != nil {
		t.Fatal(err)
	}
	checkpoints, _ = journal.List()
	if len(checkpoints) != 0 {
		t.Fatalf("job remove left checkpoints: %+v", checkpoints)
	}
}

func TestSyncCollectionRetryJournalFailsClosedOnCorruption(t *testing.T) {
	runtimeDir := t.TempDir()
	journal := newSyncCollectionRetryJournal(runtimeDir)
	if err := os.WriteFile(journal.path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := journal.List()
	var typed syncCollectionRetryCheckpointError
	if !errors.As(err, &typed) {
		t.Fatalf("corrupt journal error=%T %v", err, err)
	}
}
