# IndexFreshness warnings validation scenarios

## 010-index-chunking-task-2-indexfreshness-warnings-scenario-1

Prepare an offline sanitized fixture cache in a temporary SQLite database with five repo-scoped source records:

- `DOC-MISSING`: source exists with sync metadata but no indexed chunks.
- `DOC-CONTENT`: source content hash differs from the persisted indexed chunk hash.
- `DOC-REVISION`: source content hash matches the persisted chunk, but the current remote/sync revision and updated timestamp are newer than indexed chunk metadata.
- `DOC-FRESH`: source content hash, revision metadata, updated timestamp, and chunk metadata all match.
- `DOC-LINK`: source and chunk metadata are fresh, but a cached derived link points at a missing target.

## 010-index-chunking-task-2-indexfreshness-warnings-scenario-2

Run product read surfaces against the offline fixture cache and require visible warnings/diagnostics:

- CLI `stale-index --format json` includes `missing_index`, `stale_index`, `stale_index_revision`, and `link_stale_only` warning records, while `DOC-FRESH` remains `fresh` with no warning code.
- CLI `cache-status --format json` includes index freshness warning counts for the non-fresh classifications.
- CLI `get-snippet --format json --source-id DOC-MISSING` returns an empty chunk result with `missing_index` warning metadata instead of silent success.
- MCP JSON-RPC tool calls exercise the configured MCP server path. MCP `stale_index_report` includes `missing_index`, `stale_index_revision`, and `link_stale_only`; MCP `get_snippet` includes the same missing warning metadata.
- CLI `export-snapshot --format json` includes snapshot warning metadata for missing/stale/link-only freshness states.

## 010-index-chunking-task-2-indexfreshness-warnings-scenario-3

Validate decommission behavior for `decommission-8`: `get-snippet` and `export-snapshot` must not silently return empty chunks or omit citations/chunk data for unindexed records. The fixture uses `DOC-MISSING` to prove both surfaces return explicit warning metadata when chunks are absent.

## 010-index-chunking-task-2-indexfreshness-warnings-scenario-4

Run executable offline evidence through runtime/product paths:

- Go index runtime test package for freshness classification.
- CLI integration over a temporary fixture cache.
- MCP JSON-RPC interaction over stdio using the production MCP server package.
- Snapshot export via CLI over the same fixture cache.

The validation asserts deterministic ordering by comparing two `stale-index --format json` outputs byte-for-byte and checking warning/source order.
