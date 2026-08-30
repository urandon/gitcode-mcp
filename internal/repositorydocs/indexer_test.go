package repositorydocs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
)

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestIndexerPublishesCommittedRevisionAndReusesRenamedBlob(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "# Guide\n\nРусский, English, 中文.\n")
	firstCommit := commitTestRepository(t, root, "first")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	provider, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake-embedding", Dimensions: 8, BatchSize: 2, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(store, provider)
	var phases []string
	first, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: firstCommit, ChunkBytes: 32, Progress: func(progress IndexProgress) {
		phases = append(phases, progress.Phase)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != cache.RepoDocSetReady || first.EligibleFiles != 1 || first.EligibleChunks == 0 || first.EmbeddedChunks != first.EligibleChunks {
		t.Fatalf("first result = %#v", first)
	}
	for _, required := range []string{"plan", "walk", "embed", "publish", "published"} {
		if !containsString(phases, required) {
			t.Fatalf("progress phases = %v, missing %s", phases, required)
		}
	}

	runGit(t, root, "mv", "docs/guide.md", "docs/renamed.md")
	secondCommit := commitTestRepository(t, root, "rename")
	second, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: secondCommit, ChunkBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != cache.RepoDocSetReady || second.EmbeddedChunks != 0 || second.ReusedChunks != first.EligibleChunks {
		t.Fatalf("second result = %#v", second)
	}
	memberships, err := store.ListRepositoryDocMembership(ctx, "owner/repo", second.RevisionSetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) == 0 || memberships[0].Path != "docs/renamed.md" {
		t.Fatalf("memberships = %#v", memberships)
	}
}

func TestIndexerLeavesBoundedRunPartial(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "one paragraph\n\ntwo paragraph\n\nthree paragraph\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	provider, _ := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 1, Timeout: time.Second})
	indexer := NewIndexer(store, provider)
	var result IndexResult
	for run := 0; run < 10; run++ {
		result, err = indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, ChunkBytes: 16, MaxChunks: 1})
		if err != nil {
			t.Fatal(err)
		}
		if run == 0 && (result.State != cache.RepoDocSetPartial || result.FailedChunks == 0 || result.EmbeddedChunks != 1) {
			t.Fatalf("first bounded result = %#v", result)
		}
		if result.State == cache.RepoDocSetReady {
			break
		}
	}
	if result.State != cache.RepoDocSetReady || result.ReusedChunks+result.EmbeddedChunks != result.EligibleChunks {
		t.Fatalf("resumed result = %#v", result)
	}
}

func TestIndexerStreamsProviderBatches(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", strings.Repeat("bounded repository documentation\n\n", 32))
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	fake, _ := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 2, Timeout: time.Second})
	provider := &recordingEmbeddingProvider{EmbeddingProvider: fake}
	result, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, ChunkBytes: 32, BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetReady || provider.calls < 2 || provider.maxBatch > 2 {
		t.Fatalf("result=%#v calls=%d max_batch=%d", result, provider.calls, provider.maxBatch)
	}
}

func TestIndexerFallsBackPerItemAndKeepsFailedCoveragePartial(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/en.md", "English resilient indexing marker.\n")
	writeTestFile(t, root, "docs/ru.md", "Русский устойчивый индекс.\n")
	writeTestFile(t, root, "docs/zh.md", "中文失败标记.\n")
	commit := commitTestRepository(t, root, "multilingual provider fallback")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := &selectiveFailureProvider{EmbeddingProvider: repositoryDocsSearchProvider(t), marker: "中文失败标记"}
	result, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetPartial || result.EligibleChunks != 3 || result.EmbeddedChunks != 2 || result.FailedChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
	if provider.batchCalls != 1 || provider.singleCalls != 3 || provider.failedSingles != 1 {
		t.Fatalf("provider calls batch=%d single=%d failed=%d", provider.batchCalls, provider.singleCalls, provider.failedSingles)
	}
}

func TestIndexerRejectsProviderVectorDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "dimension contract must fail closed\n")
	commit := commitTestRepository(t, root, "dimension mismatch")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := &wrongDimensionProvider{EmbeddingProvider: repositoryDocsSearchProvider(t)}
	result, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetPartial || result.EligibleChunks != 1 || result.EmbeddedChunks != 0 || result.FailedChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
	if _, err := store.LoadRepositoryDocSearchSnapshot(ctx, "owner/repo", result.RevisionSetID, result.NamespaceID); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("partial set became searchable: %v", err)
	}
}

func TestIndexerWriterContentionDoesNotCallEmbeddingProvider(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "writer contention must be retryable\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	contender, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	lease, err := contender.AcquireWriter(ctx, cache.WriterRequest{Operation: "test-holder", RepoID: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	defer contender.ReleaseWriter(context.Background(), lease)

	fake, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 1, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingEmbeddingProvider{EmbeddingProvider: fake}
	indexer := NewIndexer(store, provider)
	indexer.writerWaitBudget = 40 * time.Millisecond
	_, err = indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	var contention cache.ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("index error = %T %[1]v, want ErrLockContention", err)
	}
	if provider.calls != 0 {
		t.Fatalf("embedding provider calls = %d, want 0", provider.calls)
	}
	sets, listErr := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: "owner/repo"})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(sets) != 0 {
		t.Fatalf("revision sets written during contention: %#v", sets)
	}
}

func TestIndexerPreservesFetchedEmbeddingAcrossTransientWriterContention(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "preserve this fetched embedding across a local writer handoff\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	contender, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	fake, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 8, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider := &postEmbedContentionProvider{EmbeddingProvider: fake, store: contender, hold: 75 * time.Millisecond}
	indexer := NewIndexer(store, provider)
	indexer.writerWaitBudget = time.Second
	result, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetReady || result.EmbeddedChunks != result.EligibleChunks || result.EligibleChunks != 1 {
		t.Fatalf("result = %#v", result)
	}
	if provider.calls != 1 {
		t.Fatalf("embedding provider calls = %d, want exactly one", provider.calls)
	}
}

func TestIndexerReplaysDurableVectorCheckpointAfterWriterBudgetExhaustion(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	document := "durably preserve this provider result without caching source bytes\n"
	writeTestFile(t, root, "README.md", document)
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	cachePath := filepath.Join(work, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	contender, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	fake, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 8, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider := &postEmbedContentionProvider{EmbeddingProvider: fake, store: contender, hold: 150 * time.Millisecond}
	checkpointDir := filepath.Join(work, "vector-checkpoints")
	checkpoints, err := NewFileVectorCheckpointStore(checkpointDir)
	if err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(store, provider).WithVectorCheckpointStore(checkpoints)
	indexer.writerWaitBudget = 20 * time.Millisecond
	if _, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit}); err == nil {
		t.Fatal("first indexing run succeeded despite writer budget exhaustion")
	}
	if provider.calls != 1 {
		t.Fatalf("embedding provider calls after first run = %d, want 1", provider.calls)
	}
	entries, err := os.ReadDir(checkpointDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("durable checkpoints = %d, err=%v", len(entries), err)
	}
	checkpointBytes, err := os.ReadFile(filepath.Join(checkpointDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(checkpointBytes), document) {
		t.Fatal("vector checkpoint persisted repository document bytes")
	}
	time.Sleep(175 * time.Millisecond)
	indexer.writerWaitBudget = time.Second
	result, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetReady || result.EmbeddedChunks != 1 || result.EligibleChunks != 1 {
		t.Fatalf("replayed result = %#v", result)
	}
	if provider.calls != 1 {
		t.Fatalf("embedding provider calls after replay = %d, want exactly one", provider.calls)
	}
	entries, err = os.ReadDir(checkpointDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("checkpoints after replay = %d, err=%v", len(entries), err)
	}
}

func TestIndexerCheckpointsFetchedVectorAfterRequestCancellation(t *testing.T) {
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "preserve the fetched vector across cancellation\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	store, err := cache.NewSQLiteStore(context.Background(), filepath.Join(work, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	fake, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 8, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancelAfterEmbedProvider{EmbeddingProvider: fake, cancel: cancel}
	checkpointDir := filepath.Join(work, "vector-checkpoints")
	checkpoints, err := NewFileVectorCheckpointStore(checkpointDir)
	if err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(store, provider).WithVectorCheckpointStore(checkpoints)
	if _, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit}); err == nil {
		t.Fatal("indexing unexpectedly completed after provider cancelled its request")
	}
	entries, err := os.ReadDir(checkpointDir)
	if err != nil || len(entries) != 1 || provider.calls != 1 {
		t.Fatalf("checkpoint entries=%d provider_calls=%d err=%v", len(entries), provider.calls, err)
	}
	result, err := indexer.Run(context.Background(), IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetReady || provider.calls != 1 {
		t.Fatalf("replay result=%#v provider_calls=%d", result, provider.calls)
	}
}

func TestIndexerEnforcesHardContentExclusionsBeforeEmbedding(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	validOID := strings.Repeat("a", 64)
	writeTestFile(t, root, "docs/pointer.md", "version https://git-lfs.github.com/spec/v1\noid sha256:"+validOID+"\nsize 123\n")
	writeTestFile(t, root, "docs/nul.md", "not text\x00payload")
	if err := os.Symlink("../outside-policy.md", filepath.Join(root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	submodule := initTestRepository(t)
	writeTestFile(t, submodule, "README.md", "nested repository must not be indexed\n")
	commitTestRepository(t, submodule, "nested")
	runGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "docs/vendor")
	commit := commitTestRepository(t, root, "hard exclusions")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	fake, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 8, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingEmbeddingProvider{EmbeddingProvider: fake}
	result, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetReady || result.ExcludedFiles != 4 || result.EligibleFiles != 0 || result.EligibleChunks != 0 {
		t.Fatalf("result = %#v", result)
	}
	if provider.calls != 0 {
		t.Fatalf("embedding provider calls = %d, want 0 for hard exclusions", provider.calls)
	}
	exclusions, err := store.ListRepositoryDocExclusions(ctx, "owner/repo", result.RevisionSetID)
	if err != nil {
		t.Fatal(err)
	}
	wantExclusions := map[string]string{
		"docs/link.md":    string(DocumentContentSymlink),
		"docs/nul.md":     string(DocumentContentNUL),
		"docs/pointer.md": string(DocumentContentLFSPointer),
		"docs/vendor":     string(DocumentContentSubmodule),
	}
	if len(exclusions) != len(wantExclusions) {
		t.Fatalf("typed exclusions = %#v", exclusions)
	}
	for _, exclusion := range exclusions {
		if wantExclusions[exclusion.Path] != exclusion.ReasonCode {
			t.Fatalf("typed exclusion = %#v, want %q", exclusion, wantExclusions[exclusion.Path])
		}
	}
}

type recordingEmbeddingProvider struct {
	rag.EmbeddingProvider
	calls    int
	maxBatch int
}

type postEmbedContentionProvider struct {
	rag.EmbeddingProvider
	store *cache.SQLiteStore
	hold  time.Duration
	once  sync.Once
	calls int
	err   error
}

type cancelAfterEmbedProvider struct {
	rag.EmbeddingProvider
	cancel context.CancelFunc
	calls  int
}

type selectiveFailureProvider struct {
	rag.EmbeddingProvider
	marker        string
	batchCalls    int
	singleCalls   int
	failedSingles int
}

func (p *selectiveFailureProvider) Embed(ctx context.Context, req rag.EmbedRequest) (rag.EmbedResponse, error) {
	if len(req.Inputs) > 1 {
		p.batchCalls++
		return rag.EmbedResponse{}, errors.New("synthetic batch failure")
	}
	p.singleCalls++
	if len(req.Inputs) == 1 && strings.Contains(req.Inputs[0], p.marker) {
		p.failedSingles++
		return rag.EmbedResponse{}, errors.New("synthetic item failure")
	}
	return p.EmbeddingProvider.Embed(ctx, req)
}

type wrongDimensionProvider struct{ rag.EmbeddingProvider }

func (p *wrongDimensionProvider) Embed(ctx context.Context, req rag.EmbedRequest) (rag.EmbedResponse, error) {
	response, err := p.EmbeddingProvider.Embed(ctx, req)
	if err != nil {
		return response, err
	}
	for index := range response.Embeddings {
		response.Embeddings[index] = append(response.Embeddings[index], 1)
	}
	response.Dimensions++
	return response, nil
}

func (p *cancelAfterEmbedProvider) Embed(ctx context.Context, req rag.EmbedRequest) (rag.EmbedResponse, error) {
	p.calls++
	response, err := p.EmbeddingProvider.Embed(ctx, req)
	if err == nil && p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	return response, err
}

func (p *postEmbedContentionProvider) Embed(ctx context.Context, req rag.EmbedRequest) (rag.EmbedResponse, error) {
	p.calls++
	response, err := p.EmbeddingProvider.Embed(ctx, req)
	if err != nil {
		return response, err
	}
	p.once.Do(func() {
		lease, acquireErr := p.store.AcquireWriter(ctx, cache.WriterRequest{Operation: "post-embed-contender", RepoID: "owner/repo"})
		if acquireErr != nil {
			p.err = acquireErr
			return
		}
		go func() {
			timer := time.NewTimer(p.hold)
			defer timer.Stop()
			<-timer.C
			_ = p.store.ReleaseWriter(context.Background(), lease)
		}()
	})
	if p.err != nil {
		return rag.EmbedResponse{}, p.err
	}
	return response, nil
}

func (p *recordingEmbeddingProvider) Embed(ctx context.Context, req rag.EmbedRequest) (rag.EmbedResponse, error) {
	p.calls++
	if len(req.Inputs) > p.maxBatch {
		p.maxBatch = len(req.Inputs)
	}
	return p.EmbeddingProvider.Embed(ctx, req)
}
