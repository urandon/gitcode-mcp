package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/repositorydocs"
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
	if job.ProfileID != cfg.RAG.Indexing.Profile {
		t.Fatalf("job profile id = %q, want effective indexing profile %q", job.ProfileID, cfg.RAG.Indexing.Profile)
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

func TestRepositoryDocsIndexRejectsRemoteOrUnknownProviderBoundary(t *testing.T) {
	for _, boundary := range []string{"remote", "unknown", ""} {
		t.Run(boundary, func(t *testing.T) {
			cfg := adminFakeRAGConfig()
			profile := cfg.RAG.Profiles[cfg.RAG.Indexing.Profile]
			provider := cfg.RAG.Providers[profile.Provider]
			provider.DataBoundary = boundary
			cfg.RAG.Providers[profile.Provider] = provider
			_, _, _, err := requireLocalRepositoryDocsProvider(cfg, cfg.RAG.Indexing.Profile)
			var blocked RepositoryDocsProviderBoundaryError
			if !errors.As(err, &blocked) || blocked.DiagnosticCode() != "repository_docs_provider_boundary_blocked" {
				t.Fatalf("boundary=%q err=%T %v", boundary, err, err)
			}
		})
	}
}

func TestRepositoryDocsAdmissionSnapshotChangeIsSupersededNotFailed(t *testing.T) {
	status, class := repositoryDocsIndexErrorStatus(&repositorydocs.IndexSnapshotStaleError{})
	if status != JobStatusSuperseded || class != "repository_docs_snapshot_stale" {
		t.Fatalf("status=%q class=%q", status, class)
	}
}

func TestRepositoryDocsIndexUsesTheGuardedEffectiveProfile(t *testing.T) {
	cfg := adminFakeRAGConfig()
	localProfileID := cfg.RAG.Indexing.Profile
	cfg.RAG.DefaultProfile = "remote-default"
	cfg.RAG.Providers["remote-provider"] = config.RAGProviderConfig{Type: "fake", DataBoundary: "remote"}
	cfg.RAG.Profiles["remote-default"] = config.RAGProfileConfig{Provider: "remote-provider", Model: "remote-model", Dimensions: 2}
	profileID, providerID, boundary, err := requireLocalRepositoryDocsProvider(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if profileID != localProfileID || providerID != cfg.RAG.Profiles[localProfileID].Provider || boundary == "remote" {
		t.Fatalf("effective profile=%q provider=%q boundary=%q", profileID, providerID, boundary)
	}
}

func TestRepositoryDocsWorkKeyChangesWithTrackedOverlay(t *testing.T) {
	ctx := context.Background()
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	runGitForRepositoryDocsJobTest(t, repoPath, "init")
	runGitForRepositoryDocsJobTest(t, repoPath, "add", "README.md")
	runGitForRepositoryDocsJobTest(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	repo, err := repositorydocs.OpenRepository(ctx, repoPath)
	if err != nil {
		t.Fatal(err)
	}
	req := StartRepositoryDocsIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", IncludeWorktree: true}
	write("overlay one\n")
	one, err := repositorydocs.InspectPolicy(ctx, repo, repositorydocs.PolicyRequest{RepoID: req.RepoID, IncludeWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	write("overlay two\n")
	two, err := repositorydocs.InspectPolicy(ctx, repo, repositorydocs.PolicyRequest{RepoID: req.RepoID, IncludeWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if repositoryDocsIndexWorkKey(req, repo, one, "namespace-1") == repositoryDocsIndexWorkKey(req, repo, two, "namespace-1") {
		t.Fatal("distinct tracked overlay generations coalesced to one work key")
	}
	if repositoryDocsIndexWorkKey(req, repo, two, "namespace-1") == repositoryDocsIndexWorkKey(req, repo, two, "namespace-2") {
		t.Fatal("distinct embedding namespaces coalesced to one work key")
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
