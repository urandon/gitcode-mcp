package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"gitcode-mcp/internal/localgit"
)

func requireLocalPlanCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *localgit.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected typed %s", code)
	}
}

func TestLocalProjectMutationPlansContentFreeGuardsAndDurableReplay(t *testing.T) {
	ctx := context.Background()
	host, cfg, path := localSearchFixture(t)
	status, _ := host.Status(ctx)
	head := status.Projects[0].Commit
	q := LocalProjectMutationPlanRequest{LocalProjectMutation: LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Draft", Body: "PrivateDraftMarker", ExpectedHead: head, WriteMode: "local", IdempotencyKey: "private-retry-marker"}}}
	plan, err := host.PlanMutation(ctx, q)
	if err != nil || plan.Commit != head || plan.DataBoundary != "local_git" || plan.Operation != "create_issue" {
		t.Fatal("mutation preview unavailable")
	}
	encoded, _ := json.Marshal(plan)
	for _, secret := range []string{path, "PrivateDraftMarker", "private-retry-marker", "storage_ref", "authority"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("plan exposed content, private routing or key")
		}
	}
	status, _ = host.Status(ctx)
	if status.Projects[0].Commit != head {
		t.Fatal("plan advanced project authority")
	}
	if _, err := os.Stat(localProjectIndexPath(cfg)); !os.IsNotExist(err) {
		t.Fatal("mutation plan initialized derived storage")
	}
	_, err = host.ApplyMutationPlan(ctx, q)
	requireLocalPlanCode(t, err, "stale_plan")
	q.PlanID = plan.PlanID
	changed := q
	changed.Mutation.Body = "Different intent"
	_, err = host.ApplyMutationPlan(ctx, changed)
	requireLocalPlanCode(t, err, "stale_plan")
	created, err := host.ApplyMutationPlan(ctx, q)
	if err != nil || created.Replayed || created.Entity.Metadata.ID != "ISSUE-1" {
		t.Fatal("confirmed mutation failed")
	}
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Later", Body: "Later revision", State: "closed", ExpectedRevision: created.Entity.Citation.Blob, IdempotencyKey: "later"})
	status, _ = host.Status(ctx)
	later := status.Projects[0].Commit
	replayed, err := host.ApplyMutationPlan(ctx, q)
	if err != nil || !replayed.Replayed || replayed.Entity.Citation != created.Entity.Citation || replayed.Entity.Body != "PrivateDraftMarker" {
		t.Fatal("confirmed retry did not resolve the original Git receipt before stale guards")
	}
	status, _ = host.Status(ctx)
	if status.Projects[0].Commit != later {
		t.Fatal("retry created another commit")
	}
	stale := q
	stale.Mutation.IdempotencyKey = "new-intent"
	_, err = host.PlanMutation(ctx, stale)
	requireLocalPlanCode(t, err, "write_conflict")
	view := host.bindings["lab"]
	view.readOnly = true
	host.bindings["view"] = view
	q.Project = "view"
	_, err = host.PlanMutation(ctx, q)
	requireLocalPlanCode(t, err, "read_only")
	q.Project = "unknown"
	_, err = host.PlanMutation(ctx, q)
	requireLocalPlanCode(t, err, "unknown_project")
}

func TestLocalProjectIndexPlanPinsCommitProfileBoundaryAndBudget(t *testing.T) {
	ctx := context.Background()
	host, cfg, path := localSearchFixture(t)
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "evidence", IdempotencyKey: "issue"})
	q := LocalProjectIndexPlanRequest{LocalProjectIndexRequest: LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 1}, IdempotencyKey: "index-key"}
	plan, err := host.PlanIndex(ctx, q)
	if err != nil || plan.MaxChunks != 1 || plan.DataBoundary != "local_process" {
		t.Fatal("bounded index plan unavailable")
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), path) || strings.Contains(string(encoded), cfg.CachePath) || strings.Contains(string(encoded), "index-key") {
		t.Fatal("index plan leaked private routing or key")
	}
	if _, err := os.Stat(localProjectIndexPath(cfg)); !os.IsNotExist(err) {
		t.Fatal("index plan created a cache or embedded content")
	}
	q.PlanID = plan.PlanID
	changed := q
	changed.MaxChunks = 2
	_, err = host.ApplyIndexPlan(ctx, changed)
	requireLocalPlanCode(t, err, "stale_plan")
	profile := cfg.RAG.Profiles["fixture"]
	profile.Dimensions = 16
	cfg.RAG.Profiles["fixture"] = profile
	host.WithSearchConfig(cfg)
	_, err = host.ApplyIndexPlan(ctx, q)
	requireLocalPlanCode(t, err, "stale_plan")
	profile.Dimensions = 8
	cfg.RAG.Profiles["fixture"] = profile
	host.WithSearchConfig(cfg)
	searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_wiki", Title: "Note", Body: "later", IdempotencyKey: "wiki"})
	_, err = host.ApplyIndexPlan(ctx, q)
	requireLocalPlanCode(t, err, "stale_plan")
	_, err = host.indexAt(ctx, q.LocalProjectIndexRequest, plan.Commit)
	requireLocalPlanCode(t, err, "stale_plan")
	if _, err := os.Stat(localProjectIndexPath(cfg)); !os.IsNotExist(err) {
		t.Fatal("stale index plan changed derived storage")
	}
	plan, err = host.PlanIndex(ctx, q)
	if err != nil {
		t.Fatal("fresh index plan unavailable")
	}
	q.PlanID = plan.PlanID
	indexed, err := host.ApplyIndexPlan(ctx, q)
	if err != nil || indexed.EmbeddedChunks != 1 || indexed.State != "partial" {
		t.Fatal("confirmed chunk budget was not respected")
	}
}
