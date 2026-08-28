package repositorydocs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
)

type IndexStore interface {
	ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error)
	UpsertEmbeddingNamespace(context.Context, cache.EmbeddingNamespace) (cache.EmbeddingNamespace, error)
	UpsertRepositoryDocRevisionSet(context.Context, cache.RepositoryDocRevisionSet) error
	GetRepositoryDocRevisionSet(context.Context, string, string) (cache.RepositoryDocRevisionSet, error)
	UpsertRepositoryDocChunk(context.Context, cache.RepositoryDocChunk) error
	ReplaceRepositoryDocMembership(context.Context, string, string, []cache.RepositoryDocMembership) error
	GetRepositoryDocVector(context.Context, string, string, string) (cache.RepositoryDocVector, error)
	UpsertRepositoryDocVector(context.Context, cache.RepositoryDocVector) error
}

type IndexRequest struct {
	RepoID                  string
	Repository              *Repository
	Revision                string
	IncludeWorktree         bool
	MaxFileBytes            int64
	ChunkBytes              int
	BatchSize               int
	MaxChunks               int
	EnforceExpectedSnapshot bool
	ExpectedCommitOID       string
	ExpectedPolicyHash      string
	ExpectedConfigDigest    string
	ExpectedOverlayDigest   string
	ExpectedNamespaceID     string
}

type IndexResult struct {
	RepoID         string `json:"repo_id"`
	RevisionSetID  string `json:"revision_set_id"`
	CommitOID      string `json:"commit_oid"`
	PolicyHash     string `json:"policy_hash"`
	NamespaceID    string `json:"namespace_id"`
	State          string `json:"state"`
	EligibleFiles  int    `json:"eligible_files"`
	EligibleChunks int    `json:"eligible_chunks"`
	EmbeddedChunks int    `json:"embedded_chunks"`
	ReusedChunks   int    `json:"reused_chunks"`
	FailedChunks   int    `json:"failed_chunks"`
	ExcludedFiles  int    `json:"excluded_files"`
	MissingObjects int    `json:"missing_objects"`
	GCRevisionSets int64  `json:"gc_revision_sets_deleted,omitempty"`
	GCChunks       int64  `json:"gc_chunks_deleted,omitempty"`
	GCVectors      int64  `json:"gc_vectors_deleted,omitempty"`
	GCBytesBefore  int64  `json:"gc_vector_bytes_before,omitempty"`
	GCBytesAfter   int64  `json:"gc_vector_bytes_after,omitempty"`
	Message        string `json:"message,omitempty"`
}

type Indexer struct {
	store             IndexStore
	provider          rag.EmbeddingProvider
	now               func() time.Time
	beforeOverlayRead func(string)
}

func NewIndexer(store IndexStore, provider rag.EmbeddingProvider) *Indexer {
	return &Indexer{store: store, provider: provider, now: func() time.Time { return time.Now().UTC() }}
}

func (i *Indexer) Run(ctx context.Context, req IndexRequest) (IndexResult, error) {
	if i == nil || i.store == nil || i.provider == nil || req.Repository == nil || strings.TrimSpace(req.RepoID) == "" {
		return IndexResult{}, fmt.Errorf("repository docs: indexer requires repo id, Git repository, cache, and embedding provider")
	}
	if req.MaxFileBytes <= 0 {
		req.MaxFileBytes = DefaultMaxDocumentBytes
	}
	if req.ChunkBytes <= 0 {
		req.ChunkBytes = DefaultChunkBytes
	}
	if req.BatchSize <= 0 {
		req.BatchSize = i.provider.Profile().BatchSize
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 16
	}
	commitOID, policyResolution, err := ResolvePolicy(ctx, req.Repository, req.Revision, req.IncludeWorktree)
	if err != nil {
		return IndexResult{}, err
	}
	if policyResolution.Status == PolicyStatusDisabled {
		return IndexResult{RepoID: req.RepoID, CommitOID: commitOID, PolicyHash: policyResolution.PolicyHash, State: PolicyStatusDisabled, Message: "repository documentation indexing is disabled by committed policy"}, nil
	}
	namespace, err := rag.EnsureEmbeddingNamespace(ctx, i.store, i.provider, rag.NamespaceRequest{RepoID: req.RepoID, ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: rag.DefaultLanguagePolicyID, DocumentInstructionID: "repo-doc-v1", QueryInstructionID: "repo-doc-query-v1"})
	if err != nil {
		return IndexResult{}, err
	}
	overlayDigest := ""
	var initialChanges []WorktreeChange
	if req.IncludeWorktree {
		initialChanges, err = req.Repository.TrackedChangesAtFiltered(ctx, commitOID, req.MaxFileBytes, policyOverlayPathFilter(policyResolution.Policy))
		if err != nil {
			return IndexResult{}, err
		}
		overlayDigest = worktreeOverlayDigest(req.Repository.WorktreeRef, commitOID, policyResolution.PolicyHash, initialChanges)
	}
	if req.EnforceExpectedSnapshot && (commitOID != req.ExpectedCommitOID || policyResolution.PolicyHash != req.ExpectedPolicyHash || policyResolution.ConfigDigest != req.ExpectedConfigDigest || overlayDigest != req.ExpectedOverlayDigest || namespace.ID != req.ExpectedNamespaceID) {
		return IndexResult{}, &IndexSnapshotStaleError{}
	}
	setID := revisionSetID(req.RepoID, req.Repository.GitStoreRef, commitOID, policyResolution.PolicyHash, overlayDigest, DefaultChunkPolicyID, namespace.ID)
	result := IndexResult{RepoID: req.RepoID, RevisionSetID: setID, CommitOID: commitOID, PolicyHash: policyResolution.PolicyHash, NamespaceID: namespace.ID, State: cache.RepoDocSetBuilding}
	now := i.now()
	worktreeRef := ""
	if req.IncludeWorktree {
		worktreeRef = req.Repository.WorktreeRef
	}
	set := cache.RepositoryDocRevisionSet{RepoID: req.RepoID, ID: setID, GitStoreRef: req.Repository.GitStoreRef, WorktreeRef: worktreeRef, ObjectFormat: req.Repository.ObjectFormat, CommitOID: commitOID, RequestedRevision: req.Revision, PolicyHash: policyResolution.PolicyHash, PolicySource: policyResolution.Source, ConfigDigest: policyResolution.ConfigDigest, OverlayDigest: overlayDigest, ChunkPolicyID: DefaultChunkPolicyID, NamespaceID: namespace.ID, State: cache.RepoDocSetBuilding, CreatedAt: now, UpdatedAt: now}
	if existing, getErr := i.store.GetRepositoryDocRevisionSet(ctx, req.RepoID, setID); getErr == nil {
		set.CreatedAt = existing.CreatedAt
		if existing.State == cache.RepoDocSetReady {
			return resultFromSet(existing), nil
		}
	} else if !errors.Is(getErr, cache.ErrNotFound) {
		return IndexResult{}, getErr
	}
	if err := i.store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
		return IndexResult{}, err
	}
	entries, err := req.Repository.ListTree(ctx, commitOID)
	if err != nil {
		return i.fail(ctx, set, result, "git_tree_unavailable", err)
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Path < entries[b].Path })
	type pendingChunk struct {
		meta cache.RepositoryDocChunk
		text string
	}
	var pending []pendingChunk
	attemptedChunks := 0
	flushPending := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		inputs := make([]string, len(pending))
		for idx := range pending {
			inputs[idx] = pending[idx].text
		}
		response, embedErr := i.provider.Embed(ctx, rag.EmbedRequest{Inputs: inputs})
		if embedErr != nil || len(response.Embeddings) != len(pending) {
			for _, item := range pending {
				if err := ctx.Err(); err != nil {
					return err
				}
				one, oneErr := i.provider.Embed(ctx, rag.EmbedRequest{Inputs: []string{item.text}})
				if oneErr != nil || len(one.Embeddings) != 1 {
					result.FailedChunks++
					continue
				}
				if err := i.writeVector(ctx, req.RepoID, namespace.ID, item.meta.ID, one.Embeddings[0]); err != nil {
					result.FailedChunks++
					continue
				}
				result.EmbeddedChunks++
			}
			pending = pending[:0]
			return nil
		}
		for idx, vector := range response.Embeddings {
			if err := i.writeVector(ctx, req.RepoID, namespace.ID, pending[idx].meta.ID, vector); err != nil {
				result.FailedChunks++
				continue
			}
			result.EmbeddedChunks++
		}
		pending = pending[:0]
		return nil
	}
	var memberships []cache.RepositoryDocMembership
	changed := map[string]WorktreeChange{}
	removed := map[string]bool{}
	for _, change := range initialChanges {
		changed[change.Path] = change
		if change.Deleted {
			removed[change.Path] = true
		}
		if change.Renamed && change.OldPath != "" {
			removed[change.OldPath] = true
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return i.partial(ctx, set, result, memberships, err)
		}
		if entry.Type != "blob" || entry.Mode == "120000" || removed[entry.Path] || changed[entry.Path].Path != "" || !policyResolution.Policy.Matches(entry.Path) {
			continue
		}
		if entry.Size < 0 || entry.Size > req.MaxFileBytes {
			result.ExcludedFiles++
			continue
		}
		data, readErr := req.Repository.ReadBlob(ctx, entry.OID, req.MaxFileBytes)
		if readErr != nil {
			result.MissingObjects++
			continue
		}
		chunks, chunkErr := ChunkDocument(digestBytes(data), data, req.ChunkBytes)
		if chunkErr != nil {
			result.ExcludedFiles++
			continue
		}
		result.EligibleFiles++
		for ordinal, chunk := range chunks {
			meta := cache.RepositoryDocChunk{RepoID: req.RepoID, ID: chunk.ID, ObjectFormat: req.Repository.ObjectFormat, BlobOID: entry.OID, ContentDigest: chunk.RawSliceDigest, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, RawSliceDigest: chunk.RawSliceDigest, EmbeddingInputDigest: chunk.EmbeddingInputDigest, ChunkPolicyID: DefaultChunkPolicyID, CreatedAt: now, UpdatedAt: i.now()}
			if err := i.store.UpsertRepositoryDocChunk(ctx, meta); err != nil {
				return i.fail(ctx, set, result, "metadata_write_failed", err)
			}
			memberships = append(memberships, cache.RepositoryDocMembership{RepoID: req.RepoID, RevisionSetID: setID, Path: entry.Path, ChunkID: chunk.ID, Authority: "git", Ordinal: ordinal, BlobOID: entry.OID, ContentDigest: chunk.RawSliceDigest})
			result.EligibleChunks++
			if vector, vectorErr := i.store.GetRepositoryDocVector(ctx, req.RepoID, namespace.ID, chunk.ID); vectorErr == nil && vector.Dimensions == namespace.Dimensions && vector.DType == namespace.DType {
				result.ReusedChunks++
				continue
			} else if vectorErr != nil && !errors.Is(vectorErr, cache.ErrNotFound) {
				return i.fail(ctx, set, result, "vector_read_failed", vectorErr)
			}
			if req.MaxChunks > 0 && attemptedChunks >= req.MaxChunks {
				result.FailedChunks++
				continue
			}
			pending = append(pending, pendingChunk{meta: meta, text: chunk.Text})
			attemptedChunks++
			if len(pending) >= req.BatchSize {
				if err := flushPending(); err != nil {
					return i.partial(ctx, set, result, memberships, err)
				}
			}
		}
	}
	for _, change := range initialChanges {
		if change.Deleted || !policyResolution.Policy.Matches(change.Path) {
			continue
		}
		if i.beforeOverlayRead != nil {
			i.beforeOverlayRead(change.Path)
		}
		data, readErr := req.Repository.ReadTrackedWorktreeFile(ctx, change.Path, req.MaxFileBytes)
		if readErr != nil {
			return i.superseded(ctx, set, result, "tracked documentation became unreadable after the overlay snapshot")
		}
		if int64(len(data)) != change.Size || digestBytes(data) != change.Digest {
			return i.superseded(ctx, set, result, "worktree changed while its documentation bytes were read")
		}
		contentDigest := digestBytes(data)
		chunks, chunkErr := ChunkDocument(contentDigest, data, req.ChunkBytes)
		if chunkErr != nil {
			result.ExcludedFiles++
			continue
		}
		result.EligibleFiles++
		for ordinal, chunk := range chunks {
			meta := cache.RepositoryDocChunk{RepoID: req.RepoID, ID: chunk.ID, ObjectFormat: req.Repository.ObjectFormat, WorktreeRef: req.Repository.WorktreeRef, ContentDigest: chunk.RawSliceDigest, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, RawSliceDigest: chunk.RawSliceDigest, EmbeddingInputDigest: chunk.EmbeddingInputDigest, ChunkPolicyID: DefaultChunkPolicyID, CreatedAt: now, UpdatedAt: i.now()}
			if err := i.store.UpsertRepositoryDocChunk(ctx, meta); err != nil {
				return i.fail(ctx, set, result, "metadata_write_failed", err)
			}
			memberships = append(memberships, cache.RepositoryDocMembership{RepoID: req.RepoID, RevisionSetID: setID, Path: change.Path, ChunkID: chunk.ID, Authority: "worktree", Ordinal: ordinal, ContentDigest: chunk.RawSliceDigest})
			result.EligibleChunks++
			if vector, vectorErr := i.store.GetRepositoryDocVector(ctx, req.RepoID, namespace.ID, chunk.ID); vectorErr == nil && vector.Dimensions == namespace.Dimensions && vector.DType == namespace.DType {
				result.ReusedChunks++
				continue
			} else if vectorErr != nil && !errors.Is(vectorErr, cache.ErrNotFound) {
				return i.fail(ctx, set, result, "vector_read_failed", vectorErr)
			}
			if req.MaxChunks > 0 && attemptedChunks >= req.MaxChunks {
				result.FailedChunks++
				continue
			}
			pending = append(pending, pendingChunk{meta: meta, text: chunk.Text})
			attemptedChunks++
			if len(pending) >= req.BatchSize {
				if err := flushPending(); err != nil {
					return i.partial(ctx, set, result, memberships, err)
				}
			}
		}
	}
	if err := flushPending(); err != nil {
		return i.partial(ctx, set, result, memberships, err)
	}
	if err := i.store.ReplaceRepositoryDocMembership(ctx, req.RepoID, setID, memberships); err != nil {
		return i.fail(ctx, set, result, "membership_write_failed", err)
	}
	if req.IncludeWorktree {
		finalCommitOID, finalPolicy, policyErr := ResolvePolicy(ctx, req.Repository, req.Revision, true)
		finalChanges, changeErr := req.Repository.TrackedChangesAtFiltered(ctx, commitOID, req.MaxFileBytes, policyOverlayPathFilter(policyResolution.Policy))
		if policyErr != nil || changeErr != nil || finalCommitOID != commitOID || finalPolicy.PolicyHash != policyResolution.PolicyHash || finalPolicy.ConfigDigest != policyResolution.ConfigDigest || worktreeOverlayDigest(req.Repository.WorktreeRef, commitOID, policyResolution.PolicyHash, finalChanges) != overlayDigest {
			return i.superseded(ctx, set, result, "worktree or repository documentation policy changed during indexing")
		}
	}
	result.State = cache.RepoDocSetReady
	if result.FailedChunks > 0 || result.MissingObjects > 0 || result.EmbeddedChunks+result.ReusedChunks != result.EligibleChunks {
		result.State = cache.RepoDocSetPartial
	}
	set = mergeIndexResult(set, result, i.now())
	if result.State == cache.RepoDocSetReady {
		set.CompletedAt = set.UpdatedAt
	}
	if err := i.store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
		return IndexResult{}, err
	}
	return result, nil
}

func (i *Indexer) superseded(ctx context.Context, set cache.RepositoryDocRevisionSet, result IndexResult, message string) (IndexResult, error) {
	result.State = cache.RepoDocSetSuperseded
	result.Message = message + "; reindex the current overlay"
	set = mergeIndexResult(set, result, i.now())
	set.LastErrorClass = "worktree_overlay_superseded"
	_ = i.store.UpsertRepositoryDocRevisionSet(context.WithoutCancel(ctx), set)
	return result, nil
}

func (i *Indexer) writeVector(ctx context.Context, repoID, namespaceID, chunkID string, vector []float32) error {
	blob, err := rag.EncodeNormalizedFloat32Vector(vector)
	if err != nil {
		return err
	}
	return i.store.UpsertRepositoryDocVector(ctx, cache.RepositoryDocVector{RepoID: repoID, NamespaceID: namespaceID, ChunkID: chunkID, Vector: blob, Dimensions: len(vector), DType: rag.DefaultEmbeddingDType, EmbeddedAt: i.now()})
}

func (i *Indexer) fail(ctx context.Context, set cache.RepositoryDocRevisionSet, result IndexResult, class string, cause error) (IndexResult, error) {
	result.State = cache.RepoDocSetBlocked
	result.Message = class
	set = mergeIndexResult(set, result, i.now())
	set.LastErrorClass = class
	_ = i.store.UpsertRepositoryDocRevisionSet(context.WithoutCancel(ctx), set)
	return result, cause
}

func (i *Indexer) partial(ctx context.Context, set cache.RepositoryDocRevisionSet, result IndexResult, memberships []cache.RepositoryDocMembership, cause error) (IndexResult, error) {
	result.State = cache.RepoDocSetPartial
	result.Message = "indexing interrupted; revision set is resumable"
	_ = i.store.ReplaceRepositoryDocMembership(context.WithoutCancel(ctx), result.RepoID, result.RevisionSetID, memberships)
	set = mergeIndexResult(set, result, i.now())
	set.LastErrorClass = "interrupted"
	_ = i.store.UpsertRepositoryDocRevisionSet(context.WithoutCancel(ctx), set)
	return result, cause
}

func mergeIndexResult(set cache.RepositoryDocRevisionSet, result IndexResult, now time.Time) cache.RepositoryDocRevisionSet {
	set.State = result.State
	set.EligibleFiles = result.EligibleFiles
	set.EligibleChunks = result.EligibleChunks
	set.EmbeddedChunks = result.EmbeddedChunks
	set.ReusedChunks = result.ReusedChunks
	set.FailedChunks = result.FailedChunks
	set.ExcludedFiles = result.ExcludedFiles
	set.MissingObjects = result.MissingObjects
	set.UpdatedAt = now
	return set
}

func resultFromSet(set cache.RepositoryDocRevisionSet) IndexResult {
	return IndexResult{RepoID: set.RepoID, RevisionSetID: set.ID, CommitOID: set.CommitOID, PolicyHash: set.PolicyHash, NamespaceID: set.NamespaceID, State: set.State, EligibleFiles: set.EligibleFiles, EligibleChunks: set.EligibleChunks, EmbeddedChunks: set.EmbeddedChunks, ReusedChunks: set.ReusedChunks, FailedChunks: set.FailedChunks, ExcludedFiles: set.ExcludedFiles, MissingObjects: set.MissingObjects}
}

func revisionSetID(repoID, gitStoreRef, commitOID, policyHash, overlayDigest, chunkPolicyID, namespaceID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{repoID, gitStoreRef, commitOID, policyHash, overlayDigest, chunkPolicyID, namespaceID}, "\x00")))
	return "repo-doc-set-" + hex.EncodeToString(sum[:])
}

func worktreeOverlayDigest(worktreeRef, commitOID, policyHash string, changes []WorktreeChange) string {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].OldPath < changes[j].OldPath
	})
	parts := []string{worktreeRef, commitOID, policyHash}
	for _, change := range changes {
		parts = append(parts, change.Path, change.OldPath, string([]byte{change.IndexState, change.TreeState}), fmt.Sprint(change.Deleted), change.Digest, fmt.Sprint(change.Size))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "overlay-" + hex.EncodeToString(sum[:])
}

// ResolveOverlayDigest returns the identity of the current explicit tracked
// worktree overlay. Committed-only callers receive an empty digest.
func ResolveOverlayDigest(ctx context.Context, repo *Repository, commitOID string, policy Policy, includeWorktree bool, maxBytes int64) (string, error) {
	if !includeWorktree {
		return "", nil
	}
	changes, err := repo.TrackedChangesAtFiltered(ctx, commitOID, maxBytes, policyOverlayPathFilter(policy))
	if err != nil {
		return "", err
	}
	return worktreeOverlayDigest(repo.WorktreeRef, commitOID, PolicyHash(policy), changes), nil
}

func policyOverlayPathFilter(policy Policy) func(string) bool {
	return func(repoPath string) bool {
		return repoPath == PolicyConfigPath || policy.Matches(repoPath)
	}
}
