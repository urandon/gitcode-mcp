# Validation Scenarios

## 015-cache-store-task-4-add-minimum-cache-state-test-scenario-1
A developer triggers `go test ./internal/cache/... -run TestMinimumReplacementCacheState`; the cache API ingests fixture-like source records into an in-memory store, searches for `backlog`, lists ready task records, retrieves `DOC-123`, resolves backlinks for `DOC-123`, and reports sync status without any network dependency.

Executable validation: `TestMinimumReplacementCacheState` must pass as a cache package product-path test. It must use the real in-memory SQLite `Store` implementation and `UpsertSourceGraph` for ingest-like records, then exercise `SearchSources`, `ListSources`, `GetSource`, `ResolveAlias`, `ListLinks`, `GetBacklinks`, and `GetSyncStatus` against cache tables only.

## 015-cache-store-task-4-add-minimum-cache-state-test-scenario-2
The expected visible/state outcome is that each Store call returns the data needed by the future CLI/MCP Day 7 route and the test fails if any result depends on shell-readable markdown indexes rather than cache tables.

Executable validation: `TestMinimumReplacementCacheState` must assert visible Day 7 data: the `backlog` search result contains stable id/path/title/snippet for `DOC-123`, ready task listing returns `TASK-015`, `GetSource("DOC-123")` returns metadata/body/labels/hash/timestamps, alias resolution maps `wiki:DOC-123` to `DOC-123`, backlinks are driven by `links.target_id = "DOC-123"`, and sync status returns the persisted fresh remote state. The run script also composes this scenario with existing backlink and identity tests to catch regressions to markdown-path-only behavior.

## 015-cache-store-task-4-add-minimum-cache-state-test-scenario-3
Running `go test ./... -short` includes this cache scenario as executable evidence for the broader Day 7 walkthrough.

Executable validation: the run script executes `go test ./... -short -count=1` after the focused cache scenario. This remains offline and deterministic by relying on local Go tests, in-memory cache state, and any repository tests that skip live integrations unless explicitly opted in.
