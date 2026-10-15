package service

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
)

func localSearchFixture(t *testing.T) (*LocalProjects, config.Config, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "project.git")
	if _, err := localgit.Initialize(ctx, path, "lab", "refs/heads/main"); err != nil {
		t.Fatal("initial Git fixture unavailable")
	}
	cfg := config.Config{CachePath: filepath.Join(root, "derived.db"), RAG: config.RAGConfig{Providers: map[string]config.RAGProviderConfig{"fixture": {Type: "fake", DataBoundary: "local_process"}}, Profiles: map[string]config.RAGProfileConfig{"fixture": {Provider: "fixture", Model: "fixture-embedding", Dimensions: 8, MaxInputTokens: 512, BatchSize: 2}}}}
	projects := map[string]config.LocalProjectConfig{"lab": {Backend: "local_git", ProjectID: "lab", StorageRef: "data", Ref: "refs/heads/main", RAGProfile: "fixture"}}
	host, err := NewLocalProjects(ctx, projects, func(string) (string, error) { return path, nil })
	if err != nil {
		t.Fatal("local shared service unavailable")
	}
	return host.WithSearchConfig(cfg), cfg, path
}

func searchFixtureMutation(t *testing.T, host *LocalProjects, m localgit.Mutation) localgit.Entity {
	t.Helper()
	status, err := host.Status(context.Background())
	if err != nil {
		t.Fatal("fixture head unavailable")
	}
	m.ExpectedHead = status.Projects[0].Commit
	m.WriteMode = "local"
	r, err := host.Mutate(context.Background(), LocalProjectMutation{Project: "lab", Mutation: m})
	if err != nil {
		t.Fatal("fixture mutation unavailable")
	}
	return r.Entity
}

func TestLocalProjectSearchGitAuthorityInvalidationAndRebuild(t *testing.T) {
	ctx := context.Background()
	host, cfg, _ := localSearchFixture(t)
	issue := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "oldneedle issue GitOnlyContentFixtureAlpha", IdempotencyKey: "issue"})
	comment := searchFixtureMutation(t, host, localgit.Mutation{Operation: "add_comment", ParentID: issue.Metadata.ID, Title: "Evidence", Body: "oldneedle comment GitOnlyContentFixtureBeta", IdempotencyKey: "comment"})
	wiki := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_wiki", Title: "Design", Body: "oldneedle wikiunique GitOnlyContentFixtureGamma", IdempotencyKey: "wiki"})
	q := LocalProjectSearchRequest{Project: "lab", Query: "oldneedle"}
	r, err := host.Search(ctx, q)
	if err != nil || len(r.Hits) != 3 || r.RequestedMode != "fulltext" || r.EffectiveMode != "fulltext" || r.IndexState != "not_requested" {
		t.Fatal("cache-free lexical search failed")
	}
	seen := map[string]bool{}
	for _, hit := range r.Hits {
		seen[hit.Citation.ID] = true
		if hit.Citation.ProjectID != "lab" || hit.Citation.Commit != r.Commit || len(hit.Digest) != 64 || hit.LineStart < 1 || hit.LineEnd < hit.LineStart {
			t.Fatal("project-scoped exact citation invalid")
		}
	}
	if !seen[issue.Metadata.ID] || !seen[comment.Metadata.ID] || !seen[wiki.Metadata.ID] {
		t.Fatal("entity cohort missing")
	}
	if _, err := os.Stat(localProjectIndexPath(cfg)); !os.IsNotExist(err) {
		t.Fatal("lexical query initialized a cache")
	}
	for _, excluded := range []string{"next_document", "intent_hash"} {
		got, err := host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: excluded})
		if err != nil || len(got.Hits) != 0 {
			t.Fatal("project counters or receipts entered search corpus")
		}
	}
	q.Mode = "hybrid"
	r, err = host.Search(ctx, q)
	if err != nil || len(r.Hits) != 3 || r.EffectiveMode != "fulltext" || r.Fallback != "index_unavailable" {
		t.Fatal("missing cache fallback invalid")
	}
	indexed, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 8})
	if err != nil || indexed.State != "ready" || indexed.EmbeddedChunks != 3 {
		t.Fatalf("existing index engine coverage state=%q eligible=%d embedded=%d failed=%d error=%v", indexed.State, indexed.EligibleChunks, indexed.EmbeddedChunks, indexed.FailedChunks, err)
	}
	r, err = host.Search(ctx, q)
	if err != nil || r.EffectiveMode != "hybrid" || r.IndexState != "ready" || len(r.Hits) != 3 {
		t.Fatal("ready hybrid retrieval failed")
	}
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "update_issue", ID: issue.Metadata.ID, Title: "Question", State: "open", Body: "newneedle changed GitOnlyContentFixtureDelta", IdempotencyKey: "edit"})
	r, err = host.Search(ctx, q)
	if err != nil || r.EffectiveMode != "fulltext" || r.IndexState != "stale" || len(r.Hits) != 2 {
		t.Fatal("changed commit reused stale semantic coverage")
	}
	indexed, err = host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"})
	if err != nil || indexed.State != "ready" || indexed.ReusedChunks != 2 || indexed.EmbeddedChunks != 1 {
		t.Fatal("digest-addressed vector reuse failed")
	}
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "delete_wiki", ID: wiki.Metadata.ID, IdempotencyKey: "delete"})
	r, err = host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: "wikiunique", Mode: "hybrid"})
	if err != nil || len(r.Hits) != 0 || r.EffectiveMode != "fulltext" || r.IndexState != "stale" {
		t.Fatal("tombstone remained in active corpus")
	}
	r, err = host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: "Design"})
	if err != nil || len(r.Hits) != 0 {
		t.Fatal("tombstone metadata remained searchable")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, readErr := os.ReadFile(localProjectIndexPath(cfg) + suffix)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal("derived evidence unreadable")
		}
		for _, marker := range []string{"GitOnlyContentFixtureAlpha", "GitOnlyContentFixtureBeta", "GitOnlyContentFixtureGamma", "GitOnlyContentFixtureDelta"} {
			if bytes.Contains(data, []byte(marker)) {
				t.Fatal("entity bytes persisted outside Git")
			}
		}
	}
	if os.Rename(localProjectIndexPath(cfg), localProjectIndexPath(cfg)+".removed") != nil {
		t.Fatal("derived cache loss fixture failed")
	}
	r, err = host.Search(ctx, q)
	if err != nil || len(r.Hits) != 1 || r.Fallback != "index_unavailable" {
		t.Fatal("derived cache loss lost current Git content")
	}
	indexed, err = host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"})
	if err != nil || indexed.State != "ready" || indexed.EmbeddedChunks != 2 {
		t.Fatal("derived-only rebuild failed")
	}
	current, err := host.Get(ctx, LocalProjectQuery{Project: "lab", Kind: localgit.Comment, ID: comment.Metadata.ID})
	if err != nil || current.Body != comment.Body {
		t.Fatal("index loss changed authoritative content")
	}
}

func TestLocalProjectSearchBoundsProfileBoundaryAndPartialFallback(t *testing.T) {
	ctx := context.Background()
	host, cfg, _ := localSearchFixture(t)
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "bounded content", IdempotencyKey: "issue"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_wiki", Title: "Note", Body: "bounded evidence", IdempotencyKey: "wiki"})
	for _, q := range []LocalProjectSearchRequest{{Project: "lab", Query: ""}, {Project: "lab", Query: strings.Repeat("x", 4097)}, {Project: "lab", Query: "x", Limit: 33}, {Project: "lab", Query: "x", Mode: "semantic"}, {Project: "missing", Query: "x"}} {
		if _, err := host.Search(ctx, q); err == nil {
			t.Fatal("invalid search accepted")
		}
	}
	for _, q := range []LocalProjectIndexRequest{{Project: "lab"}, {Project: "lab", WriteMode: "live"}, {Project: "lab", WriteMode: "local", MaxChunks: 129}, {Project: "lab", WriteMode: "local", MaxChunks: -1}} {
		if _, err := host.Index(ctx, q); err == nil {
			t.Fatal("unbounded or implicit index accepted")
		}
	}
	if _, err := os.Stat(localProjectIndexPath(cfg)); !os.IsNotExist(err) {
		t.Fatal("rejected operation initialized cache")
	}
	provider := cfg.RAG.Providers["fixture"]
	provider.DataBoundary = "remote"
	cfg.RAG.Providers["fixture"] = provider
	host.WithSearchConfig(cfg)
	if _, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"}); err == nil {
		t.Fatal("remote embedding boundary accepted")
	}
	if _, err := os.Stat(localProjectIndexPath(cfg)); !os.IsNotExist(err) {
		t.Fatal("remote boundary changed cache before rejection")
	}
	provider.DataBoundary = "local_process"
	cfg.RAG.Providers["fixture"] = provider
	host.WithSearchConfig(cfg)
	indexed, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 1})
	if err != nil || indexed.State != "partial" || indexed.EmbeddedChunks != 1 || indexed.FailedChunks != 1 {
		t.Fatalf("bounded index state=%q eligible=%d embedded=%d failed=%d error=%v", indexed.State, indexed.EligibleChunks, indexed.EmbeddedChunks, indexed.FailedChunks, err)
	}
	r, err := host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: "bounded", Mode: "hybrid"})
	if err != nil || r.IndexState != "partial" || r.Fallback != "index_partial" || r.EffectiveMode != "fulltext" || len(r.Hits) != 2 {
		t.Fatal("partial RAG suppressed lexical results")
	}
	store, err := cache.NewSQLiteStore(ctx, localProjectIndexPath(cfg))
	if err != nil {
		t.Fatal("partial metadata fixture unavailable")
	}
	sets, err := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: "local-project/lab", Limit: 1})
	if err != nil || len(sets) != 1 {
		store.Close()
		t.Fatal("partial set unavailable")
	}
	sets[0].State = cache.RepoDocSetBlocked
	if store.UpsertRepositoryDocRevisionSet(ctx, sets[0]) != nil {
		store.Close()
		t.Fatal("failed set fixture unavailable")
	}
	store.Close()
	r, err = host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: "bounded", Mode: "hybrid"})
	if err != nil || r.IndexState != "failed" || r.Fallback != "index_failed" || len(r.Hits) != 2 {
		t.Fatal("failed index hid lexical results")
	}
	indexed, err = host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 1})
	if err != nil || indexed.State != "ready" || indexed.ReusedChunks != 1 || indexed.EmbeddedChunks != 1 {
		t.Fatal("bounded index failed to resume")
	}
	b := host.bindings["lab"]
	b.profile = "disabled"
	host.bindings["lab"] = b
	r, err = host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: "bounded", Mode: "hybrid"})
	if err != nil || r.EffectiveMode != "fulltext" || r.Fallback != "profile_disabled" {
		t.Fatal("disabled profile was silently enabled")
	}
}

func TestLocalProjectSearchUntrustedDerivedCitationFallsBack(t *testing.T) {
	ctx := context.Background()
	host, cfg, _ := localSearchFixture(t)
	issue := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "needleissue", IdempotencyKey: "issue"})
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_wiki", Title: "Note", Body: "wikievidence", IdempotencyKey: "wiki"})
	if _, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"}); err != nil {
		t.Fatal("derived index fixture unavailable")
	}
	db, err := sql.Open("sqlite", localProjectIndexPath(cfg))
	if err != nil {
		t.Fatal("derived corruption fixture unavailable")
	}
	_, err = db.ExecContext(ctx, `UPDATE repo_doc_membership SET path='wiki/DOC-1.md' WHERE repo_id='local-project/lab' AND path='issues/ISSUE-1/issue.md'`)
	db.Close()
	if err != nil {
		t.Fatal("derived locator mutation unavailable")
	}
	r, err := host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: "needleissue", Mode: "hybrid"})
	if err != nil || r.EffectiveMode != "fulltext" || r.Fallback != "index_unavailable" || len(r.Hits) != 1 || r.Hits[0].Citation.Blob != issue.Citation.Blob {
		t.Fatal("derived locator minted a false project citation or hid lexical data")
	}
}

func TestLocalProjectSearchAliasesAndIndependentProjectScopes(t *testing.T) {
	ctx := context.Background()
	host, cfg, path := localSearchFixture(t)
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "onlyfirstproject", IdempotencyKey: "issue"})
	view := host.bindings["lab"]
	view.readOnly = true
	host.bindings["view"] = view
	if _, err := host.Index(ctx, LocalProjectIndexRequest{Project: "view", WriteMode: "local"}); err == nil {
		t.Fatal("read-only alias wrote index")
	}
	if _, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"}); err != nil {
		t.Fatal("first project index unavailable")
	}
	r, err := host.Search(ctx, LocalProjectSearchRequest{Project: "view", Query: "onlyfirstproject", Mode: "hybrid"})
	if err != nil || r.EffectiveMode != "hybrid" || len(r.Hits) != 1 || r.Project != "view" || r.Hits[0].Citation.ProjectID != "lab" {
		t.Fatal("canonical alias failed to share exact index identity")
	}
	otherPath := filepath.Join(filepath.Dir(path), "other.git")
	if _, err := localgit.Initialize(ctx, otherPath, "other", "refs/heads/main"); err != nil {
		t.Fatal("second project store unavailable")
	}
	other, err := NewLocalProjects(ctx, map[string]config.LocalProjectConfig{"other": {Backend: "local_git", ProjectID: "other", StorageRef: "other", Ref: "refs/heads/main", RAGProfile: "fixture"}}, func(string) (string, error) { return otherPath, nil })
	if err != nil {
		t.Fatal("second project binding unavailable")
	}
	other.WithSearchConfig(cfg)
	status, _ := other.Status(ctx)
	_, err = other.Mutate(ctx, LocalProjectMutation{Project: "other", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Other", Body: "onlysecondproject", ExpectedHead: status.Projects[0].Commit, WriteMode: "local", IdempotencyKey: "issue"}})
	if err != nil {
		t.Fatal("second project mutation unavailable")
	}
	if _, err := other.Index(ctx, LocalProjectIndexRequest{Project: "other", WriteMode: "local"}); err != nil {
		t.Fatal("second project index unavailable")
	}
	r, err = other.Search(ctx, LocalProjectSearchRequest{Project: "other", Query: "onlysecondproject", Mode: "hybrid"})
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Citation.ProjectID != "other" || strings.Contains(r.Hits[0].Snippet, "onlyfirstproject") {
		t.Fatal("project-scoped index leaked another authority")
	}
	if _, err := os.Stat(cfg.CachePath); !os.IsNotExist(err) {
		t.Fatal("local derived scopes initialized remote binding cache")
	}
}

func TestLocalProjectIndexWriterContentionIsTypedWithoutAuthorityChange(t *testing.T) {
	ctx := context.Background()
	host, cfg, _ := localSearchFixture(t)
	issue := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "writer evidence", IdempotencyKey: "issue"})
	if _, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"}); err != nil {
		t.Fatal("index fixture unavailable")
	}
	store, err := cache.NewSQLiteStore(ctx, localProjectIndexPath(cfg))
	if err != nil {
		t.Fatal("writer fixture unavailable")
	}
	defer store.Close()
	lease, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "fixture-writer", RepoID: "local-project/lab"})
	if err != nil {
		t.Fatal("writer admission fixture unavailable")
	}
	defer store.ReleaseWriter(ctx, lease)
	_, err = host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"})
	coded, ok := err.(interface{ DiagnosticCode() string })
	if !ok || coded.DiagnosticCode() != "local_git_writer_busy" {
		t.Fatal("real derived writer contention lost its typed classification")
	}
	got, err := host.Get(ctx, LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: issue.Metadata.ID})
	if err != nil || got.Citation != issue.Citation {
		t.Fatal("derived writer contention changed or blocked Git authority")
	}
}
