package repositorydocs

import (
	"context"

	"gitcode-mcp/internal/cache"
)

type PolicyRequest struct {
	RepoID                       string `json:"repo_id"`
	RegistrationID               string `json:"registration_id,omitempty"`
	SourceRegistrationID         string `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64  `json:"source_registration_generation,omitempty"`
	Revision                     string `json:"revision,omitempty"`
	IncludeWorktree              bool   `json:"include_worktree,omitempty"`
}

type PlanRequest = PolicyRequest

type PlanResult struct {
	PolicyResult
	EffectiveInclude []string `json:"effective_include"`
	EffectiveExclude []string `json:"effective_exclude"`
	EligibleFiles    int      `json:"eligible_files"`
	EligibleBytes    int64    `json:"eligible_bytes"`
	ExcludedFiles    int      `json:"excluded_files"`
	MissingObjects   int      `json:"missing_objects"`
	TrackedChanges   int      `json:"tracked_changes,omitempty"`
}

type PolicyResult struct {
	RepoID                       string           `json:"repo_id"`
	CorpusKind                   string           `json:"corpus_kind"`
	RegistrationID               string           `json:"registration_id,omitempty"`
	SourceRegistrationID         string           `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64            `json:"source_registration_generation,omitempty"`
	RequestedRevision            string           `json:"requested_revision,omitempty"`
	EffectiveRevision            string           `json:"effective_revision"`
	Authority                    string           `json:"authority"`
	CommitOID                    string           `json:"commit_oid"`
	IncludeWorktree              bool             `json:"include_worktree"`
	GitStoreRef                  string           `json:"git_store_ref"`
	WorktreeRef                  string           `json:"worktree_ref,omitempty"`
	OverlayDigest                string           `json:"overlay_digest,omitempty"`
	Policy                       PolicyResolution `json:"policy"`
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
	overlayDigest, err := ResolveOverlayDigest(ctx, repo, commitOID, policy.Policy, req.IncludeWorktree, DefaultMaxDocumentBytes)
	if err != nil {
		return PolicyResult{}, err
	}
	authority := "git"
	if req.IncludeWorktree {
		authority = "worktree_overlay"
	}
	result := PolicyResult{
		RepoID: req.RepoID, CorpusKind: "repository_docs", RegistrationID: req.RegistrationID,
		SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: req.SourceRegistrationGeneration,
		RequestedRevision: req.Revision, EffectiveRevision: commitOID, Authority: authority,
		CommitOID: commitOID, IncludeWorktree: req.IncludeWorktree, GitStoreRef: repo.GitStoreRef, OverlayDigest: overlayDigest, Policy: policy,
	}
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
	sets, err := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: req.RepoID, SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: req.SourceRegistrationGeneration, GitStoreRef: policy.GitStoreRef, CommitOID: policy.CommitOID, PolicyHash: policy.Policy.PolicyHash, OverlayDigest: policy.OverlayDigest, ExactOverlay: true, ChunkPolicyID: DefaultProcessingPolicy().ID(), Limit: 20})
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{PolicyResult: policy, RevisionSets: sets}, nil
}

func InspectPlan(ctx context.Context, repo *Repository, req PlanRequest) (PlanResult, error) {
	policy, err := InspectPolicy(ctx, repo, req)
	if err != nil {
		return PlanResult{}, err
	}
	result := PlanResult{PolicyResult: policy}
	result.EffectiveInclude, result.EffectiveExclude = policy.Policy.Policy.EffectiveMatchers()
	err = repo.WalkTree(ctx, policy.CommitOID, func(entry TreeEntry) error {
		if !policy.Policy.Policy.Matches(entry.Path) {
			return nil
		}
		class := ClassifyDocumentContent(entry, nil)
		if class == DocumentContentSymlink || class == DocumentContentSubmodule {
			result.ExcludedFiles++
			return nil
		}
		if entry.Type != "blob" {
			return nil
		}
		if entry.Size < 0 || entry.Size > DefaultMaxDocumentBytes {
			result.ExcludedFiles++
			return nil
		}
		data, readErr := repo.ReadBlob(ctx, entry.OID, DefaultMaxDocumentBytes)
		if readErr != nil {
			result.MissingObjects++
			return nil
		}
		if ClassifyDocumentContent(entry, data) != DocumentContentRegular {
			result.ExcludedFiles++
			return nil
		}
		result.EligibleFiles++
		result.EligibleBytes += int64(len(data))
		return nil
	})
	if err != nil {
		return PlanResult{}, err
	}
	if req.IncludeWorktree {
		changes, err := repo.TrackedChangesAtFiltered(ctx, policy.CommitOID, DefaultMaxDocumentBytes, policyOverlayPathFilter(policy.Policy.Policy))
		if err != nil {
			return PlanResult{}, err
		}
		result.TrackedChanges = len(changes)
	}
	return result, nil
}
