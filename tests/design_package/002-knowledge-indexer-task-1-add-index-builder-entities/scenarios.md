# Validation Scenarios: 002-knowledge-indexer-task-1-add-index-builder-entities

## 002-knowledge-indexer-task-1-add-index-builder-entities-scenario-1
A developer triggers the product index path by running `go test ./internal/index/... -run TestIndexPipeline` against cache-backed fixture sources; `FullBuild` returns success, derived projections contain source-ledger/task-backlog/backlink/broken-link data, `StaleCheck` returns a JSON-serializable stale backlink count and affected source ids, and `IncrementalBuild` on unchanged sources reports no rewrites and zero new stale items.

Executable evidence: `run.sh` invokes the exact focused index pipeline test trigger. The test exercises `FullBuild`, `StaleCheck`, `IncrementalBuild`, the `SourceReader`, `DerivedWriter`, and `DerivedLinkReader` contracts through local fixture records and fails if projections, stale output, or no-rewrite incremental behavior are missing.

## 002-knowledge-indexer-task-1-add-index-builder-entities-scenario-2
A developer triggers the chunking product path by running `go test ./internal/index/... -run TestChunkDeterminism`; a markdown source produces chunks with correct byte offsets, line ranges, heading paths, inherited metadata, outbound links, resolved aliases, and a second run over the same source produces identical chunk ids.

Executable evidence: `run.sh` invokes the exact focused chunk determinism test trigger. The test exercises `ParseSource` and `ChunkSource` against local markdown fixture input and fails if chunk ids or payloads are nondeterministic or if offset, line, heading, metadata, outbound-link, or alias provenance is incorrect.

## 002-knowledge-indexer-task-1-add-index-builder-entities-scenario-3
A developer runs `go test ./internal/index/... -run TestCitationAnchors`; heading/task/acceptance anchors include correct source id, content hash, byte offsets, line ranges, heading paths, and are emitted through `DerivedWriter` so service/cache adapters can satisfy `GetSnippet` and citation lookup by anchor.

Executable evidence: `run.sh` invokes the exact focused citation-anchor test trigger. The test exercises anchor production through `FullBuild` and writer emission, and fails if heading, task/status, acceptance, or chunk anchors are missing required provenance fields or are not attached to derived rows.

## 002-knowledge-indexer-task-1-add-index-builder-entities-scenario-4
A developer runs `go test ./internal/index/... -run TestParserLinkEdgeCases`; malformed frontmatter yields parse diagnostics and body parsing continues, duplicate ids produce collision diagnostics and block affected commits, duplicate same-source aliases dedupe, ambiguous cross-source aliases produce ambiguous diagnostics and broken-link rows, unresolved relative paths produce broken-link rows, CRLF line ranges are normalized, and UTF-8 byte offsets remain original-byte accurate.

Executable evidence: `run.sh` invokes the exact focused parser/link edge-case test trigger. The test exercises local parser, alias resolver, diagnostics, broken-link generation, collision blocking, CRLF line normalization, and invalid UTF-8 handling without network access.

## 002-knowledge-indexer-task-1-add-index-builder-entities-scenario-5
A developer runs `go test ./... -short` and the index tests complete without network access, proving cache-first behavior used by `gitcode-mcp index --full`, `gitcode-mcp index --incremental`, and `gitcode-mcp stale-index`.

Executable evidence: `run.sh` invokes `go test ./... -short -count=1` after the focused index tests. This exercises all packages in short mode and fails on compile errors, package dependency violations, or network-dependent tests that do not skip cleanly.
