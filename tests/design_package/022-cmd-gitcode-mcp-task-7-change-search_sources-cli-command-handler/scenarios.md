# Validation Scenarios: Change search_sources CLI command handler

## Scenario Inventory

### 022-cmd-gitcode-mcp-task-7-change-search_sources-cli-command-handler-scenario-1

- **Description**: gitcode-mcp search_sources 'test' after fixture sync/index → non-empty results. gitcode-mcp search_sources 'notfound' → empty result set, no error.
- **Actor**: Operator running search_sources CLI command after fixture-backed cache population.
- **Expected Outcome**:
  - `gitcode-mcp search_sources --repo fixture-a backlog` returns source records with id, path, title, snippet fields.
  - JSON output (`--format json`) returns valid `SearchSourcesResult` with non-empty `results`.
  - `gitcode-mcp search_sources --repo fixture-a NONEXISTENT` returns exit code 0 with empty result set.
  - JSON output for non-matching query has `results: []` (empty array).
  - No `cache_empty` error on any search_sources path.
  - Text output for non-matching query produces empty output (no rows printed).
  - `gitcode-mcp search_sources --help` prints valid help text and exits 0.
- **Evidence Type**: CLI command execution; Go test assertions.
- **Freshness**: Current binary/test packages with fixture-synced cache data; offline only.
- **Mock Policy**: No mocks — uses real cache/store/service path via cacheBackedFactory and standalone binary.

## Decommission Verification

### decommission-7

- **Target**: search_sources returning cache_empty after successful sync/index.
- **Verification**: `search_sources` against fixture-populated cache returns non-empty results for matching query; returns exit 0 and empty result set for non-matching query (not an error). Existing Go tests `TestSearchSourcesCommandJSON` and `TestSearchSourcesCommandEmptyJSON` exercise the full service-backed CLI path and pass.
