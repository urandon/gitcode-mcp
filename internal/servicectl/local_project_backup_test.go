package servicectl

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func TestLocalProjectRestoredAdminAndDerivedCacheLoss(t *testing.T) {
	m, path := localFixtureManager(t)
	ctx := context.Background()
	store, err := localgit.Open(ctx, path, localgit.Limits{})
	if err != nil {
		t.Fatal("fixture store")
	}
	snap, _ := store.Snapshot(ctx, "refs/heads/main")
	req := localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "Original evidence", ExpectedHead: snap.Commit(), WriteMode: "local", IdempotencyKey: "question"}
	created, err := store.Mutate(ctx, "refs/heads/main", req)
	if err != nil {
		t.Fatal("fixture create")
	}
	updated, err := store.Mutate(ctx, "refs/heads/main", localgit.Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Answered", Body: "LaterEvidence", State: "closed", ExpectedRevision: created.Entity.Citation.Blob, WriteMode: "local", IdempotencyKey: "answer"})
	if err != nil {
		t.Fatal("fixture edit")
	}
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	if _, err = store.ExportBundle(ctx, "refs/heads/main", "lab", bundle); err != nil {
		t.Fatal("fixture export")
	}
	target, _ := config.LocalProjectStorePath(m.Source, "restored")
	if _, _, err = localgit.RestoreBundle(ctx, target, bundle, "lab", "refs/heads/main", updated.Entity.Citation.Commit); err != nil {
		t.Fatal("fixture restore")
	}
	m.AdminAutoStart = true
	m.EffectiveConfig.CachePath = filepath.Join(filepath.Dir(path), "derived.db")
	m.EffectiveConfig.RAG = config.RAGConfig{Providers: map[string]config.RAGProviderConfig{"fixture": {Type: "fake", DataBoundary: "local_process"}}, Profiles: map[string]config.RAGProfileConfig{"fixture": {Provider: "fixture", Model: "fixture-embedding", Dimensions: 8, MaxInputTokens: 512, BatchSize: 2}}}
	p := m.EffectiveConfig.LocalProjects["lab"]
	p.StorageRef = "restored"
	p.RAGProfile = "fixture"
	m.EffectiveConfig.LocalProjects["lab"] = p
	ipc, stop := runLocalFixture(t, m)
	browser := openLocalAdmin(t, ipc)
	var current localgit.Entity
	browser.operation(t, "get", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1"}, &current)
	if current.Body != "LaterEvidence" || current.Citation != updated.Entity.Citation {
		t.Fatal("restored Admin bytes/citation")
	}
	var indexPlan service.LocalProjectPlan
	indexReq := service.LocalProjectIndexPlanRequest{LocalProjectIndexRequest: service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 8}}
	browser.operation(t, "index-plan", indexReq, &indexPlan)
	indexReq.PlanID = indexPlan.PlanID
	indexReq.IdempotencyKey = "derived"
	var indexed service.LocalProjectIndexResult
	browser.operation(t, "index-apply", indexReq, &indexed)
	if indexed.State != "ready" {
		t.Fatal("restored explicit index")
	}
	var search service.LocalProjectSearchResult
	browser.operation(t, "search", service.LocalProjectSearchRequest{Project: "lab", Query: "LaterEvidence", Mode: "hybrid"}, &search)
	if search.EffectiveMode != "hybrid" || len(search.Hits) != 1 {
		t.Fatal("restored hybrid not ready")
	}
	stop()
	derived := m.EffectiveConfig.CachePath + ".local-project-index.db"
	if err = os.Remove(derived); err != nil {
		t.Fatal("derived fixture absent")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if e := os.Remove(derived + suffix); e != nil && !os.IsNotExist(e) {
			t.Fatal("derived fixture cleanup")
		}
	}
	ipc, stop = runLocalFixture(t, m)
	defer stop()
	browser = openLocalAdmin(t, ipc)
	browser.operation(t, "search", service.LocalProjectSearchRequest{Project: "lab", Query: "LaterEvidence", Mode: "hybrid"}, &search)
	if search.EffectiveMode != "fulltext" || len(search.Hits) != 1 || search.Hits[0].Citation != updated.Entity.Citation || search.Fallback == "" {
		t.Fatal("cache loss hid restored Git evidence")
	}
	var history []localgit.Citation
	browser.operation(t, "history", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1", MaxCommits: 64}, &history)
	if len(history) != 2 || history[1] != created.Entity.Citation {
		t.Fatal("cache loss changed history")
	}
	req.ExpectedHead = updated.Entity.Citation.Commit
	mutation := service.LocalProjectMutationPlanRequest{LocalProjectMutation: service.LocalProjectMutation{Project: "lab", Mutation: req}}
	var plan service.LocalProjectPlan
	browser.operation(t, "mutation-plan", mutation, &plan)
	mutation.PlanID = plan.PlanID
	var replay localgit.MutationResult
	browser.operation(t, "mutation-apply", mutation, &replay)
	if !replay.Replayed || replay.Entity.Citation != created.Entity.Citation || replay.Entity.Body != created.Entity.Body {
		t.Fatal("restored Admin receipt replay")
	}
}
