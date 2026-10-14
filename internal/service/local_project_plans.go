package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"gitcode-mcp/internal/localgit"
)

// Plans are stateless intent digests, not another project authority or receipt.
// Source mutations still settle exclusively through the Git adapter's receipt.
type LocalProjectMutationPlanRequest struct {
	LocalProjectMutation
	PlanID string `json:"plan_id,omitempty"`
}

type LocalProjectIndexPlanRequest struct {
	LocalProjectIndexRequest
	PlanID         string `json:"plan_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type LocalProjectPlan struct {
	PlanID       string   `json:"plan_id"`
	Project      string   `json:"project"`
	ProjectID    string   `json:"project_id"`
	Operation    string   `json:"operation"`
	TargetID     string   `json:"target_id,omitempty"`
	Commit       string   `json:"commit"`
	DataBoundary string   `json:"data_boundary"`
	MaxChunks    int      `json:"max_chunks,omitempty"`
	Effects      []string `json:"effects"`
}

func localProjectPlanID(b localProjectBinding, intent any) string {
	encoded, _ := json.Marshal(struct {
		Authority string `json:"authority"`
		ProjectID string `json:"project_id"`
		Ref       string `json:"ref"`
		Intent    any    `json:"intent"`
	}{b.runtime.authority, b.runtime.projectID, b.runtime.ref, intent})
	sum := sha256.Sum256(encoded)
	return "local-project-plan-" + hex.EncodeToString(sum[:])
}

func (s *LocalProjects) PlanMutation(ctx context.Context, q LocalProjectMutationPlanRequest) (LocalProjectPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var zero LocalProjectPlan
	b, err := s.binding(q.Project)
	if err != nil {
		return zero, err
	}
	if b.readOnly {
		return zero, localProjectError("read_only", "project")
	}
	commit, err := b.runtime.store.PreviewMutation(ctx, b.runtime.ref, b.runtime.projectID, q.Mutation)
	if err != nil {
		return zero, err
	}
	return LocalProjectPlan{PlanID: localProjectPlanID(b, q.LocalProjectMutation), Project: q.Project, ProjectID: b.runtime.projectID, Operation: q.Mutation.Operation, TargetID: q.Mutation.ID, Commit: commit, DataBoundary: "local_git", Effects: []string{"validate_current_revision", "publish_entity_counters_and_receipt_in_one_git_commit"}}, nil
}

func (s *LocalProjects) ApplyMutationPlan(ctx context.Context, q LocalProjectMutationPlanRequest) (localgit.MutationResult, error) {
	b, err := s.binding(q.Project)
	if err != nil {
		return localgit.MutationResult{}, err
	}
	if q.PlanID == "" || q.PlanID != localProjectPlanID(b, q.LocalProjectMutation) {
		return localgit.MutationResult{}, localProjectError("stale_plan", "plan_id")
	}
	// Do not re-preview here: known Git receipts must precede stale guards on
	// interrupted replay. The unchanged intent digest and actual writer/CAS
	// validate the confirmed input and current authority respectively.
	return s.Mutate(ctx, q.LocalProjectMutation)
}

func (s *LocalProjects) PlanIndex(ctx context.Context, q LocalProjectIndexPlanRequest) (LocalProjectPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var zero LocalProjectPlan
	if q.MaxChunks == 0 {
		q.MaxChunks = 64
	}
	if q.WriteMode != "local" || q.MaxChunks < 1 || q.MaxChunks > 128 {
		return zero, localProjectError("invalid_argument", "index")
	}
	b, err := s.binding(q.Project)
	if err != nil {
		return zero, err
	}
	if b.readOnly {
		return zero, localProjectError("read_only", "project")
	}
	if _, err = s.localProjectProvider(b.profile); err != nil {
		return zero, err
	}
	if localProjectIndexPath(s.searchConfig) == "" {
		return zero, localProjectError("invalid_configuration", "cache")
	}
	snap, err := projectSnapshot(ctx, b)
	if err != nil {
		return zero, err
	}
	profile := s.searchConfig.RAG.Profiles[b.profile]
	provider := s.searchConfig.RAG.Providers[profile.Provider]
	intent := struct {
		Request  LocalProjectIndexRequest `json:"request"`
		Commit   string                   `json:"commit"`
		Profile  any                      `json:"profile"`
		Provider any                      `json:"provider"`
		Cache    string                   `json:"cache"`
	}{q.LocalProjectIndexRequest, snap.Commit(), profile, provider, localProjectIndexPath(s.searchConfig)}
	return LocalProjectPlan{PlanID: localProjectPlanID(b, intent), Project: q.Project, ProjectID: b.runtime.projectID, Operation: "index", Commit: snap.Commit(), DataBoundary: provider.DataBoundary, MaxChunks: q.MaxChunks, Effects: []string{"embed_at_most_confirmed_new_chunks", "publish_disposable_metadata_and_vectors"}}, nil
}

func (s *LocalProjects) ApplyIndexPlan(ctx context.Context, q LocalProjectIndexPlanRequest) (LocalProjectIndexResult, error) {
	plan, err := s.PlanIndex(ctx, q)
	if err != nil {
		return LocalProjectIndexResult{}, err
	}
	if q.PlanID == "" || q.PlanID != plan.PlanID {
		return LocalProjectIndexResult{}, localProjectError("stale_plan", "plan_id")
	}
	return s.indexAt(ctx, q.LocalProjectIndexRequest, plan.Commit)
}
