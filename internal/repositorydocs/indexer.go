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

const (
	repositoryDocsWriterWaitBudget = 5 * time.Second
	repositoryDocsWriterBackoffMin = 5 * time.Millisecond
	repositoryDocsWriterBackoffMax = 250 * time.Millisecond
)

type IndexStore interface {
	AcquireWriter(context.Context, cache.WriterRequest) (*cache.WriterLease, error)
	ReleaseWriter(context.Context, *cache.WriterLease) error
	ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error)
	UpsertEmbeddingNamespace(context.Context, cache.EmbeddingNamespace) (cache.EmbeddingNamespace, error)
	UpsertRepositoryDocRevisionSet(context.Context, cache.RepositoryDocRevisionSet) error
	GetRepositoryDocRevisionSet(context.Context, string, string) (cache.RepositoryDocRevisionSet, error)
	UpsertRepositoryDocChunk(context.Context, cache.RepositoryDocChunk) error
	ReplaceRepositoryDocMembership(context.Context, string, string, []cache.RepositoryDocMembership) error
	UpsertRepositoryDocMembership(context.Context, cache.RepositoryDocMembership) error
	ReplaceRepositoryDocExclusions(context.Context, string, string, []cache.RepositoryDocExclusion) error
	UpsertRepositoryDocExclusion(context.Context, cache.RepositoryDocExclusion) error
	PublishStagedRepositoryDocRevisionSet(context.Context, cache.RepositoryDocRevisionSet) error
	GetRepositoryDocVector(context.Context, string, string, string) (cache.RepositoryDocVector, error)
	UpsertRepositoryDocVector(context.Context, cache.RepositoryDocVector) error
}

type IndexRequest struct {
	RepoID                       string
	SourceRegistrationID         string
	SourceRegistrationGeneration int64
	Repository                   *Repository
	Revision                     string
	IncludeWorktree              bool
	MaxFileBytes                 int64
	ChunkBytes                   int
	BatchSize                    int
	MaxChunks                    int
	EnforceExpectedSnapshot      bool
	ExpectedCommitOID            string
	ExpectedPolicyHash           string
	ExpectedConfigDigest         string
	ExpectedOverlayDigest        string
	ExpectedNamespaceID          string
	Progress                     func(IndexProgress)
}

type IndexProgress struct {
	Phase          string
	EligibleFiles  int
	EligibleChunks int
	EmbeddedChunks int
	ReusedChunks   int
	FailedChunks   int
	ExcludedFiles  int
	MissingObjects int
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
	checkpoints       VectorCheckpointStore
	now               func() time.Time
	writerWaitBudget  time.Duration
	beforeOverlayRead func(string)
}

func NewIndexer(store IndexStore, provider rag.EmbeddingProvider) *Indexer {
	return &Indexer{store: store, provider: provider, now: func() time.Time { return time.Now().UTC() }, writerWaitBudget: repositoryDocsWriterWaitBudget}
}

func (i *Indexer) WithVectorCheckpointStore(store VectorCheckpointStore) *Indexer {
	if i != nil {
		i.checkpoints = store
	}
	return i
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
	processingPolicy := ProcessingPolicyFor(req.MaxFileBytes, req.ChunkBytes)
	chunkPolicyID := processingPolicy.ID()
	commitOID, policyResolution, err := ResolvePolicy(ctx, req.Repository, req.Revision, req.IncludeWorktree)
	if err != nil {
		return IndexResult{}, err
	}
	if policyResolution.Status == PolicyStatusDisabled {
		return IndexResult{RepoID: req.RepoID, CommitOID: commitOID, PolicyHash: policyResolution.PolicyHash, State: PolicyStatusDisabled, Message: "repository documentation indexing is disabled by committed policy"}, nil
	}
	namespaceIdentity, err := i.provider.NamespaceIdentity(ctx, rag.NamespaceRequest{RepoID: req.RepoID, ChunkPolicyID: chunkPolicyID, LanguagePolicyID: rag.DefaultLanguagePolicyID, DocumentInstructionID: "repo-doc-v1", QueryInstructionID: "repo-doc-query-v1"})
	if err != nil {
		return IndexResult{}, err
	}
	namespace, ok, err := i.store.ResolveEmbeddingNamespace(ctx, namespaceIdentity)
	if err != nil {
		return IndexResult{}, err
	}
	if !ok {
		err = i.withWriter(ctx, req.RepoID, "repository-docs-namespace", func() error {
			var resolveErr error
			namespace, ok, resolveErr = i.store.ResolveEmbeddingNamespace(ctx, namespaceIdentity)
			if resolveErr != nil || ok {
				return resolveErr
			}
			now := i.now()
			namespace, resolveErr = i.store.UpsertEmbeddingNamespace(ctx, cache.EmbeddingNamespace{EmbeddingNamespaceIdentity: namespaceIdentity, CreatedAt: now, UpdatedAt: now})
			return resolveErr
		})
		if err != nil {
			return IndexResult{}, err
		}
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
	identity := NewRevisionSetIdentity(req.RepoID, req.SourceRegistrationID, req.SourceRegistrationGeneration, req.Repository, commitOID, policyResolution, overlayDigest, processingPolicy, namespace.ID)
	setID := identity.ID()
	result := IndexResult{RepoID: req.RepoID, RevisionSetID: setID, CommitOID: commitOID, PolicyHash: policyResolution.PolicyHash, NamespaceID: namespace.ID, State: cache.RepoDocSetBuilding}
	emitProgress := func(phase string) {
		if req.Progress != nil {
			req.Progress(IndexProgress{Phase: phase, EligibleFiles: result.EligibleFiles, EligibleChunks: result.EligibleChunks, EmbeddedChunks: result.EmbeddedChunks, ReusedChunks: result.ReusedChunks, FailedChunks: result.FailedChunks, ExcludedFiles: result.ExcludedFiles, MissingObjects: result.MissingObjects})
		}
	}
	emitProgress("plan")
	now := i.now()
	worktreeRef := identity.WorktreeRef
	set := cache.RepositoryDocRevisionSet{RepoID: req.RepoID, ID: setID, SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: req.SourceRegistrationGeneration, GitStoreRef: req.Repository.GitStoreRef, WorktreeRef: worktreeRef, ObjectFormat: req.Repository.ObjectFormat, CommitOID: commitOID, RequestedRevision: req.Revision, PolicyHash: policyResolution.PolicyHash, PolicySource: policyResolution.Source, ConfigDigest: policyResolution.ConfigDigest, OverlayDigest: overlayDigest, ChunkPolicyID: chunkPolicyID, ProcessingPolicyID: processingPolicy.ID(), NamespaceID: namespace.ID, State: cache.RepoDocSetBuilding, CreatedAt: now, UpdatedAt: now}
	if existing, getErr := i.store.GetRepositoryDocRevisionSet(ctx, req.RepoID, setID); getErr == nil {
		set.CreatedAt = existing.CreatedAt
		if existing.State == cache.RepoDocSetReady {
			return resultFromSet(existing), nil
		}
	} else if !errors.Is(getErr, cache.ErrNotFound) {
		return IndexResult{}, getErr
	}
	if err := i.withWriter(ctx, req.RepoID, "repository-docs-set-building", func() error {
		if err := i.store.UpsertRepositoryDocRevisionSet(ctx, set); err != nil {
			return err
		}
		if err := i.store.ReplaceRepositoryDocMembership(ctx, req.RepoID, setID, nil); err != nil {
			return err
		}
		return i.store.ReplaceRepositoryDocExclusions(ctx, req.RepoID, setID, nil)
	}); err != nil {
		if errors.Is(err, cache.ErrRepositoryDocRevisionSetPublished) {
			return i.publishedResult(ctx, req.RepoID, setID)
		}
		return IndexResult{}, err
	}
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
				if err := i.persistProviderVector(ctx, req.RepoID, namespace.ID, item.meta.ID, namespace.Dimensions, one.Embeddings[0]); err != nil {
					result.FailedChunks++
					continue
				}
				result.EmbeddedChunks++
			}
			pending = pending[:0]
			emitProgress("embed")
			return nil
		}
		for idx, vector := range response.Embeddings {
			if err := i.persistProviderVector(ctx, req.RepoID, namespace.ID, pending[idx].meta.ID, namespace.Dimensions, vector); err != nil {
				result.FailedChunks++
				continue
			}
			result.EmbeddedChunks++
		}
		pending = pending[:0]
		emitProgress("embed")
		return nil
	}
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
	walkFailureClass := ""
	walkPartial := false
	recordExclusion := func(path, authority, blobOID, reason string) error {
		exclusion := cache.RepositoryDocExclusion{RepoID: req.RepoID, RevisionSetID: setID, Path: path, Authority: authority, BlobOID: blobOID, ReasonCode: reason}
		return i.withWriter(ctx, req.RepoID, "repository-docs-exclusion", func() error {
			return i.store.UpsertRepositoryDocExclusion(ctx, exclusion)
		})
	}
	err = req.Repository.WalkTree(ctx, commitOID, func(entry TreeEntry) error {
		if err := ctx.Err(); err != nil {
			walkPartial = true
			return err
		}
		if removed[entry.Path] || changed[entry.Path].Path != "" || !policyResolution.Policy.Matches(entry.Path) {
			return nil
		}
		structuralClass := ClassifyDocumentContent(entry, nil)
		if structuralClass == DocumentContentSymlink || structuralClass == DocumentContentSubmodule {
			if err := recordExclusion(entry.Path, "git", entry.OID, string(structuralClass)); err != nil {
				walkFailureClass = "metadata_write_failed"
				return err
			}
			result.ExcludedFiles++
			return nil
		}
		if entry.Type != "blob" {
			return nil
		}
		if entry.Size < 0 || entry.Size > req.MaxFileBytes {
			if err := recordExclusion(entry.Path, "git", entry.OID, "document_too_large"); err != nil {
				walkFailureClass = "metadata_write_failed"
				return err
			}
			result.ExcludedFiles++
			return nil
		}
		data, readErr := req.Repository.ReadBlob(ctx, entry.OID, req.MaxFileBytes)
		if readErr != nil {
			if err := recordExclusion(entry.Path, "git", entry.OID, "git_object_unavailable"); err != nil {
				walkFailureClass = "metadata_write_failed"
				return err
			}
			result.ExcludedFiles++
			result.MissingObjects++
			return nil
		}
		contentClass := ClassifyDocumentContent(entry, data)
		if contentClass != DocumentContentRegular {
			if err := recordExclusion(entry.Path, "git", entry.OID, string(contentClass)); err != nil {
				walkFailureClass = "metadata_write_failed"
				return err
			}
			result.ExcludedFiles++
			return nil
		}
		chunks, chunkErr := ChunkDocumentWithPolicy(digestBytes(data), data, processingPolicy)
		if chunkErr != nil {
			if err := recordExclusion(entry.Path, "git", entry.OID, "chunking_failed"); err != nil {
				walkFailureClass = "metadata_write_failed"
				return err
			}
			result.ExcludedFiles++
			return nil
		}
		result.EligibleFiles++
		for ordinal, chunk := range chunks {
			meta := cache.RepositoryDocChunk{RepoID: req.RepoID, ID: chunk.ID, ObjectFormat: req.Repository.ObjectFormat, BlobOID: entry.OID, ContentDigest: chunk.RawSliceDigest, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, RawSliceDigest: chunk.RawSliceDigest, EmbeddingInputDigest: chunk.EmbeddingInputDigest, ChunkPolicyID: chunkPolicyID, CreatedAt: now, UpdatedAt: i.now()}
			membership := cache.RepositoryDocMembership{RepoID: req.RepoID, RevisionSetID: setID, Path: entry.Path, ChunkID: chunk.ID, Authority: "git", Ordinal: ordinal, BlobOID: entry.OID, ContentDigest: chunk.RawSliceDigest}
			if err := i.withWriter(ctx, req.RepoID, "repository-docs-chunk", func() error {
				if err := i.store.UpsertRepositoryDocChunk(ctx, meta); err != nil {
					return err
				}
				return i.store.UpsertRepositoryDocMembership(ctx, membership)
			}); err != nil {
				walkFailureClass = "metadata_write_failed"
				return err
			}
			result.EligibleChunks++
			if vector, vectorErr := i.store.GetRepositoryDocVector(ctx, req.RepoID, namespace.ID, chunk.ID); vectorErr == nil && vector.Dimensions == namespace.Dimensions && vector.DType == namespace.DType {
				i.deleteVectorCheckpoint(ctx, req.RepoID, namespace.ID, chunk.ID)
				result.ReusedChunks++
				continue
			} else if vectorErr != nil && !errors.Is(vectorErr, cache.ErrNotFound) {
				walkFailureClass = "vector_read_failed"
				return vectorErr
			}
			if replayed, replayErr := i.replayVectorCheckpoint(ctx, req.RepoID, namespace.ID, chunk.ID, namespace.Dimensions, namespace.DType); replayErr != nil {
				walkFailureClass = "vector_checkpoint_replay_failed"
				return replayErr
			} else if replayed {
				result.EmbeddedChunks++
				continue
			}
			if req.MaxChunks > 0 && attemptedChunks >= req.MaxChunks {
				result.FailedChunks++
				continue
			}
			pending = append(pending, pendingChunk{meta: meta, text: chunk.Text})
			attemptedChunks++
			if len(pending) >= req.BatchSize {
				if err := flushPending(); err != nil {
					walkPartial = true
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		if walkPartial || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return i.partial(ctx, set, result, err)
		}
		if walkFailureClass != "" {
			return i.fail(ctx, set, result, walkFailureClass, err)
		}
		return i.fail(ctx, set, result, "git_tree_unavailable", err)
	}
	emitProgress("walk")
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
		contentClass := ClassifyDocumentContent(TreeEntry{Mode: "100644", Type: "blob"}, data)
		if contentClass != DocumentContentRegular {
			if err := recordExclusion(change.Path, "worktree", "", string(contentClass)); err != nil {
				return i.fail(ctx, set, result, "metadata_write_failed", err)
			}
			result.ExcludedFiles++
			continue
		}
		chunks, chunkErr := ChunkDocumentWithPolicy(contentDigest, data, processingPolicy)
		if chunkErr != nil {
			if err := recordExclusion(change.Path, "worktree", "", "chunking_failed"); err != nil {
				return i.fail(ctx, set, result, "metadata_write_failed", err)
			}
			result.ExcludedFiles++
			continue
		}
		result.EligibleFiles++
		for ordinal, chunk := range chunks {
			meta := cache.RepositoryDocChunk{RepoID: req.RepoID, ID: chunk.ID, ObjectFormat: req.Repository.ObjectFormat, WorktreeRef: req.Repository.WorktreeRef, ContentDigest: chunk.RawSliceDigest, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, RawSliceDigest: chunk.RawSliceDigest, EmbeddingInputDigest: chunk.EmbeddingInputDigest, ChunkPolicyID: chunkPolicyID, CreatedAt: now, UpdatedAt: i.now()}
			membership := cache.RepositoryDocMembership{RepoID: req.RepoID, RevisionSetID: setID, Path: change.Path, ChunkID: chunk.ID, Authority: "worktree", Ordinal: ordinal, WorktreeRef: req.Repository.WorktreeRef, ContentDigest: chunk.RawSliceDigest}
			if err := i.withWriter(ctx, req.RepoID, "repository-docs-chunk", func() error {
				if err := i.store.UpsertRepositoryDocChunk(ctx, meta); err != nil {
					return err
				}
				return i.store.UpsertRepositoryDocMembership(ctx, membership)
			}); err != nil {
				return i.fail(ctx, set, result, "metadata_write_failed", err)
			}
			result.EligibleChunks++
			if vector, vectorErr := i.store.GetRepositoryDocVector(ctx, req.RepoID, namespace.ID, chunk.ID); vectorErr == nil && vector.Dimensions == namespace.Dimensions && vector.DType == namespace.DType {
				i.deleteVectorCheckpoint(ctx, req.RepoID, namespace.ID, chunk.ID)
				result.ReusedChunks++
				continue
			} else if vectorErr != nil && !errors.Is(vectorErr, cache.ErrNotFound) {
				return i.fail(ctx, set, result, "vector_read_failed", vectorErr)
			}
			if replayed, replayErr := i.replayVectorCheckpoint(ctx, req.RepoID, namespace.ID, chunk.ID, namespace.Dimensions, namespace.DType); replayErr != nil {
				return i.fail(ctx, set, result, "vector_checkpoint_replay_failed", replayErr)
			} else if replayed {
				result.EmbeddedChunks++
				continue
			}
			if req.MaxChunks > 0 && attemptedChunks >= req.MaxChunks {
				result.FailedChunks++
				continue
			}
			pending = append(pending, pendingChunk{meta: meta, text: chunk.Text})
			attemptedChunks++
			if len(pending) >= req.BatchSize {
				if err := flushPending(); err != nil {
					return i.partial(ctx, set, result, err)
				}
			}
		}
	}
	if err := flushPending(); err != nil {
		return i.partial(ctx, set, result, err)
	}
	emitProgress("publish")
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
	if err := i.withWriter(ctx, req.RepoID, "repository-docs-publish", func() error {
		return i.store.PublishStagedRepositoryDocRevisionSet(ctx, set)
	}); err != nil {
		if errors.Is(err, cache.ErrRepositoryDocRevisionSetPublished) {
			return i.publishedResult(ctx, req.RepoID, setID)
		}
		return IndexResult{}, err
	}
	emitProgress("published")
	return result, nil
}

func (i *Indexer) superseded(ctx context.Context, set cache.RepositoryDocRevisionSet, result IndexResult, message string) (IndexResult, error) {
	result.State = cache.RepoDocSetSuperseded
	result.Message = message + "; reindex the current overlay"
	set = mergeIndexResult(set, result, i.now())
	set.LastErrorClass = "worktree_overlay_superseded"
	if err := i.withWriter(context.WithoutCancel(ctx), result.RepoID, "repository-docs-superseded", func() error {
		return i.store.UpsertRepositoryDocRevisionSet(context.WithoutCancel(ctx), set)
	}); err != nil {
		return result, fmt.Errorf("repository docs: persist superseded revision set: %w", err)
	}
	return result, nil
}

func (i *Indexer) writeVector(ctx context.Context, repoID, namespaceID, chunkID string, vector []float32) error {
	blob, err := rag.EncodeNormalizedFloat32Vector(vector)
	if err != nil {
		return err
	}
	return i.writeEncodedVector(ctx, cache.RepositoryDocVector{RepoID: repoID, NamespaceID: namespaceID, ChunkID: chunkID, Vector: blob, Dimensions: len(vector), DType: rag.DefaultEmbeddingDType, EmbeddedAt: i.now()})
}

func (i *Indexer) writeEncodedVector(ctx context.Context, vector cache.RepositoryDocVector) error {
	return i.withWriter(ctx, vector.RepoID, "repository-docs-vector", func() error {
		return i.store.UpsertRepositoryDocVector(ctx, vector)
	})
}

func (i *Indexer) persistProviderVector(ctx context.Context, repoID, namespaceID, chunkID string, expectedDimensions int, vector []float32) error {
	if expectedDimensions <= 0 || len(vector) != expectedDimensions {
		return fmt.Errorf("repository docs: embedding provider returned %d dimensions, want %d", len(vector), expectedDimensions)
	}
	blob, err := rag.EncodeNormalizedFloat32Vector(vector)
	if err != nil {
		return err
	}
	stored := cache.RepositoryDocVector{RepoID: repoID, NamespaceID: namespaceID, ChunkID: chunkID, Vector: blob, Dimensions: len(vector), DType: rag.DefaultEmbeddingDType, EmbeddedAt: i.now()}
	var checkpointErr error
	if i.checkpoints != nil {
		// The provider has already completed the expensive/non-repeatable side of
		// the boundary. Persist its vector even when the request is cancelled so a
		// later admitted writer can replay it without another provider call.
		checkpointErr = i.checkpoints.Save(context.WithoutCancel(ctx), VectorCheckpoint{RepoID: repoID, NamespaceID: namespaceID, ChunkID: chunkID, Vector: blob, Dimensions: len(vector), DType: rag.DefaultEmbeddingDType, CreatedAt: stored.EmbeddedAt})
	}
	writeErr := i.writeEncodedVector(ctx, stored)
	if writeErr != nil {
		return errors.Join(checkpointErr, writeErr)
	}
	if checkpointErr != nil {
		// The primary vector publication is already durable, so a failed auxiliary
		// handoff must not turn complete coverage into a false failed chunk.
		return nil
	}
	i.deleteVectorCheckpoint(ctx, repoID, namespaceID, chunkID)
	return nil
}

func (i *Indexer) replayVectorCheckpoint(ctx context.Context, repoID, namespaceID, chunkID string, dimensions int, dtype string) (bool, error) {
	if i.checkpoints == nil {
		return false, nil
	}
	checkpoint, err := i.checkpoints.Load(ctx, repoID, namespaceID, chunkID)
	if errors.Is(err, ErrVectorCheckpointNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if checkpoint.Dimensions != dimensions || checkpoint.DType != dtype {
		return false, VectorCheckpointPersistenceError{operation: "validate", cause: errors.New("checkpoint namespace contract changed")}
	}
	if err := i.writeEncodedVector(ctx, cache.RepositoryDocVector{RepoID: repoID, NamespaceID: namespaceID, ChunkID: chunkID, Vector: checkpoint.Vector, Dimensions: checkpoint.Dimensions, DType: checkpoint.DType, EmbeddedAt: checkpoint.CreatedAt}); err != nil {
		return false, err
	}
	i.deleteVectorCheckpoint(ctx, repoID, namespaceID, chunkID)
	return true, nil
}

func (i *Indexer) deleteVectorCheckpoint(ctx context.Context, repoID, namespaceID, chunkID string) {
	if i.checkpoints != nil {
		_ = i.checkpoints.Delete(context.WithoutCancel(ctx), repoID, namespaceID, chunkID)
	}
}

func (i *Indexer) withWriter(ctx context.Context, repoID, operation string, fn func() error) (err error) {
	budget := i.writerWaitBudget
	if budget <= 0 {
		budget = repositoryDocsWriterWaitBudget
	}
	deadline := time.Now().Add(budget)
	backoff := repositoryDocsWriterBackoffMin
	var lease *cache.WriterLease
	var lastContention error
	for {
		lease, err = i.store.AcquireWriter(ctx, cache.WriterRequest{Operation: operation, RepoID: repoID})
		if err == nil {
			break
		}
		var contention cache.ErrLockContention
		if !errors.As(err, &contention) {
			return err
		}
		lastContention = err
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return lastContention
		}
		wait := backoff
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < repositoryDocsWriterBackoffMax {
			backoff *= 2
			if backoff > repositoryDocsWriterBackoffMax {
				backoff = repositoryDocsWriterBackoffMax
			}
		}
	}
	defer func() {
		if releaseErr := i.store.ReleaseWriter(context.Background(), lease); err == nil {
			err = releaseErr
		}
	}()
	return fn()
}

func (i *Indexer) publishedResult(ctx context.Context, repoID, setID string) (IndexResult, error) {
	set, err := i.store.GetRepositoryDocRevisionSet(ctx, repoID, setID)
	if err != nil {
		return IndexResult{}, err
	}
	if set.State != cache.RepoDocSetReady {
		return IndexResult{}, cache.ErrRepositoryDocRevisionSetPublished
	}
	return resultFromSet(set), nil
}

func (i *Indexer) fail(ctx context.Context, set cache.RepositoryDocRevisionSet, result IndexResult, class string, cause error) (IndexResult, error) {
	result.State = cache.RepoDocSetBlocked
	result.Message = class
	set = mergeIndexResult(set, result, i.now())
	set.LastErrorClass = class
	persistErr := i.withWriter(context.WithoutCancel(ctx), result.RepoID, "repository-docs-blocked", func() error {
		return i.store.UpsertRepositoryDocRevisionSet(context.WithoutCancel(ctx), set)
	})
	if persistErr != nil {
		return result, errors.Join(cause, fmt.Errorf("repository docs: persist blocked revision set: %w", persistErr))
	}
	return result, cause
}

func (i *Indexer) partial(ctx context.Context, set cache.RepositoryDocRevisionSet, result IndexResult, cause error) (IndexResult, error) {
	result.State = cache.RepoDocSetPartial
	result.Message = "indexing interrupted; revision set is resumable"
	set = mergeIndexResult(set, result, i.now())
	set.LastErrorClass = "interrupted"
	persistErr := i.withWriter(context.WithoutCancel(ctx), result.RepoID, "repository-docs-partial", func() error {
		return i.store.PublishStagedRepositoryDocRevisionSet(context.WithoutCancel(ctx), set)
	})
	if persistErr != nil {
		return result, errors.Join(cause, fmt.Errorf("repository docs: persist partial revision set: %w", persistErr))
	}
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

// SemanticConfigDigest includes the committed configuration generation only
// when repository_docs was explicitly configured. An unrelated config file
// that uses the built-in corpus policy does not churn derived-state identity.
func SemanticConfigDigest(policy PolicyResolution) string {
	if policy.Source == PolicySourceBuiltin {
		return ""
	}
	return policy.ConfigDigest
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
