# Validation Scenarios: 021 internal-search search_sources FTS routing and empty behavior

## 021-internal-search-task-1-change-search_sources-fts-query-routing-and-empty-scenario-1

Validate offline cache/service search behavior for `search_sources` after fixture sync/index-equivalent cache population:

- a matching query (`test` through the service fixture path and `backlog` through the cache FTS projection path) returns non-empty source records with source fields such as id/path/title/snippet;
- a non-matching query (`NONEXISTENT`) returns an empty result set with no error;
- a bound repository with no sources returns an empty result set with no `ErrCacheEmpty`;
- deleting the FTS projection after source ingest is repaired on first source search and returns the expected records;
- FTS-enabled and fallback stores produce equivalent visible source results, including kind-filtered queries.

This scenario is exercised by package tests:

- `go test ./internal/cache -run 'TestMinimumReplacementCacheState|TestSearchFallbackParity' -count=1`
- `go test ./internal/service -run 'TestSearchSources|TestFixtureProviderSearchSourcesSmoke' -count=1`

## 021-internal-search-task-1-change-search_sources-fts-query-routing-and-empty-scenario-2

Validate offline MCP runtime behavior for the `search_sources` tool:

- the MCP server is started over local stdio pipes;
- a JSON-RPC `tools/call` invocation for `search_sources` against fixture cache data succeeds without an MCP error;
- structured content decodes as `service.SearchSourcesResult`;
- returned records are source records with source identifiers and paths, not chunk-only records.

This scenario is exercised by:

- `go test ./internal/mcp -run TestIntegration -count=1`
