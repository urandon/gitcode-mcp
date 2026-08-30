package repositorydocs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
)

const (
	SearchModeFullText = "fulltext"
	SearchModeHybrid   = "hybrid"
)

type SearchStore interface {
	GetRepositoryDocRevisionSet(context.Context, string, string) (cache.RepositoryDocRevisionSet, error)
	ListRepositoryDocRevisionSets(context.Context, cache.RepositoryDocRevisionSetFilter) ([]cache.RepositoryDocRevisionSet, error)
	LoadRepositoryDocSearchSnapshot(context.Context, string, string, string) (cache.RepositoryDocSearchSnapshot, error)
}

type SearchRequest struct {
	RepoID                       string      `json:"repo_id"`
	RegistrationID               string      `json:"registration_id,omitempty"`
	SourceRegistrationID         string      `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64       `json:"source_registration_generation,omitempty"`
	Repository                   *Repository `json:"-"`
	Revision                     string      `json:"revision,omitempty"`
	IncludeWorktree              bool        `json:"include_worktree,omitempty"`
	Query                        string      `json:"query"`
	Mode                         string      `json:"mode,omitempty"`
	Limit                        int         `json:"limit,omitempty"`
	MaxFileBytes                 int64       `json:"max_file_bytes,omitempty"`
	ChunkBytes                   int         `json:"chunk_bytes,omitempty"`
}

type SearchCoverage struct {
	State          string `json:"state"`
	EligibleFiles  int    `json:"eligible_files"`
	EligibleChunks int    `json:"eligible_chunks"`
	EmbeddedChunks int    `json:"embedded_chunks"`
	ReusedChunks   int    `json:"reused_chunks"`
	FailedChunks   int    `json:"failed_chunks"`
	MissingObjects int    `json:"missing_objects"`
}

type Citation struct {
	Authority      string `json:"authority"`
	CommitOID      string `json:"commit_oid"`
	BlobOID        string `json:"blob_oid"`
	Path           string `json:"path"`
	LineStart      int    `json:"line_start"`
	LineEnd        int    `json:"line_end"`
	RawSliceDigest string `json:"raw_slice_digest"`
}

type SearchHit struct {
	Rank     int      `json:"rank"`
	ChunkID  string   `json:"chunk_id"`
	Snippet  string   `json:"snippet"`
	Score    float64  `json:"score"`
	Lexical  float64  `json:"lexical_score,omitempty"`
	Semantic float64  `json:"semantic_score,omitempty"`
	Citation Citation `json:"citation"`
}

type SearchWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SearchResult struct {
	RepoID            string          `json:"repo_id"`
	CorpusKind        string          `json:"corpus_kind"`
	Query             string          `json:"query"`
	RequestedRevision string          `json:"requested_revision"`
	EffectiveRevision string          `json:"effective_revision"`
	Mode              string          `json:"mode"`
	RequestedMode     string          `json:"requested_mode"`
	EffectiveMode     string          `json:"effective_mode"`
	Authority         string          `json:"authority"`
	OverlayDigest     string          `json:"overlay_digest,omitempty"`
	RevisionSetID     string          `json:"revision_set_id,omitempty"`
	PolicyHash        string          `json:"policy_hash"`
	PolicySource      string          `json:"policy_source"`
	NamespaceID       string          `json:"namespace_id,omitempty"`
	Coverage          SearchCoverage  `json:"coverage"`
	Hits              []SearchHit     `json:"hits"`
	Warnings          []string        `json:"warnings,omitempty"`
	WarningDetails    []SearchWarning `json:"warning_details,omitempty"`
	Fallback          string          `json:"fallback,omitempty"`
}

func (r *SearchResult) addWarning(code, message string) {
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	r.Warnings = append(r.Warnings, message)
	r.WarningDetails = append(r.WarningDetails, SearchWarning{Code: code, Message: message})
}

type Retriever struct {
	store    SearchStore
	provider rag.EmbeddingProvider
}

func NewRetriever(store SearchStore, provider rag.EmbeddingProvider) *Retriever {
	return &Retriever{store: store, provider: provider}
}

func (r *Retriever) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if r == nil || r.store == nil || req.Repository == nil || strings.TrimSpace(req.RepoID) == "" || strings.TrimSpace(req.Query) == "" {
		return SearchResult{}, fmt.Errorf("repository docs: search requires repo id, Git repository, query, and cache")
	}
	if req.Mode == "" {
		req.Mode = SearchModeHybrid
	}
	if req.Mode != SearchModeHybrid && req.Mode != SearchModeFullText {
		return SearchResult{}, fmt.Errorf("repository docs: unsupported search mode %q", req.Mode)
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	processingPolicy := ProcessingPolicyFor(req.MaxFileBytes, req.ChunkBytes)
	chunkPolicyID := processingPolicy.ID()
	commitOID, policy, err := ResolvePolicy(ctx, req.Repository, req.Revision, req.IncludeWorktree)
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{RepoID: req.RepoID, CorpusKind: "repository_docs", Query: req.Query, RequestedRevision: req.Revision, EffectiveRevision: commitOID, Mode: req.Mode, RequestedMode: req.Mode, EffectiveMode: req.Mode, Authority: "git", PolicyHash: policy.PolicyHash, PolicySource: policy.Source}
	if policy.Status == PolicyStatusDisabled {
		result.Fallback = PolicyStatusDisabled
		result.addWarning("repository_docs_disabled", "repository documentation search is disabled by the committed policy")
		return result, nil
	}
	overlayDigest := ""
	var initialChanges []WorktreeChange
	if req.IncludeWorktree {
		changes, overlayErr := req.Repository.TrackedChangesAtFiltered(ctx, commitOID, processingPolicy.MaxFileBytes, policyOverlayPathFilter(policy.Policy))
		if overlayErr != nil {
			return SearchResult{}, overlayErr
		}
		initialChanges = changes
		overlayDigest = worktreeOverlayDigest(req.Repository.WorktreeRef, commitOID, policy.PolicyHash, changes)
		result.Authority = "worktree_overlay"
		result.OverlayDigest = overlayDigest
	}
	semanticAvailable := req.Mode == SearchModeHybrid && r.provider != nil
	expectedNamespaceID := ""
	if semanticAvailable {
		identity, identityErr := r.provider.NamespaceIdentity(ctx, rag.NamespaceRequest{RepoID: req.RepoID, ChunkPolicyID: chunkPolicyID, LanguagePolicyID: rag.DefaultLanguagePolicyID, DocumentInstructionID: "repo-doc-v1", QueryInstructionID: "repo-doc-query-v1"})
		if identityErr != nil {
			semanticAvailable = false
			result.Fallback = "provider_unavailable"
			result.EffectiveMode = SearchModeFullText
			result.addWarning("embedding_provider_identity_unavailable", "embedding provider identity unavailable; returning verified lexical results")
		} else {
			expectedNamespaceID = cache.EmbeddingNamespaceID(identity)
		}
	}
	var selected cache.RepositoryDocRevisionSet
	if semanticAvailable {
		expectedSetID := NewRevisionSetIdentity(req.RepoID, req.SourceRegistrationID, req.SourceRegistrationGeneration, req.Repository, commitOID, policy, overlayDigest, processingPolicy, expectedNamespaceID).ID()
		selected, err = r.store.GetRepositoryDocRevisionSet(ctx, req.RepoID, expectedSetID)
		if err != nil && !errors.Is(err, cache.ErrNotFound) {
			return SearchResult{}, err
		}
		if errors.Is(err, cache.ErrNotFound) || selected.State != cache.RepoDocSetReady {
			selected = cache.RepositoryDocRevisionSet{}
		}
	}
	if selected.ID != "" {
		result.RevisionSetID = selected.ID
		result.NamespaceID = selected.NamespaceID
		result.Coverage = SearchCoverage{State: selected.State, EligibleFiles: selected.EligibleFiles, EligibleChunks: selected.EligibleChunks, EmbeddedChunks: selected.EmbeddedChunks, ReusedChunks: selected.ReusedChunks, FailedChunks: selected.FailedChunks, MissingObjects: selected.MissingObjects}
	} else if req.Mode == SearchModeHybrid && semanticAvailable {
		result.EffectiveMode = SearchModeFullText
		result.Fallback = "revision_set_unavailable"
		if req.IncludeWorktree {
			priorSets, priorErr := r.store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: req.RepoID, SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: req.SourceRegistrationGeneration, GitStoreRef: req.Repository.GitStoreRef, CommitOID: commitOID, PolicyHash: policy.PolicyHash, ChunkPolicyID: chunkPolicyID, Limit: 20})
			if priorErr != nil {
				return SearchResult{}, priorErr
			}
			for _, prior := range priorSets {
				if prior.WorktreeRef == req.Repository.WorktreeRef && prior.OverlayDigest != "" && prior.OverlayDigest != overlayDigest {
					result.Fallback = "worktree_overlay_stale"
					break
				}
			}
		}
		if result.Fallback == "worktree_overlay_stale" {
			result.addWarning("worktree_overlay_stale", "worktree_overlay_stale: tracked content changed; reindex the overlay")
		} else {
			result.addWarning("revision_set_unavailable", "semantic revision set is unavailable; using Git-backed full-text retrieval")
		}
	}
	lexical, lexicalCoverage, lexicalWarnings, err := r.lexical(ctx, req.Repository, commitOID, policy.Policy, processingPolicy, req.Query, initialChanges, req.Limit)
	if err != nil {
		return SearchResult{}, err
	}
	for _, warning := range lexicalWarnings {
		result.addWarning(warning.Code, warning.Message)
	}
	if selected.ID == "" {
		result.Coverage = lexicalCoverage
	}
	semantic := map[string]rankedHit{}
	if req.Mode == SearchModeHybrid && selected.ID != "" && semanticAvailable {
		semantic, err = r.semantic(ctx, req, selected)
		if err != nil {
			result.Fallback = "semantic_unavailable"
			result.EffectiveMode = SearchModeFullText
			result.addWarning("semantic_retrieval_unavailable", "semantic retrieval unavailable; returning verified lexical results")
			semantic = map[string]rankedHit{}
		}
	} else if req.Mode == SearchModeHybrid && !semanticAvailable && result.Fallback == "" {
		result.Fallback = "provider_unavailable"
		result.EffectiveMode = SearchModeFullText
		result.addWarning("embedding_provider_unavailable", "embedding provider unavailable; returning verified lexical results")
	}
	merged := fuseRanks(lexical, semantic)
	for _, item := range merged {
		if len(result.Hits) >= req.Limit {
			break
		}
		hit, ok, warning := r.hydrate(ctx, req.Repository, commitOID, item)
		if warning.Message != "" {
			result.addWarning(warning.Code, warning.Message)
		}
		if !ok {
			continue
		}
		hit.Rank = len(result.Hits) + 1
		result.Hits = append(result.Hits, hit)
	}
	if req.IncludeWorktree {
		finalCommitOID, finalPolicy, resolveErr := ResolvePolicy(ctx, req.Repository, req.Revision, true)
		if resolveErr != nil {
			return SearchResult{}, resolveErr
		}
		finalChanges, changeErr := req.Repository.TrackedChangesAtFiltered(ctx, commitOID, processingPolicy.MaxFileBytes, policyOverlayPathFilter(policy.Policy))
		if changeErr != nil {
			return SearchResult{}, changeErr
		}
		if finalCommitOID != commitOID || finalPolicy.PolicyHash != policy.PolicyHash || finalPolicy.ConfigDigest != policy.ConfigDigest || worktreeOverlayDigest(req.Repository.WorktreeRef, commitOID, policy.PolicyHash, finalChanges) != overlayDigest {
			return SearchResult{}, &WorktreeOverlayStaleError{}
		}
	}
	return result, nil
}

type rankedHit struct {
	candidate cache.RepositoryDocCandidate
	score     float64
	lexical   float64
	semantic  float64
	text      string
}

func (r *Retriever) lexical(ctx context.Context, repo *Repository, commitOID string, policy Policy, processing ProcessingPolicy, query string, changes []WorktreeChange, limit int) (map[string]rankedHit, SearchCoverage, []SearchWarning, error) {
	processing = processing.normalized()
	chunkPolicyID := processing.ID()
	want := tokenize(query)
	results := map[string]rankedHit{}
	candidateLimit := boundedLexicalCandidateLimit(limit)
	coverage := SearchCoverage{State: "lexical-only"}
	warnings := map[string]SearchWarning{}
	recordAvailabilityWarning := func(err error) {
		var objectErr *GitObjectError
		if errors.As(err, &objectErr) {
			warnings[objectErr.DiagnosticCode()] = SearchWarning{Code: objectErr.DiagnosticCode(), Message: objectErr.Remediation()}
			return
		}
		warnings["git_object_unavailable"] = SearchWarning{Code: "git_object_unavailable", Message: "repository object unavailable; fetch the required Git objects and retry"}
	}
	changed := map[string]WorktreeChange{}
	removed := map[string]bool{}
	if changes != nil {
		for _, change := range changes {
			changed[change.Path] = change
			if change.Deleted {
				removed[change.Path] = true
			}
			if change.Renamed && change.OldPath != "" {
				removed[change.OldPath] = true
			}
		}
	}
	err := repo.WalkTree(ctx, commitOID, func(entry TreeEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type != "blob" || entry.Mode == "120000" || removed[entry.Path] || changed[entry.Path].Path != "" || entry.Size < 0 || entry.Size > processing.MaxFileBytes || !policy.Matches(entry.Path) {
			return nil
		}
		coverage.EligibleFiles++
		data, err := repo.ReadBlob(ctx, entry.OID, processing.MaxFileBytes)
		if err != nil {
			coverage.MissingObjects++
			recordAvailabilityWarning(err)
			return nil
		}
		if ClassifyDocumentContent(entry, data) != DocumentContentRegular {
			coverage.EligibleFiles--
			return nil
		}
		chunks, err := ChunkDocumentWithPolicy(digestBytes(data), data, processing)
		if err != nil {
			coverage.FailedChunks++
			return nil
		}
		coverage.EligibleChunks += len(chunks)
		for ordinal, chunk := range chunks {
			score := lexicalScore(chunk.Text, query, want, entry.Path)
			if score <= 0 {
				continue
			}
			candidate := cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{Path: entry.Path, ChunkID: chunk.ID, Authority: "git", Ordinal: ordinal, BlobOID: entry.OID, ContentDigest: chunk.RawSliceDigest}, ObjectFormat: repo.ObjectFormat, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, RawSliceDigest: chunk.RawSliceDigest, EmbeddingInputDigest: chunk.EmbeddingInputDigest, ChunkPolicyID: chunkPolicyID}
			results[candidateRankKey(candidate)] = rankedHit{candidate: candidate, score: score, lexical: score, text: chunk.Text}
		}
		results = trimRankedHits(results, candidateLimit)
		return nil
	})
	if err != nil {
		return nil, SearchCoverage{}, nil, err
	}
	for _, change := range changes {
		if change.Deleted || !policy.Matches(change.Path) {
			continue
		}
		coverage.EligibleFiles++
		data, readErr := repo.ReadTrackedWorktreeFile(ctx, change.Path, processing.MaxFileBytes)
		if readErr != nil {
			coverage.MissingObjects++
			warnings["worktree_overlay_unavailable"] = SearchWarning{Code: "worktree_overlay_unavailable", Message: "tracked worktree content unavailable; restore the registered worktree and retry"}
			continue
		}
		if int64(len(data)) != change.Size || digestBytes(data) != change.Digest {
			return nil, SearchCoverage{}, nil, &WorktreeOverlayStaleError{}
		}
		if ClassifyDocumentContent(TreeEntry{Mode: "100644", Type: "blob"}, data) != DocumentContentRegular {
			coverage.EligibleFiles--
			continue
		}
		chunks, chunkErr := ChunkDocumentWithPolicy(digestBytes(data), data, processing)
		if chunkErr != nil {
			coverage.FailedChunks++
			continue
		}
		coverage.EligibleChunks += len(chunks)
		for ordinal, chunk := range chunks {
			score := lexicalScore(chunk.Text, query, want, change.Path)
			if score <= 0 {
				continue
			}
			candidate := cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{Path: change.Path, ChunkID: chunk.ID, Authority: "worktree", Ordinal: ordinal, ContentDigest: chunk.RawSliceDigest}, ObjectFormat: repo.ObjectFormat, WorktreeRef: repo.WorktreeRef, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, RawSliceDigest: chunk.RawSliceDigest, EmbeddingInputDigest: chunk.EmbeddingInputDigest, ChunkPolicyID: chunkPolicyID}
			results[candidateRankKey(candidate)] = rankedHit{candidate: candidate, score: score, lexical: score, text: chunk.Text}
		}
		results = trimRankedHits(results, candidateLimit)
	}
	warningList := make([]SearchWarning, 0, len(warnings))
	for _, warning := range warnings {
		warningList = append(warningList, warning)
	}
	sort.Slice(warningList, func(i, j int) bool { return warningList[i].Code < warningList[j].Code })
	return results, coverage, warningList, nil
}

func boundedLexicalCandidateLimit(limit int) int {
	if limit <= 0 {
		limit = 10
	}
	value := limit * 8
	if value < 64 {
		value = 64
	}
	if value > 512 {
		value = 512
	}
	return value
}

func trimRankedHits(values map[string]rankedHit, limit int) map[string]rankedHit {
	if len(values) <= limit {
		return values
	}
	ranked := sortedRanks(values)
	trimmed := make(map[string]rankedHit, limit)
	for _, item := range ranked[:limit] {
		trimmed[candidateRankKey(item.candidate)] = item
	}
	return trimmed
}

func (r *Retriever) semantic(ctx context.Context, req SearchRequest, set cache.RepositoryDocRevisionSet) (map[string]rankedHit, error) {
	response, err := r.provider.Embed(ctx, rag.EmbedRequest{Inputs: []string{req.Query}})
	if err != nil || len(response.Embeddings) != 1 {
		if err == nil {
			err = fmt.Errorf("repository docs: query embedding count is not one")
		}
		return nil, err
	}
	query := response.Embeddings[0]
	snapshot, err := r.store.LoadRepositoryDocSearchSnapshot(ctx, req.RepoID, set.ID, set.NamespaceID)
	if err != nil {
		return nil, err
	}
	results := map[string]rankedHit{}
	for _, candidate := range snapshot.Candidates {
		if len(candidate.Vector) == 0 {
			continue
		}
		vector, err := rag.DecodeFloat32Vector(candidate.Vector, candidate.Dimensions)
		if err != nil || len(vector) != len(query) {
			continue
		}
		score := cosine(query, vector)
		results[candidateRankKey(candidate)] = rankedHit{candidate: candidate, score: score, semantic: score}
	}
	return results, nil
}

func fuseRanks(lexical, semantic map[string]rankedHit) []rankedHit {
	lex := sortedRanks(lexical)
	sem := sortedRanks(semantic)
	merged := map[string]rankedHit{}
	lexicalRank := 1
	for index, item := range lex {
		if index > 0 && item.score != lex[index-1].score {
			lexicalRank = index + 1
		}
		item.score = 1 / float64(60+lexicalRank)
		merged[candidateRankKey(item.candidate)] = item
	}
	semanticRank := 1
	for index, item := range sem {
		if index > 0 && item.score != sem[index-1].score {
			semanticRank = index + 1
		}
		key := candidateRankKey(item.candidate)
		current, ok := merged[key]
		if !ok {
			current = item
			current.score = 0
		}
		current.score += 1 / float64(60+semanticRank)
		current.semantic = item.semantic
		merged[key] = current
	}
	return sortedRanks(merged)
}

func candidateRankKey(candidate cache.RepositoryDocCandidate) string {
	return candidate.Path + "\x00" + candidate.ChunkID
}

func sortedRanks(items map[string]rankedHit) []rankedHit {
	out := make([]rankedHit, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].candidate.Path != out[j].candidate.Path {
			return out[i].candidate.Path < out[j].candidate.Path
		}
		return out[i].candidate.ChunkID < out[j].candidate.ChunkID
	})
	return out
}

func (r *Retriever) hydrate(ctx context.Context, repo *Repository, commitOID string, item rankedHit) (SearchHit, bool, SearchWarning) {
	candidate := item.candidate
	if candidate.Authority == "worktree" {
		data, err := repo.ReadTrackedWorktreeFile(ctx, candidate.Path, DefaultMaxDocumentBytes)
		if err != nil {
			return SearchHit{}, false, SearchWarning{Code: "worktree_overlay_unavailable", Message: "worktree_overlay_unavailable: reindex the tracked overlay"}
		}
		if candidate.ByteStart < 0 || candidate.ByteEnd > len(data) || candidate.ByteStart >= candidate.ByteEnd || digestBytes(data[candidate.ByteStart:candidate.ByteEnd]) != candidate.RawSliceDigest {
			return SearchHit{}, false, SearchWarning{Code: "worktree_overlay_stale", Message: "worktree_overlay_stale: tracked content changed; reindex the overlay"}
		}
		raw := data[candidate.ByteStart:candidate.ByteEnd]
		snippet := truncateUTF8Bytes(strings.TrimSpace(string(raw)), 800)
		return SearchHit{ChunkID: candidate.ChunkID, Snippet: snippet, Score: item.score, Lexical: item.lexical, Semantic: item.semantic, Citation: Citation{Authority: "worktree", CommitOID: commitOID, Path: candidate.Path, LineStart: candidate.LineStart, LineEnd: candidate.LineEnd, RawSliceDigest: candidate.RawSliceDigest}}, true, SearchWarning{}
	}
	data, err := repo.ReadBlob(ctx, candidate.BlobOID, DefaultMaxDocumentBytes)
	if err != nil {
		return SearchHit{}, false, SearchWarning{Code: "citation_blob_unavailable", Message: "citation omitted: Git blob is unavailable"}
	}
	if candidate.ByteStart < 0 || candidate.ByteEnd > len(data) || candidate.ByteStart >= candidate.ByteEnd {
		return SearchHit{}, false, SearchWarning{Code: "citation_locator_invalid", Message: "citation omitted: stored chunk locator is invalid"}
	}
	raw := data[candidate.ByteStart:candidate.ByteEnd]
	if digestBytes(raw) != candidate.RawSliceDigest {
		return SearchHit{}, false, SearchWarning{Code: "citation_digest_mismatch", Message: "citation omitted: Git content failed digest verification"}
	}
	snippet := truncateUTF8Bytes(strings.TrimSpace(string(raw)), 800)
	return SearchHit{ChunkID: candidate.ChunkID, Snippet: snippet, Score: item.score, Lexical: item.lexical, Semantic: item.semantic, Citation: Citation{Authority: "git", CommitOID: commitOID, BlobOID: candidate.BlobOID, Path: candidate.Path, LineStart: candidate.LineStart, LineEnd: candidate.LineEnd, RawSliceDigest: candidate.RawSliceDigest}}, true, SearchWarning{}
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func lexicalScore(text, query string, queryTokens map[string]struct{}, path string) float64 {
	lower := strings.ToLower(text)
	score := 0.0
	if strings.Contains(lower, strings.ToLower(strings.TrimSpace(query))) {
		score += 4
	}
	tokens := tokenize(text)
	for token := range queryTokens {
		if _, ok := tokens[token]; ok {
			score++
		}
	}
	if strings.Contains(strings.ToLower(path), strings.ToLower(strings.TrimSpace(query))) {
		score += 3
	}
	return score
}

func tokenize(value string) map[string]struct{} {
	returnValue := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) }) {
		if token != "" {
			returnValue[token] = struct{}{}
		}
	}
	return returnValue
}

func cosine(a, b []float32) float64 {
	var dot, aa, bb float64
	for idx := range a {
		dot += float64(a[idx] * b[idx])
		aa += float64(a[idx] * a[idx])
		bb += float64(b[idx] * b[idx])
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / (math.Sqrt(aa) * math.Sqrt(bb))
}
