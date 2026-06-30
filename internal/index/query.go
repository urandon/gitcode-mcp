package index

import (
	"context"
	"sort"
	"strings"
)

type MemoryChunkIndex struct {
	chunks []Chunk
}

func NewMemoryChunkIndex(chunks []Chunk) MemoryChunkIndex {
	copied := append([]Chunk(nil), chunks...)
	sortChunks(copied)
	return MemoryChunkIndex{chunks: copied}
}

func (idx MemoryChunkIndex) ListChunks(ctx context.Context, query ChunkQuery) (ChunkQueryResult, error) {
	return idx.ListChunksWithWarnings(ctx, query, nil)
}

func (idx MemoryChunkIndex) ListChunksWithWarnings(ctx context.Context, query ChunkQuery, warnings []IndexWarning) (ChunkQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return ChunkQueryResult{}, err
	}
	matches := idx.filter(query, "")
	return chunkQueryResult(matches, query.Limit, query.Offset, filterWarnings(warnings, query)), nil
}

func (idx MemoryChunkIndex) SearchChunks(ctx context.Context, query ChunkSearchQuery) (ChunkQueryResult, error) {
	return idx.SearchChunksWithWarnings(ctx, query, nil)
}

func (idx MemoryChunkIndex) SearchChunksWithWarnings(ctx context.Context, query ChunkSearchQuery, warnings []IndexWarning) (ChunkQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return ChunkQueryResult{}, err
	}
	needle := normalizeChunkText(query.Query)
	matches := idx.filter(query.ChunkQuery, needle)
	result := chunkQueryResult(matches, query.Limit, query.Offset, filterWarnings(warnings, query.ChunkQuery))
	result.SearchMode = SearchModeFullText
	return result, nil
}

func (idx MemoryChunkIndex) GetSnippet(ctx context.Context, query SnippetQuery) (ChunkQueryResult, error) {
	return idx.GetSnippetWithWarnings(ctx, query, nil)
}

func (idx MemoryChunkIndex) GetSnippetWithWarnings(ctx context.Context, query SnippetQuery, warnings []IndexWarning) (ChunkQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return ChunkQueryResult{}, err
	}
	base := ChunkQuery{RepoID: query.RepoID, SourceID: query.SourceID, RecordID: query.RecordID, SnapshotID: query.SnapshotID, Policy: query.Policy}
	warnings = filterWarnings(warnings, base)
	matches := idx.filter(base, "")
	var out []Chunk
	for _, chunk := range matches {
		if query.ChunkID != "" && chunk.ID != query.ChunkID {
			continue
		}
		if query.LineStart > 0 || query.LineEnd > 0 {
			if query.LineStart > 0 && chunk.LineEnd < query.LineStart {
				continue
			}
			if query.LineEnd > 0 && chunk.LineStart > query.LineEnd {
				continue
			}
		}
		if query.ByteLength > 0 || query.ByteOffset > 0 {
			start := query.ByteOffset
			end := query.ByteOffset + query.ByteLength
			if query.ByteLength <= 0 {
				end = chunk.ByteEnd
			}
			if chunk.ByteEnd <= start || chunk.ByteStart >= end {
				continue
			}
		}
		out = append(out, chunk)
	}
	result := chunkQueryResult(out, 1, 0, warnings)
	for i := range result.Chunks {
		result.Chunks[i].SnippetText = result.Chunks[i].Text
	}
	return result, nil
}

func (idx MemoryChunkIndex) filter(query ChunkQuery, needle string) []Chunk {
	var out []Chunk
	policy := query.Policy
	for _, chunk := range idx.chunks {
		if query.RepoID != "" && chunk.RepoID != query.RepoID {
			continue
		}
		if query.SourceID != "" && chunk.SourceID != query.SourceID {
			continue
		}
		if query.RecordID != "" && chunk.RecordID != query.RecordID {
			continue
		}
		if query.SnapshotID != "" && chunk.SnapshotID != query.SnapshotID {
			continue
		}
		if policy != "" && chunk.Policy != policy {
			continue
		}
		if needle != "" && !strings.Contains(chunk.NormalizedText, needle) {
			continue
		}
		out = append(out, chunk)
	}
	return out
}

func filterWarnings(warnings []IndexWarning, query ChunkQuery) []IndexWarning {
	out := make([]IndexWarning, 0, len(warnings))
	for _, warning := range warnings {
		if query.RepoID != "" && warning.RepoID != "" && warning.RepoID != query.RepoID {
			continue
		}
		if query.SourceID != "" && warning.SourceID != "" && warning.SourceID != query.SourceID {
			continue
		}
		if query.RecordID != "" && warning.RecordID != "" && warning.RecordID != query.RecordID {
			continue
		}
		if query.SnapshotID != "" && warning.SnapshotID != "" && warning.SnapshotID != query.SnapshotID {
			continue
		}
		if query.Policy != "" && warning.Policy != "" && warning.Policy != query.Policy {
			continue
		}
		out = append(out, warning)
	}
	return out
}

func chunkQueryResult(chunks []Chunk, limit, offset int, warnings []IndexWarning) ChunkQueryResult {
	sortChunks(chunks)
	total := len(chunks)
	if offset < 0 {
		offset = 0
	}
	if offset > len(chunks) {
		chunks = nil
	} else {
		chunks = chunks[offset:]
	}
	if limit > 0 && len(chunks) > limit {
		chunks = chunks[:limit]
	}
	results := make([]ChunkResult, 0, len(chunks))
	for _, chunk := range chunks {
		results = append(results, chunkResult(chunk))
	}
	resultWarnings := append([]IndexWarning(nil), warnings...)
	if resultWarnings == nil {
		resultWarnings = []IndexWarning{}
	}
	return ChunkQueryResult{Chunks: results, Limit: limit, Offset: offset, Total: total, Warnings: resultWarnings}
}

func chunkResult(chunk Chunk) ChunkResult {
	return ChunkResult{ID: chunk.ID, RepoID: chunk.RepoID, SourceID: chunk.SourceID, RecordID: chunk.RecordID, SnapshotID: chunk.SnapshotID, Policy: chunk.Policy, ContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: append([]string(nil), chunk.HeadingPath...), Text: chunk.Text, NormalizedText: chunk.NormalizedText, CitationAnchorID: chunk.CitationAnchorID, InheritedMetadata: copyMap(chunk.InheritedMetadata), OutboundLinks: append([]string(nil), chunk.OutboundLinks...), ResolvedAliases: copyMap(chunk.ResolvedAliases)}
}

func sortChunks(chunks []Chunk) {
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].RepoID != chunks[j].RepoID {
			return chunks[i].RepoID < chunks[j].RepoID
		}
		if chunks[i].SourceID != chunks[j].SourceID {
			return chunks[i].SourceID < chunks[j].SourceID
		}
		if chunks[i].RecordID != chunks[j].RecordID {
			return chunks[i].RecordID < chunks[j].RecordID
		}
		if chunks[i].Policy != chunks[j].Policy {
			return chunks[i].Policy < chunks[j].Policy
		}
		if chunks[i].ByteStart != chunks[j].ByteStart {
			return chunks[i].ByteStart < chunks[j].ByteStart
		}
		if chunks[i].LineStart != chunks[j].LineStart {
			return chunks[i].LineStart < chunks[j].LineStart
		}
		return chunks[i].ID < chunks[j].ID
	})
}

func copyMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}
