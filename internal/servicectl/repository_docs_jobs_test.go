package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
		SourceRegistrationID: "repo-doc-source-test", SourceRegistrationGeneration: 1,
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

func TestRepositoryDocsIndexRequiresRegisteredSourceBeforeJobCreation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	repoPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo"), "unregistered")
	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	_, err = jobs.StartRepositoryDocsIndex(ctx, Manager{EffectiveConfig: &cfg}, StartRepositoryDocsIndexJobRequest{
		RepoID: "owner/repo", RepositoryPath: repoPath, CachePath: cachePath,
	})
	if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "repository_docs_source_not_registered" {
		t.Fatalf("unregistered start error=%T %v", err, err)
	}
	if len(jobs.List()) != 0 {
		t.Fatalf("unregistered start created a job: %+v", jobs.List())
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
	req := StartRepositoryDocsIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "maintenance-reg-1", SourceRegistrationID: "source-reg-1", SourceRegistrationGeneration: 3, IncludeWorktree: true}
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
	baseKey := repositoryDocsIndexWorkKey(req, repo, two, "namespace-1")
	executionInputs := map[string]func(*StartRepositoryDocsIndexJobRequest){
		"batch size": func(candidate *StartRepositoryDocsIndexJobRequest) { candidate.BatchSize = 128 },
		"max chunks": func(candidate *StartRepositoryDocsIndexJobRequest) { candidate.MaxChunks = 10_000 },
		"profile alias with same namespace": func(candidate *StartRepositoryDocsIndexJobRequest) {
			candidate.Profile = "renamed-profile-with-the-same-exact-namespace"
		},
	}
	for name, mutate := range executionInputs {
		t.Run(name, func(t *testing.T) {
			candidate := req
			mutate(&candidate)
			if got := repositoryDocsIndexWorkKey(candidate, repo, two, "namespace-1"); got != baseKey {
				t.Fatalf("execution control changed semantic work key: base=%q changed=%q", baseKey, got)
			}
		})
	}
	differentAuthority := req
	differentAuthority.SourceRegistrationID = "source-reg-2"
	if repositoryDocsIndexWorkKey(differentAuthority, repo, two, "namespace-1") == baseKey {
		t.Fatal("distinct repository registrations coalesced to one work key")
	}
	differentGeneration := req
	differentGeneration.SourceRegistrationGeneration++
	if repositoryDocsIndexWorkKey(differentGeneration, repo, two, "namespace-1") == baseKey {
		t.Fatal("distinct repository registration generations coalesced to one work key")
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

func TestRPCRepositoryDocsRegistrationPersistenceFailureCreatesNoJob(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	repoPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo"), "admission failure")
	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	manager := Manager{EffectiveConfig: &cfg}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(dir, "managed-caches.json"))
	enroll := testMaintenanceEnrollRequest(cachePath, "enroll-admission-failure", MaintenancePolicy{})
	enroll.ConfigSnapshot = cfg
	enroll.ConfigHash = maintenanceHash(cfg)
	if _, err := maintenance.Enroll(ctx, enroll); err != nil {
		t.Fatal(err)
	}
	maintenance.mu.Lock()
	generationBefore := maintenance.generation
	maintenance.mu.Unlock()
	// A directory at the registry target makes the atomic rename fail after the
	// candidate source was validated, exercising admission rollback.
	brokenRegistry := filepath.Join(dir, "registry-is-a-directory")
	if err := os.Mkdir(brokenRegistry, 0o700); err != nil {
		t.Fatal(err)
	}
	maintenance.path = brokenRegistry
	params, err := json.Marshal(StartRepositoryDocsIndexJobRequest{RepoID: "owner/repo", RepositoryPath: repoPath, CachePath: cachePath})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (RPCServer{Manager: manager, Jobs: jobs, Maintenance: maintenance}).dispatch(ctx, "Jobs.StartRepositoryDocsIndex", params)
	if err == nil {
		t.Fatal("repository-doc admission unexpectedly succeeded")
	}
	if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "repository_docs_registration_persist_failed" {
		t.Fatalf("registration persistence error=%T %v", err, err)
	}
	if strings.Contains(err.Error(), repoPath) || strings.Contains(err.Error(), brokenRegistry) {
		t.Fatalf("public admission error leaked a private path: %v", err)
	}
	if len(jobs.List()) != 0 {
		t.Fatalf("registration failure left an orphan job: %+v", jobs.List())
	}
	listed, listErr := maintenance.List(ctx)
	if listErr != nil || len(listed.Entries) != 1 || listed.Entries[0].RepositoryDocs != nil {
		t.Fatalf("failed registration changed in-memory authority: %+v err=%v", listed, listErr)
	}
	maintenance.mu.Lock()
	generationAfter := maintenance.generation
	maintenance.mu.Unlock()
	if generationAfter != generationBefore {
		t.Fatalf("failed registration advanced registry generation: before=%d after=%d", generationBefore, generationAfter)
	}
}

func TestRPCRepositoryDocsConcurrentConflictingAdmissionStartsOnlyWinner(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	repositories := []string{
		createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-a"), "alpha"),
		createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-b"), "beta"),
	}
	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	manager := Manager{EffectiveConfig: &cfg}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(dir, "managed-caches.json"))
	enroll := testMaintenanceEnrollRequest(cachePath, "enroll-concurrent-admission", MaintenancePolicy{})
	enroll.ConfigSnapshot = cfg
	enroll.ConfigHash = maintenanceHash(cfg)
	if _, err := maintenance.Enroll(ctx, enroll); err != nil {
		t.Fatal(err)
	}
	server := RPCServer{Manager: manager, Jobs: jobs, Maintenance: maintenance}
	start := make(chan struct{})
	errs := make(chan error, len(repositories))
	var wg sync.WaitGroup
	for _, repositoryPath := range repositories {
		repositoryPath := repositoryPath
		wg.Add(1)
		go func() {
			defer wg.Done()
			params, marshalErr := json.Marshal(StartRepositoryDocsIndexJobRequest{
				RepoID: "owner/repo", RepositoryPath: repositoryPath, CachePath: cachePath,
			})
			if marshalErr != nil {
				errs <- marshalErr
				return
			}
			<-start
			_, dispatchErr := server.dispatch(ctx, "Jobs.StartRepositoryDocsIndex", params)
			errs <- dispatchErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, busyCount := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		var busy ErrCacheWriterBusy
		if errors.As(err, &busy) {
			busyCount++
			continue
		}
		t.Fatalf("unexpected concurrent admission error=%T %v", err, err)
	}
	if succeeded != 1 || busyCount != 1 {
		t.Fatalf("concurrent outcomes succeeded=%d busy=%d", succeeded, busyCount)
	}
	listedJobs := jobs.List()
	if got := len(listedJobs); got != 1 {
		t.Fatalf("jobs=%d, want exactly the admitted winner", got)
	}
	maintenance.mu.Lock()
	var admitted MaintenanceEntry
	var admittedPath, admittedProfile string
	for _, candidate := range maintenance.entries {
		if source := maintenance.sources[candidate.RegistrationID][listedJobs[0].SourceRegistrationID]; source != nil {
			admitted = cloneMaintenanceEntryForSource(candidate, source)
			admittedPath = source.RepositoryPath
			admittedProfile = source.Profile
			break
		}
	}
	maintenance.mu.Unlock()
	if admitted.RepositoryDocs == nil {
		t.Fatal("winning admission did not persist source authority")
	}
	prepared, err := prepareRepositoryDocsIndex(ctx, manager, StartRepositoryDocsIndexJobRequest{
		RepoID: "owner/repo", RepositoryPath: admittedPath, Profile: admittedProfile, CachePath: cachePath,
		CacheUUID: admitted.CacheUUID, RegistrationID: admitted.RegistrationID,
		SourceRegistrationID: admitted.RepositoryDocs.SourceRegistrationID, SourceRegistrationGeneration: admitted.RepositoryDocs.SourceRegistrationGeneration,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantWorkKey := repositoryDocsIndexWorkKey(prepared.request, prepared.repository, prepared.policy, prepared.namespaceID)
	wantSetID := repositoryDocsRevisionSetIdentity(prepared.request, prepared.repository, prepared.policy, prepared.namespaceID).ID()
	if listedJobs[0].RegistrationID != admitted.RegistrationID || listedJobs[0].WorkKey != wantWorkKey || listedJobs[0].SourceRegistrationID != admitted.RepositoryDocs.SourceRegistrationID || listedJobs[0].SourceRegistrationGeneration != admitted.RepositoryDocs.SourceRegistrationGeneration || listedJobs[0].ExpectedRevisionSetID != wantSetID {
		t.Fatalf("job did not use persisted source authority: job=%+v registration=%+v", listedJobs[0], admitted)
	}
	waitForTerminalJob(t, jobs, listedJobs[0].ID)
}

func TestCreateCoalescedJobDoesNotExposeUndurableQueuedWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapshotPath := t.TempDir() // A directory cannot be replaced by the jobs snapshot file.
	jobs := NewJobManager(snapshotPath)
	job, created, err := jobs.createCoalescedJob(RepositoryDocsIndexJobType, "owner/repo", "local", 0, "work-key", "cache-1", "registration-1", "namespace-1", cancel)
	if !errors.Is(err, ErrJobAdmissionPersistence) || created || job.ID != "" {
		t.Fatalf("job=%#v created=%v err=%v", job, created, err)
	}
	if strings.Contains(err.Error(), snapshotPath) {
		t.Fatalf("job snapshot path leaked: %v", err)
	}
	if got := jobs.List(); len(got) != 0 {
		t.Fatalf("undurable jobs remained observable: %#v", got)
	}
	if jobs.nextID != 0 {
		t.Fatalf("next id advanced after persistence failure: %d", jobs.nextID)
	}
	if ctx.Err() != nil {
		t.Fatalf("caller-owned context was cancelled before Start handled the error: %v", ctx.Err())
	}
}

func runGitForRepositoryDocsJobTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
