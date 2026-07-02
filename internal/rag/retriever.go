package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/index"
)

const (
	SearchModeHybridRAG = "hybrid_rag"

	RAGSearchStatusReady            = "ready"
	RAGSearchStatusNoNamespace      = "no_namespace"
	RAGSearchStatusProviderNotReady = "provider_not_ready"
	RAGSearchStatusEmpty            = "empty"
	RAGSearchStatusNoResults        = "no_results"
)

type SearchRequest struct {
	RepoID                string `json:"repo_id"`
	Query                 string `json:"query"`
	ProfileID             string `json:"profile_id,omitempty"`
	SourceID              string `json:"source_id,omitempty"`
	RecordID              string `json:"record_id,omitempty"`
	SnapshotID            string `json:"snapshot_id,omitempty"`
	ChunkPolicyID         string `json:"chunk_policy_id,omitempty"`
	LanguagePolicyID      string `json:"language_policy_id,omitempty"`
	DocumentInstructionID string `json:"document_instruction_id,omitempty"`
	QueryInstructionID    string `json:"query_instruction_id,omitempty"`
	TopK                  int    `json:"top_k,omitempty"`
	Limit                 int    `json:"limit,omitempty"`
}

type SearchResult struct {
	RepoID       string          `json:"repo_id"`
	Query        string          `json:"query"`
	SearchMode   string          `json:"search_mode"`
	Status       string          `json:"status"`
	Provider     ProviderStatus  `json:"provider"`
	Namespace    NamespaceStatus `json:"namespace"`
	Coverage     CoverageStatus  `json:"coverage"`
	Results      []SearchContext `json:"results"`
	Warnings     []string        `json:"warnings,omitempty"`
	FailureClass string          `json:"failure_class,omitempty"`
	Message      string          `json:"message,omitempty"`
	GeneratedAt  time.Time       `json:"generated_at"`
}

type SearchContext struct {
	Rank          int            `json:"rank"`
	ChunkID       string         `json:"chunk_id"`
	SourceID      string         `json:"source_id"`
	RecordID      string         `json:"record_id,omitempty"`
	SnapshotID    string         `json:"snapshot_id,omitempty"`
	Path          string         `json:"path,omitempty"`
	Title         string         `json:"title,omitempty"`
	LineStart     int            `json:"line_start,omitempty"`
	LineEnd       int            `json:"line_end,omitempty"`
	Snippet       string         `json:"snippet"`
	Score         ScoreBreakdown `json:"score"`
	NamespaceID   string         `json:"namespace_id"`
	ProfileID     string         `json:"profile_id,omitempty"`
	Model         string         `json:"model,omitempty"`
	ModelRevision string         `json:"model_revision,omitempty"`
}

type ScoreBreakdown struct {
	Hybrid   float64 `json:"hybrid"`
	Semantic float64 `json:"semantic"`
	Lexical  float64 `json:"lexical"`
}

type RAGRetriever struct {
	store       ragSearchStore
	provider    EmbeddingProvider
	vectorStore VectorStore
	now         func() time.Time
}

type RAGRetrieverOptions struct {
	VectorStore VectorStore
	Now         func() time.Time
}

type ragSearchStore interface {
	ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error)
	ListChunks(context.Context, cache.ChunkFilter) ([]cache.Chunk, error)
	ListChunkEmbeddings(context.Context, cache.ChunkEmbeddingFilter) ([]cache.ChunkEmbedding, error)
	GetSourceScoped(context.Context, string, string) (cache.Source, error)
}

func NewRAGRetriever(store ragSearchStore, provider EmbeddingProvider, opts RAGRetrieverOptions) *RAGRetriever {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	vectorStore := opts.VectorStore
	if vectorStore == nil {
		if fullStore, ok := store.(exactScanCacheStore); ok {
			vectorStore = NewExactScanVectorStore(fullStore)
		}
	}
	return &RAGRetriever{store: store, provider: provider, vectorStore: vectorStore, now: now}
}

func (r *RAGRetriever) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if r == nil || r.store == nil {
		return SearchResult{}, fmt.Errorf("rag search: cache store is required")
	}
	if r.provider == nil {
		return SearchResult{}, fmt.Errorf("rag search: embedding provider is required")
	}
	if r.vectorStore == nil {
		return SearchResult{}, fmt.Errorf("rag search: vector store is required")
	}
	if strings.TrimSpace(req.RepoID) == "" {
		return SearchResult{}, fmt.Errorf("rag search: repo id is required")
	}
	if strings.TrimSpace(req.Query) == "" {
		return SearchResult{}, fmt.Errorf("rag search: query is required")
	}
	req.ChunkPolicyID = firstNonEmpty(req.ChunkPolicyID, "heading-v1")
	req.LanguagePolicyID = firstNonEmpty(req.LanguagePolicyID, DefaultLanguagePolicyID)
	req.DocumentInstructionID = firstNonEmpty(req.DocumentInstructionID, DefaultDocumentInstructionID)
	req.QueryInstructionID = firstNonEmpty(req.QueryInstructionID, DefaultQueryInstructionID)
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	topK := req.TopK
	if topK <= 0 {
		topK = limit * 4
	}
	if topK < limit {
		topK = limit
	}

	profile := r.provider.Profile()
	result := SearchResult{
		RepoID:      req.RepoID,
		Query:       req.Query,
		SearchMode:  SearchModeHybridRAG,
		Status:      RAGSearchStatusReady,
		GeneratedAt: r.now().UTC(),
		Provider: ProviderStatus{
			ProfileID:    firstNonEmpty(req.ProfileID, profile.ProfileID),
			ProviderID:   profile.ProviderID,
			ProviderType: profile.ProviderType,
			Endpoint:     profile.Endpoint,
			Model:        profile.Model,
			Dimensions:   profile.Dimensions,
			BatchSize:    profile.BatchSize,
		},
	}
	info, providerErr := r.provider.ModelInfo(ctx)
	if providerErr != nil {
		result.Status = RAGSearchStatusProviderNotReady
		result.Provider.ErrorClass = providerFailureClass(providerErr)
		result.Provider.Message = providerErr.Error()
		result.FailureClass = result.Provider.ErrorClass
		result.Message = providerErr.Error()
		return result, nil
	}
	result.Provider.Ready = true
	result.Provider.Model = info.Model
	result.Provider.Revision = info.Revision

	identity, err := r.provider.NamespaceIdentity(ctx, NamespaceRequest{RepoID: req.RepoID, ChunkPolicyID: req.ChunkPolicyID, LanguagePolicyID: req.LanguagePolicyID, DocumentInstructionID: req.DocumentInstructionID, QueryInstructionID: req.QueryInstructionID})
	if err != nil {
		result.Status = RAGSearchStatusProviderNotReady
		result.FailureClass = providerFailureClass(err)
		result.Message = err.Error()
		return result, nil
	}
	namespace, ok, err := r.store.ResolveEmbeddingNamespace(ctx, identity)
	if err != nil {
		return SearchResult{}, err
	}
	result.Namespace.Exists = ok
	result.Namespace.Current = ok
	if !ok {
		result.Status = RAGSearchStatusNoNamespace
		result.Warnings = append(result.Warnings, "rag namespace is missing; run rag index for this repo/profile/model")
		return result, nil
	}
	result.Namespace.ID = namespace.ID

	chunks, err := r.store.ListChunks(ctx, cache.ChunkFilter{RepoID: req.RepoID, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID, Policy: req.ChunkPolicyID})
	if err != nil {
		return SearchResult{}, err
	}
	if len(chunks) == 0 {
		result.Status = RAGSearchStatusEmpty
		result.Warnings = append(result.Warnings, "no cached chunks match the request")
		return result, nil
	}
	result.Coverage.TotalChunks = len(chunks)
	byID := make(map[string]cache.Chunk, len(chunks))
	for _, chunk := range chunks {
		byID[chunk.ID] = chunk
	}
	coverage, err := searchCoverage(ctx, r.store, req, namespace.ID, byID)
	if err != nil {
		return SearchResult{}, err
	}
	result.Coverage = coverage
	if coverage.MissingChunks > 0 || coverage.StaleChunks > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("rag coverage is incomplete: %d missing, %d stale", coverage.MissingChunks, coverage.StaleChunks))
	}

	embedded, err := r.provider.Embed(ctx, EmbedRequest{Inputs: []string{req.Query}})
	if err != nil {
		result.Status = RAGSearchStatusProviderNotReady
		result.FailureClass = providerFailureClass(err)
		result.Message = err.Error()
		return result, nil
	}
	if len(embedded.Embeddings) != 1 {
		return SearchResult{}, fmt.Errorf("rag search: embedding count = %d, want 1", len(embedded.Embeddings))
	}
	semantic, err := r.vectorStore.Search(ctx, VectorSearchRequest{RepoID: req.RepoID, NamespaceID: namespace.ID, QueryVector: embedded.Embeddings[0], TopK: topK, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID})
	if err != nil {
		return SearchResult{}, err
	}
	candidates := map[string]*rankedCandidate{}
	for _, item := range semantic {
		chunk, ok := byID[item.ChunkID]
		if !ok {
			continue
		}
		c := candidateFor(candidates, chunk)
		c.score.Semantic = maxFloat(c.score.Semantic, float64(item.Score))
	}
	for _, item := range lexicalCandidates(ctx, chunks, req, topK) {
		c := candidateFor(candidates, item.chunk)
		c.score.Lexical = maxFloat(c.score.Lexical, item.score)
	}
	ranked := make([]rankedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.score.Hybrid = 0.75*candidate.score.Semantic + 0.25*candidate.score.Lexical
		if candidate.score.Hybrid <= 0 {
			continue
		}
		ranked = append(ranked, *candidate)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score.Hybrid != ranked[j].score.Hybrid {
			return ranked[i].score.Hybrid > ranked[j].score.Hybrid
		}
		if ranked[i].score.Semantic != ranked[j].score.Semantic {
			return ranked[i].score.Semantic > ranked[j].score.Semantic
		}
		if ranked[i].score.Lexical != ranked[j].score.Lexical {
			return ranked[i].score.Lexical > ranked[j].score.Lexical
		}
		return ranked[i].chunk.ID < ranked[j].chunk.ID
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	if len(ranked) == 0 {
		result.Status = RAGSearchStatusNoResults
		return result, nil
	}
	sourceCache := map[string]cache.Source{}
	for idx, item := range ranked {
		source, ok := sourceCache[item.chunk.SourceID]
		if !ok {
			source, _ = r.store.GetSourceScoped(ctx, req.RepoID, item.chunk.SourceID)
			sourceCache[item.chunk.SourceID] = source
		}
		result.Results = append(result.Results, SearchContext{
			Rank:          idx + 1,
			ChunkID:       item.chunk.ID,
			SourceID:      item.chunk.SourceID,
			RecordID:      item.chunk.RecordID,
			SnapshotID:    item.chunk.SnapshotID,
			Path:          source.Path,
			Title:         source.Title,
			LineStart:     item.chunk.LineStart,
			LineEnd:       item.chunk.LineEnd,
			Snippet:       packSnippet(item.chunk.Text),
			Score:         item.score,
			NamespaceID:   namespace.ID,
			ProfileID:     namespace.ProfileID,
			Model:         namespace.ModelID,
			ModelRevision: namespace.ModelRevision,
		})
	}
	return result, nil
}

func searchCoverage(ctx context.Context, store ragSearchStore, req SearchRequest, namespaceID string, chunks map[string]cache.Chunk) (CoverageStatus, error) {
	coverage := CoverageStatus{TotalChunks: len(chunks)}
	embeddings, err := store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{RepoID: req.RepoID, NamespaceID: namespaceID, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID})
	if err != nil {
		return CoverageStatus{}, err
	}
	seen := map[string]bool{}
	for _, embedding := range embeddings {
		chunk, ok := chunks[embedding.ChunkID]
		if !ok || seen[embedding.ChunkID] {
			continue
		}
		seen[embedding.ChunkID] = true
		if embedding.ChunkContentHash == chunk.ContentHash {
			coverage.EmbeddedChunks++
		} else {
			coverage.StaleChunks++
		}
	}
	coverage.MissingChunks = coverage.TotalChunks - coverage.EmbeddedChunks - coverage.StaleChunks
	if coverage.MissingChunks < 0 {
		coverage.MissingChunks = 0
	}
	return coverage, nil
}

type rankedCandidate struct {
	chunk cache.Chunk
	score ScoreBreakdown
}

type lexicalCandidate struct {
	chunk cache.Chunk
	score float64
}

func candidateFor(candidates map[string]*rankedCandidate, chunk cache.Chunk) *rankedCandidate {
	if c, ok := candidates[chunk.ID]; ok {
		return c
	}
	c := &rankedCandidate{chunk: chunk}
	candidates[chunk.ID] = c
	return c
}

func lexicalCandidates(ctx context.Context, chunks []cache.Chunk, req SearchRequest, limit int) []lexicalCandidate {
	indexChunks := make([]index.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		indexChunks = append(indexChunks, index.Chunk{RepoID: chunk.RepoID, ID: chunk.ID, SourceID: chunk.SourceID, RecordID: chunk.RecordID, SnapshotID: chunk.SnapshotID, ContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: append([]string(nil), chunk.HeadingPath...), Text: chunk.Text, NormalizedText: chunk.NormalizedText, Policy: index.ChunkPolicy(chunk.Policy)})
	}
	result, err := index.NewMemoryChunkIndex(indexChunks).SearchChunks(ctx, index.ChunkSearchQuery{ChunkQuery: index.ChunkQuery{RepoID: req.RepoID, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID, Policy: index.ChunkPolicy(req.ChunkPolicyID), Limit: limit}, Query: req.Query})
	if err != nil {
		return nil
	}
	byID := make(map[string]cache.Chunk, len(chunks))
	for _, chunk := range chunks {
		byID[chunk.ID] = chunk
	}
	out := make([]lexicalCandidate, 0, len(result.Chunks))
	for _, match := range result.Chunks {
		chunk, ok := byID[match.ID]
		if !ok {
			continue
		}
		out = append(out, lexicalCandidate{chunk: chunk, score: lexicalScore(req.Query, chunk)})
	}
	return out
}

func lexicalScore(query string, chunk cache.Chunk) float64 {
	query = normalizeRAGText(query)
	text := normalizeRAGText(firstNonEmpty(chunk.NormalizedText, chunk.Text))
	if query == "" || text == "" {
		return 0
	}
	if strings.Contains(text, query) {
		return 1
	}
	tokens := splitRAGTokens(query)
	if len(tokens) == 0 {
		return 0
	}
	matches := 0
	for _, token := range tokens {
		if strings.Contains(text, token) {
			matches++
		}
	}
	return float64(matches) / float64(len(tokens))
}

func normalizeRAGText(text string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func splitRAGTokens(text string) []string {
	fields := strings.Fields(normalizeRAGText(text))
	if len(fields) > 0 {
		return fields
	}
	if utf8.RuneCountInString(text) > 0 {
		return []string{text}
	}
	return nil
}

func packSnippet(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Join(strings.Fields(text), " ")
	const limit = 360
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
