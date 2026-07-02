package rag

import (
	"container/heap"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"gitcode-mcp/internal/cache"
)

const vectorFloat32ByteSize = 4

type VectorStore interface {
	Search(context.Context, VectorSearchRequest) ([]VectorSearchResult, error)
}

type VectorSearchRequest struct {
	RepoID      string
	NamespaceID string
	QueryVector []float32
	TopK        int
	SourceID    string
	RecordID    string
	SnapshotID  string
}

type VectorSearchResult struct {
	RepoID           string
	NamespaceID      string
	ChunkID          string
	SourceID         string
	RecordID         string
	SnapshotID       string
	ChunkContentHash string
	Score            float32
	VectorHash       string
}

type ExactScanVectorStore struct {
	store exactScanCacheStore
}

type exactScanCacheStore interface {
	ListChunkEmbeddings(context.Context, cache.ChunkEmbeddingFilter) ([]cache.ChunkEmbedding, error)
	ListChunks(context.Context, cache.ChunkFilter) ([]cache.Chunk, error)
}

func NewExactScanVectorStore(store exactScanCacheStore) *ExactScanVectorStore {
	return &ExactScanVectorStore{store: store}
}

func (s *ExactScanVectorStore) Search(ctx context.Context, req VectorSearchRequest) ([]VectorSearchResult, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("rag vector store: cache store is required")
	}
	if req.RepoID == "" || req.NamespaceID == "" {
		return nil, fmt.Errorf("rag vector store: repo id and namespace id are required")
	}
	if req.TopK <= 0 {
		return []VectorSearchResult{}, nil
	}
	queryVector, err := NormalizeFloat32Vector(req.QueryVector)
	if err != nil {
		return nil, fmt.Errorf("rag vector store: invalid query vector: %w", err)
	}
	currentHashes, err := s.currentChunkHashes(ctx, req)
	if err != nil {
		return nil, err
	}
	embeddings, err := s.store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{
		RepoID:      req.RepoID,
		NamespaceID: req.NamespaceID,
		SourceID:    req.SourceID,
		RecordID:    req.RecordID,
		SnapshotID:  req.SnapshotID,
	})
	if err != nil {
		return nil, err
	}
	top := &vectorResultHeap{}
	heap.Init(top)
	for _, embedding := range embeddings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if currentHash, ok := currentHashes[embedding.ChunkID]; !ok || currentHash != embedding.ChunkContentHash {
			continue
		}
		vector, err := DecodeFloat32Vector(embedding.Vector, embedding.Dimensions)
		if err != nil {
			return nil, fmt.Errorf("rag vector store: decode %s: %w", embedding.ChunkID, err)
		}
		if len(vector) != len(queryVector) {
			return nil, fmt.Errorf("rag vector store: vector dimensions for %s = %d, query = %d", embedding.ChunkID, len(vector), len(queryVector))
		}
		result := VectorSearchResult{
			RepoID:           embedding.RepoID,
			NamespaceID:      embedding.NamespaceID,
			ChunkID:          embedding.ChunkID,
			SourceID:         embedding.SourceID,
			RecordID:         embedding.RecordID,
			SnapshotID:       embedding.SnapshotID,
			ChunkContentHash: embedding.ChunkContentHash,
			Score:            dotProduct(queryVector, vector),
			VectorHash:       embedding.VectorHash,
		}
		if top.Len() < req.TopK {
			heap.Push(top, result)
			continue
		}
		if betterVectorResult(result, (*top)[0]) {
			heap.Pop(top)
			heap.Push(top, result)
		}
	}
	results := make([]VectorSearchResult, top.Len())
	for i := len(results) - 1; i >= 0; i-- {
		results[i] = heap.Pop(top).(VectorSearchResult)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return betterVectorResult(results[i], results[j])
	})
	return results, nil
}

func (s *ExactScanVectorStore) currentChunkHashes(ctx context.Context, req VectorSearchRequest) (map[string]string, error) {
	chunks, err := s.store.ListChunks(ctx, cache.ChunkFilter{
		RepoID:     req.RepoID,
		SourceID:   req.SourceID,
		RecordID:   req.RecordID,
		SnapshotID: req.SnapshotID,
	})
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]string, len(chunks))
	for _, chunk := range chunks {
		hashes[chunk.ID] = chunk.ContentHash
	}
	return hashes, nil
}

func EncodeNormalizedFloat32Vector(vector []float32) ([]byte, error) {
	normalized, err := NormalizeFloat32Vector(vector)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(normalized)*vectorFloat32ByteSize)
	for i, value := range normalized {
		binary.LittleEndian.PutUint32(out[i*vectorFloat32ByteSize:], math.Float32bits(value))
	}
	return out, nil
}

func DecodeFloat32Vector(blob []byte, dimensions int) ([]float32, error) {
	if dimensions <= 0 {
		return nil, fmt.Errorf("dimensions must be positive")
	}
	wantBytes := dimensions * vectorFloat32ByteSize
	if len(blob) != wantBytes {
		return nil, fmt.Errorf("blob length = %d, want %d", len(blob), wantBytes)
	}
	vector := make([]float32, dimensions)
	for i := 0; i < dimensions; i++ {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*vectorFloat32ByteSize:]))
	}
	return vector, nil
}

func NormalizeFloat32Vector(vector []float32) ([]float32, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("vector is empty")
	}
	var sum float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("vector contains non-finite value")
		}
		sum += float64(value) * float64(value)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return nil, fmt.Errorf("vector norm is zero")
	}
	normalized := make([]float32, len(vector))
	for i, value := range vector {
		normalized[i] = float32(float64(value) / norm)
	}
	return normalized, nil
}

func dotProduct(left, right []float32) float32 {
	var score float32
	for i := range left {
		score += left[i] * right[i]
	}
	return score
}

func betterVectorResult(left, right VectorSearchResult) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.ChunkID != right.ChunkID {
		return left.ChunkID < right.ChunkID
	}
	return left.VectorHash < right.VectorHash
}

type vectorResultHeap []VectorSearchResult

func (h vectorResultHeap) Len() int { return len(h) }

func (h vectorResultHeap) Less(i, j int) bool {
	return betterVectorResult(h[j], h[i])
}

func (h vectorResultHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *vectorResultHeap) Push(value any) {
	*h = append(*h, value.(VectorSearchResult))
}

func (h *vectorResultHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	*h = old[:n-1]
	return value
}
