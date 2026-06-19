# Validation scenarios for 016-rag-ready-corpus-task-2-add-corpus-snapshot-apis

## 016-rag-ready-corpus-task-2-add-corpus-snapshot-apis-scenario-1

A developer runs `go test ./internal/service/... -run TestExportDeterminism`; the service API populates cache state with nested metadata and aliases, exports twice through `ExportSnapshot(ctx, {Format:"json"})`, exports twice through `ExportSnapshot(ctx, {Format:"markdown"})`, and receives byte-identical output within each format.

Offline validation command: `go test ./internal/service/... -run '^TestExportDeterminism$' -count=1`.

## 016-rag-ready-corpus-task-2-add-corpus-snapshot-apis-scenario-2

A developer runs `go test ./internal/service/... -run TestExportIncludesChunkProvenance`; the JSON snapshot contains source ids plus chunk `id`, `byte_start`, `byte_end`, `line_start`, `line_end`, `heading_path`, `content_hash`, `inherited_metadata`, `outbound_links`, and `resolved_aliases`.

Offline validation command: `go test ./internal/service/... -run '^TestExportIncludesChunkProvenance$' -count=1`.

## 016-rag-ready-corpus-task-2-add-corpus-snapshot-apis-scenario-3

A developer runs `go test ./internal/service/... -run TestDiffSnapshot`; the service compares `base` from exported JSON bytes to `head` from current cache state and reports added/removed/changed source and chunk categories with changed field names.

Offline validation command: `go test ./internal/service/... -run '^TestDiffSnapshot$' -count=1`.

## 016-rag-ready-corpus-task-2-add-corpus-snapshot-apis-scenario-4

Running `gitcode-mcp export --format json` after fixture ingest returns valid deterministic JSON suitable for the Day 7 offline walkthrough, and the test verifies no legacy markdown index or source file read occurs during export/diff after prior ingest/index.

Concrete offline validation:

1. Run CLI export and diff package tests through `go test ./internal/cli/... -run 'Test.*Export' -count=1` and `go test ./internal/cli/... -run 'Test.*Diff' -count=1`.
2. Run the real CLI runtime path with a temp cache: `go run ./cmd/gitcode-mcp ingest --cache-path <temp-db>`, then `go run ./cmd/gitcode-mcp index --cache-path <temp-db>`.
3. Export JSON twice through `go run ./cmd/gitcode-mcp export --format json --cache-path <temp-db>`.
4. Assert both exports are byte-identical, parse as a snapshot JSON payload, and require a non-empty corpus with sources and chunk provenance.
5. Ensure the exported JSON contains no legacy markdown-index/source-file sentinel.

## 016-rag-ready-corpus-task-2-add-corpus-snapshot-apis-scenario-5

If `gitcode-mcp export --format sqlite` is exposed, a service test verifies it can be loaded and logically diffed against the same current cache state, without asserting byte-identical SQLite file output.

Concrete offline validation:

1. Probe `gitcode-mcp export --format sqlite` against the same temp cache.
2. If the command rejects the format, record the scenario as not exposed and pass this conditional scenario.
3. If the command succeeds, require that service-level SQLite snapshot diff coverage exists and passes without byte-identical SQLite assertions.
