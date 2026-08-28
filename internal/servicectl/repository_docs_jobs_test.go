package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func TestRepositoryDocsIndexJobCanonicalizesAliasAndPublishesMetadata(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{
		RepoID: "owner/canonical", Owner: "owner", Name: "canonical",
		APIBaseURL: "https://api.gitcode.com/api/v5", Aliases: []string{"urandon/sessionless"},
		Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	const sourceSentinel = "REPOSITORY_DOC_SOURCE_MUST_NOT_PERSIST_7f4a"
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Canonical docs\n\nAlias-safe repository documentation. "+sourceSentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForRepositoryDocsJobTest(t, repoPath, "init")
	runGitForRepositoryDocsJobTest(t, repoPath, "add", "README.md")
	runGitForRepositoryDocsJobTest(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")

	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	manager := Manager{EffectiveConfig: &cfg}
	jobSnapshotPath := filepath.Join(t.TempDir(), "jobs.json")
	jobs := NewJobManager(jobSnapshotPath)
	job, err := jobs.StartRepositoryDocsIndex(ctx, manager, StartRepositoryDocsIndexJobRequest{
		RepoID: "urandon/sessionless", RepositoryPath: repoPath, CachePath: cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.RepoID != "owner/canonical" {
		t.Fatalf("job repo id = %q, want canonical binding", job.RepoID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, _ = jobs.Get(job.ID)
		if jobTerminalStatus(job.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %#v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status != JobStatusSucceeded {
		t.Fatalf("job = %#v", job)
	}
	publicJob, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(publicJob, []byte(sourceSentinel)) || bytes.Contains(publicJob, []byte(repoPath)) {
		t.Fatalf("public job leaked repository source or path: %s", publicJob)
	}
	jobSnapshot, err := os.ReadFile(jobSnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(jobSnapshot, []byte(sourceSentinel)) || bytes.Contains(jobSnapshot, []byte(repoPath)) {
		t.Fatalf("persisted job snapshot leaked repository source or path: %s", jobSnapshot)
	}
	cacheBytes, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cacheBytes, []byte(sourceSentinel)) {
		t.Fatal("repository source sentinel persisted in SQLite cache")
	}
	store, err = cache.NewSQLiteReadOnlyStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sets, err := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: "owner/canonical"})
	if err != nil || len(sets) != 1 || sets[0].State != cache.RepoDocSetReady {
		t.Fatalf("canonical revision sets = %#v, err=%v", sets, err)
	}
	aliasSets, err := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: "urandon/sessionless"})
	if err != nil || len(aliasSets) != 0 {
		t.Fatalf("alias revision sets = %#v, err=%v", aliasSets, err)
	}
}

func TestRepositoryDocsVectorByteCeiling(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{name: "default", want: DefaultRepositoryDocsVectorBytes},
		{name: "configured", raw: "1048576", want: 1048576},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "invalid", raw: "one-megabyte", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := repositoryDocsVectorByteCeiling(Manager{Source: testSource{env: map[string]string{EnvRepositoryDocsVectorByteCeiling: test.raw}}})
			if (err != nil) != test.wantErr {
				t.Fatalf("repositoryDocsVectorByteCeiling() error = %v, wantErr=%v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("repositoryDocsVectorByteCeiling() = %d, want %d", got, test.want)
			}
		})
	}
}

func runGitForRepositoryDocsJobTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
