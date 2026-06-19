# Scenarios: Add Service Query API

## 005-agent-query-surface-task-1-add-service-query-api-scenario-1

A developer runs `go test ./internal/service/... -run 'Test(SearchSources|GetSource|ListSources|GetBacklinks|ResolveID|GetSnippet|GetSyncStatus|RecentChanges|LinkCheck|StaleIndex|ExportSnapshot|DiffSnapshot)'`; the target product API route is `internal/service.Service`, the trigger is populated in-memory cache data and cache/index projections, and the expected outcome is stable ids, paths, titles, snippets, backlinks, resolved remote aliases, exact or clamped line snippets, freshness fields, recent changes, broken-link reports, stale-index reports, and deterministic snapshot/diff DTOs returned without network access.

Executable evidence: `run.sh` invokes `go test ./internal/service/... -run 'Test(SearchSources|GetSource|ListSources|GetBacklinks|ResolveID|GetSnippet|GetSyncStatus|RecentChanges|LinkCheck|StaleIndex|ExportSnapshot|DiffSnapshot)' -count=1`. The tests exercise the production `internal/service.Service` methods over local in-memory cache state and cache/index projections.

## 005-agent-query-surface-task-1-add-service-query-api-scenario-2

A developer runs `go test ./internal/service/... -run TestMCPToolDTOContract`; the trigger is service calls corresponding only to the approved first-slice MCP tools `search_sources`, `get_source`, `list_sources`, `source_backlinks`, `resolve_id`, `sync_status`, `export_snapshot`, and `diff_snapshot`, and the expected outcome is DTOs containing fields required by MCP `structuredContent` while no `recent`, `link_check`, or `stale_index` MCP tool is required.

Executable evidence: `run.sh` invokes `go test ./internal/service/... -run TestMCPToolDTOContract -count=1`. The test exercises the shared service DTO route intended for MCP structured content without starting a live MCP transport or adding deferred MCP tools.

## 005-agent-query-surface-task-1-add-service-query-api-scenario-3

A developer runs `go test ./internal/service/... -run TestQueryEdgeCases`; the trigger is cache-empty, not-found, invalid query, equal-score search results, missing line metadata, overlong snippet range, stale index projection, and broken links, and the expected outcome is typed service errors or warning fields, deterministic tie ordering, null/zero line fallback, clamped snippet warnings, and cache-backed stale/link reports.

Executable evidence: `run.sh` invokes `go test ./internal/service/... -run TestQueryEdgeCases -count=1` and also runs the scenario-1 service tests that include clamped snippets, stale-index projection, and broken-link report coverage. The validation fails if edge cases are not represented by typed service errors, deterministic ordering, or warning/result DTOs.

## 005-agent-query-surface-task-1-add-service-query-api-scenario-4

A developer runs `go test ./internal/service/... -run TestQueryMethodsDoNotUseLiveNetwork`; the trigger is service methods with no GitCode client configured, and the expected outcome is all routine query reads complete from cache only.

Executable evidence: `run.sh` invokes `go test ./internal/service/... -run TestQueryMethodsDoNotUseLiveNetwork -count=1` with GitCode credential environment variables unset. The test exercises all routine service query methods through a `Service` constructed only with a cache store.
