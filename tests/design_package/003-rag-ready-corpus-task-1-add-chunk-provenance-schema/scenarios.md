# Scenarios: Add Chunk Provenance Schema

## 003-rag-ready-corpus-task-1-add-chunk-provenance-schema-scenario-1

A developer runs `go test ./internal/cache/... -run TestChunkSchemaEmbeddingColumn`; the cache API opens an in-memory SQLite store, inspects the `chunks` table, inserts multiple chunks sharing the same `source_id` and `content_hash` but different `byte_start`, observes `embedding` as NULL, and verifies duplicate `(source_id, content_hash, byte_start)` is rejected.

Executable evidence: `run.sh` invokes `go test ./internal/cache/... -run TestChunkSchemaEmbeddingColumn -count=1`. The test exercises the production cache API and SQLite schema through an in-memory store and fails if the embedding column, nullable default behavior, multi-chunk version model, or uniqueness constraint is missing.

## 003-rag-ready-corpus-task-1-add-chunk-provenance-schema-scenario-2

A developer runs `go test ./internal/index/... -run TestChunkDeterminism`; the index runtime ingests markdown with frontmatter, pre-heading text, headings, links, and a fenced code block, then returns chunks with correct half-open byte offsets, inclusive line ranges, heading paths, inherited metadata, outbound links, resolved aliases, normalized text, and identical chunk ids on a second run over the same source.

Executable evidence: `run.sh` invokes `go test ./internal/index/... -run TestChunkDeterminism -count=1`. The test exercises `ParseSource` and `ChunkSource` over local markdown fixture bytes and fails if offsets, line ranges, heading paths, inherited metadata, outbound links, resolved aliases, normalized text, or deterministic chunk ids are incorrect.

## 003-rag-ready-corpus-task-1-add-chunk-provenance-schema-scenario-3

The same test verifies the frontmatter invariant by asserting no chunk range overlaps valid frontmatter bytes, `Chunk.Text == original_bytes[byte_start:byte_end]` for every chunk, frontmatter values appear only in `InheritedMetadata`, and a snippet/citation call over each chunk byte range returns the same text.

Executable evidence: `run.sh` invokes `go test ./internal/index/... -run TestChunkDeterminism -count=1`. The same runtime path fails if valid frontmatter bytes are included in chunk ranges, if chunk text stops matching the original source byte slice, if frontmatter leaks into chunk text instead of metadata, or if citation snippets over chunk byte ranges differ from `Chunk.Text`.
