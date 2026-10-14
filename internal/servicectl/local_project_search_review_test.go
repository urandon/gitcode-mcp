package servicectl

import (
	"context"
	"database/sql"
	"encoding/json"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
	"path/filepath"
	"testing"
)

func g5ReviewIndexedFixture(t *testing.T) (*RPCClient, string, localgit.Entity) {
	t.Helper()
	m, _ := localFixtureManager(t)
	cfg := m.EffectiveConfig
	cfg.CachePath = filepath.Join(t.TempDir(), "derived.db")
	cfg.RAG = config.RAGConfig{
		Providers: map[string]config.RAGProviderConfig{"fixture": {Type: "fake", DataBoundary: "local_process"}},
		Profiles:  map[string]config.RAGProfileConfig{"fixture": {Provider: "fixture", Model: "fixture-embedding", Dimensions: 8, MaxInputTokens: 512, BatchSize: 2}},
	}
	b := cfg.LocalProjects["lab"]
	b.RAGProfile = "fixture"
	cfg.LocalProjects["lab"] = b
	client, _ := runLocalFixture(t, m)
	var status service.LocalProjectsStatus
	if client.Call(context.Background(), "LocalProjects.Status", nil, &status) != nil || len(status.Projects) != 1 {
		t.Fatal("source fixture unavailable")
	}
	var made localgit.MutationResult
	if client.Call(context.Background(), "LocalProjects.Mutate", service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "needle original Git evidence", ExpectedHead: status.Projects[0].Commit, WriteMode: "local", IdempotencyKey: "fixture"}}, &made) != nil {
		t.Fatal("mutation fixture unavailable")
	}
	var indexed service.LocalProjectIndexResult
	if client.Call(context.Background(), "LocalProjects.Index", service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local"}, &indexed) != nil || indexed.State != "ready" {
		t.Fatal("ready index fixture unavailable")
	}
	var positive service.LocalProjectSearchResult
	if client.Call(context.Background(), "LocalProjects.Search", service.LocalProjectSearchRequest{Project: "lab", Query: "needle", Mode: "hybrid"}, &positive) != nil || positive.EffectiveMode != "hybrid" || len(positive.Hits) != 1 {
		t.Fatal("genuine ready semantic control failed")
	}
	return client, cfg.CachePath + ".local-project-index.db", made.Entity
}

func g5ReviewAlterDerived(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("derived fixture unavailable")
	}
	_, err = db.Exec(statement)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatal("derived fixture alteration failed")
	}
}

func TestG5ReviewMalformedVectorStillReturnsLexicalOverIPC(t *testing.T) {
	client, path, want := g5ReviewIndexedFixture(t)
	g5ReviewAlterDerived(t, path, "UPDATE repo_doc_vectors SET vector=X'00'")
	var got service.LocalProjectSearchResult
	err := client.Call(context.Background(), "LocalProjects.Search", service.LocalProjectSearchRequest{Project: "lab", Query: "needle", Mode: "hybrid"}, &got)
	if err != nil {
		t.Fatal("malformed derived vector suppressed current lexical result over IPC")
	}
	if got.EffectiveMode != "fulltext" || got.Coverage.State != "lexical-only" || len(got.Hits) != 1 || got.Hits[0].Citation != want.Citation {
		t.Fatal("derived corruption did not produce honest lexical fallback")
	}
}

func TestG5ReviewMissingVectorsCannotClaimReadyHybrid(t *testing.T) {
	client, path, want := g5ReviewIndexedFixture(t)
	g5ReviewAlterDerived(t, path, "DELETE FROM repo_doc_vectors")
	var got service.LocalProjectSearchResult
	if client.Call(context.Background(), "LocalProjects.Search", service.LocalProjectSearchRequest{Project: "lab", Query: "needle", Mode: "hybrid"}, &got) != nil {
		t.Fatal("missing vectors suppressed lexical result")
	}
	if got.EffectiveMode != "fulltext" || got.IndexState == "ready" || len(got.Hits) != 1 || got.Hits[0].Citation != want.Citation {
		t.Fatal("missing derived vectors were reported as ready hybrid coverage")
	}
}

func TestG5ReviewDerivedLineLocatorMustMatchGitBytes(t *testing.T) {
	client, path, _ := g5ReviewIndexedFixture(t)
	g5ReviewAlterDerived(t, path, "UPDATE repo_doc_chunks SET line_start=999,line_end=999")
	var got service.LocalProjectSearchResult
	if client.Call(context.Background(), "LocalProjects.Search", service.LocalProjectSearchRequest{Project: "lab", Query: "unmatchedsemanticphrase", Mode: "hybrid"}, &got) != nil {
		t.Fatal("line-corruption fallback failed")
	}
	for _, hit := range got.Hits {
		if hit.LineStart == 999 || hit.LineEnd == 999 {
			t.Fatal("derived metadata minted line citation absent from pinned Git bytes")
		}
	}
}

func TestG5ReviewIndexCountsCannotOverflowConservation(t *testing.T) {
	r := map[string]any{"project": "lab", "project_id": "lab", "commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "revision_set_id": "repo-doc-set-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "state": "partial", "eligible_chunks": 126, "embedded_chunks": 128, "reused_chunks": int64(9223372036854775807), "failed_chunks": int64(9223372036854775807)}
	raw, _ := json.Marshal(r)
	if _, ok := validateLocalSearchResult("LocalProjects.Index", raw); ok {
		t.Fatal("overflowing impossible index result accepted")
	}
}
