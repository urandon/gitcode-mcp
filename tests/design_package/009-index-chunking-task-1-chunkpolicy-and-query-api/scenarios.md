# Validation Scenarios: 009 ChunkPolicy and Query API

## 009-index-chunking-task-1-chunkpolicy-and-query-api-scenario-1
A developer indexes sanitized fixture issue, wiki, and changelog records through the index runtime using default options and then using `sliding_window`; CLI `list-chunks`/`get-snippet` and MCP `list_chunks`/`search_chunks`/`get_snippet` return chunks through the shared `ChunkQueryResult` shape.

Executable validation:
- Builds a temporary offline SQLite cache.
- Adds sanitized issue, wiki, and changelog records.
- Indexes each record through `internal/index.ChunkSourceWithOptions` with default heading options and `sliding_window` options.
- Persists chunks to the production cache schema.
- Calls production CLI `list-chunks`, `search-chunks`, and `get-snippet` with JSON output.
- Calls production MCP stdio tools `list_chunks`, `search_chunks`, and `get_snippet` over the same cache-backed service.

## 009-index-chunking-task-1-chunkpolicy-and-query-api-scenario-2
Visible responses include deterministic IDs, repo/source/record/snapshot metadata when present, policy, content hashes, byte and line ranges, heading path, normalized text or snippet text, pagination fields, and warning metadata.

Executable validation:
- Decodes CLI `ChunkQueryResult` JSON and MCP `structuredContent` as `service.ChunkQueryResult`.
- Fails if any returned chunk lacks ID, repo/source/record/snapshot metadata, policy, content hash, byte range, line range, heading path, or normalized/snippet text.
- Fails if pagination fields are not visible and stable.
- Fails if the JSON response omits the `warnings` metadata field.

## 009-index-chunking-task-1-chunkpolicy-and-query-api-scenario-3
Executable evidence is local indexing tests plus CLI and MCP read tests over sanitized fixtures proving two repeated runs produce identical chunk IDs/ranges/content, both policies remain queryable, limit/offset ordering is stable, and CLI/MCP list/snippet responses are parity-compatible over the same query result model.

Executable validation:
- Runs focused local index tests for determinism, boundaries, and query contracts.
- Re-indexes the same sanitized fixtures twice in the validation test and compares IDs, byte/line ranges, text, hashes, metadata, and policies.
- Verifies heading and `sliding_window` chunks are separately queryable and do not collide.
- Verifies limit/offset ordering returns distinct adjacent chunks in deterministic order.
- Compares CLI and MCP `ChunkQueryResult` payloads for list and snippet parity over the same cache contents.
