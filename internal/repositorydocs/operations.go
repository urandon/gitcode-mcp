package repositorydocs

import (
	"context"

	"gitcode-mcp/internal/cache"
)

type PolicyRequest struct {
	RepoID          string `json:"repo_id"`
	Revision        string `json:"revision,omitempty"`
	IncludeWorktree bool   `json:"include_worktree,omitempty"`
}

type PolicyResult struct {
	RepoID          string           `json:"repo_id"`
	CommitOID       string           `json:"commit_oid"`
	IncludeWorktree bool             `json:"include_worktree"`
	GitStoreRef     string           `json:"git_store_ref"`
	WorktreeRef     string           `json:"worktree_ref,omitempty"`
	OverlayDigest   string           `json:"overlay_digest,omitempty"`
	Policy          PolicyResolution `json:"policy"`
}

type StatusRequest = PolicyRequest

type StatusResult struct {
	PolicyResult
	RevisionSets []cache.RepositoryDocRevisionSet `json:"revision_sets"`
}

func InspectPolicy(ctx context.Context, repo *Repository, req PolicyRequest) (PolicyResult, error) {
	commitOID, policy, err := ResolvePolicy(ctx, repo, req.Revision, req.IncludeWorktree)
	if err != nil {
		return PolicyResult{}, err
	}
	overlayDigest, err := ResolveOverlayDigest(ctx, repo, commitOID, policy.PolicyHash, req.IncludeWorktree, DefaultMaxDocumentBytes)
	if err != nil {
		return PolicyResult{}, err
	}
	result := PolicyResult{RepoID: req.RepoID, CommitOID: commitOID, IncludeWorktree: req.IncludeWorktree, GitStoreRef: repo.GitStoreRef, OverlayDigest: overlayDigest, Policy: policy}
	if req.IncludeWorktree {
		result.WorktreeRef = repo.WorktreeRef
	}
	return result, nil
}

func InspectStatus(ctx context.Context, store SearchStore, repo *Repository, req StatusRequest) (StatusResult, error) {
	policy, err := InspectPolicy(ctx, repo, req)
	if err != nil {
		return StatusResult{}, err
	}
	sets, err := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: req.RepoID, GitStoreRef: policy.GitStoreRef, CommitOID: policy.CommitOID, PolicyHash: policy.Policy.PolicyHash, OverlayDigest: policy.OverlayDigest, ExactOverlay: true, ChunkPolicyID: DefaultChunkPolicyID, Limit: 20})
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{PolicyResult: policy, RevisionSets: sets}, nil
}
