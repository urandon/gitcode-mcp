package repositorydocs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	RevisionSetIdentityRevision  = "repo-doc-revision-set-identity-v1"
	ProcessingPolicyRevision     = "repo-doc-processing-policy-v1"
	DefaultChunkerRevision       = "repo-doc-markdown-chunker-v1"
	DefaultNormalizationRevision = "repo-doc-normalization-v1"
	DefaultHardExclusionRevision = "repo-doc-hard-exclusions-v1"
)

// ProcessingPolicy identifies every processing decision that can change the
// eligible document set, chunk boundaries, normalized embedding input, or
// chunk identity. Execution budgets deliberately do not belong here.
type ProcessingPolicy struct {
	Revision              string `json:"revision"`
	ChunkerRevision       string `json:"chunker_revision"`
	ChunkBytes            int    `json:"chunk_bytes"`
	NormalizationRevision string `json:"normalization_revision"`
	MaxFileBytes          int64  `json:"max_file_bytes"`
	HardExclusionRevision string `json:"hard_exclusion_revision"`
}

func DefaultProcessingPolicy() ProcessingPolicy {
	return ProcessingPolicy{
		Revision:              ProcessingPolicyRevision,
		ChunkerRevision:       DefaultChunkerRevision,
		ChunkBytes:            DefaultChunkBytes,
		NormalizationRevision: DefaultNormalizationRevision,
		MaxFileBytes:          DefaultMaxDocumentBytes,
		HardExclusionRevision: DefaultHardExclusionRevision,
	}
}

// ProcessingPolicyFor returns the exact policy used for one indexing or
// retrieval operation, applying the product defaults to omitted values.
func ProcessingPolicyFor(maxFileBytes int64, chunkBytes int) ProcessingPolicy {
	policy := DefaultProcessingPolicy()
	if maxFileBytes > 0 {
		policy.MaxFileBytes = maxFileBytes
	}
	if chunkBytes > 0 {
		policy.ChunkBytes = chunkBytes
	}
	return policy
}

func (p ProcessingPolicy) normalized() ProcessingPolicy {
	defaults := DefaultProcessingPolicy()
	if p.Revision == "" {
		p.Revision = defaults.Revision
	}
	if p.ChunkerRevision == "" {
		p.ChunkerRevision = defaults.ChunkerRevision
	}
	if p.ChunkBytes <= 0 {
		p.ChunkBytes = defaults.ChunkBytes
	}
	if p.NormalizationRevision == "" {
		p.NormalizationRevision = defaults.NormalizationRevision
	}
	if p.MaxFileBytes <= 0 {
		p.MaxFileBytes = defaults.MaxFileBytes
	}
	if p.HardExclusionRevision == "" {
		p.HardExclusionRevision = defaults.HardExclusionRevision
	}
	return p
}

func (p ProcessingPolicy) ID() string {
	p = p.normalized()
	return "repo-doc-processing-" + canonicalIdentityDigest(p)
}

// RevisionSetIdentity is the single versioned identity for reusable repository
// documentation derived state. Paths and execution controls are excluded.
type RevisionSetIdentity struct {
	Revision                     string           `json:"revision"`
	RepoID                       string           `json:"repo_id"`
	SourceRegistrationID         string           `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64            `json:"source_registration_generation,omitempty"`
	GitStoreRef                  string           `json:"git_store_ref"`
	ObjectFormat                 string           `json:"object_format"`
	CommitOID                    string           `json:"commit_oid"`
	PolicyHash                   string           `json:"policy_hash"`
	ConfigDigest                 string           `json:"config_digest,omitempty"`
	WorktreeRef                  string           `json:"worktree_ref,omitempty"`
	OverlayDigest                string           `json:"overlay_digest,omitempty"`
	Processing                   ProcessingPolicy `json:"processing"`
	NamespaceID                  string           `json:"namespace_id"`
}

// NewRevisionSetIdentity centralizes identity construction for index storage
// and daemon admission. Callers pass only semantic inputs; execution budgets
// cannot accidentally become part of the reusable-state key.
func NewRevisionSetIdentity(repoID, sourceRegistrationID string, sourceRegistrationGeneration int64, repo *Repository, commitOID string, policy PolicyResolution, overlayDigest string, processing ProcessingPolicy, namespaceID string) RevisionSetIdentity {
	worktreeRef := ""
	if repo != nil && overlayDigest != "" {
		worktreeRef = repo.WorktreeRef
	}
	identity := RevisionSetIdentity{
		RepoID: repoID, SourceRegistrationID: sourceRegistrationID, SourceRegistrationGeneration: sourceRegistrationGeneration,
		CommitOID: commitOID, PolicyHash: policy.PolicyHash, ConfigDigest: SemanticConfigDigest(policy),
		WorktreeRef: worktreeRef, OverlayDigest: overlayDigest, Processing: processing, NamespaceID: namespaceID,
	}
	if repo != nil {
		identity.GitStoreRef = repo.GitStoreRef
		identity.ObjectFormat = repo.ObjectFormat
	}
	return identity.normalized()
}

func (i RevisionSetIdentity) normalized() RevisionSetIdentity {
	if i.Revision == "" {
		i.Revision = RevisionSetIdentityRevision
	}
	i.Processing = i.Processing.normalized()
	// A worktree reference has meaning only when an overlay participates in the
	// revision. Committed-only sets remain portable across sibling worktrees.
	if i.OverlayDigest == "" {
		i.WorktreeRef = ""
	}
	return i
}

func (i RevisionSetIdentity) ID() string {
	i = i.normalized()
	return "repo-doc-set-" + canonicalIdentityDigest(i)
}

func canonicalIdentityDigest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
