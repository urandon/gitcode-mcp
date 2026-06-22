# Design Package Component: internal-index

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Index Engine

## Summary
The index engine produces false `stale_index` positives because `BuildFreshnessReport` prefers `source.Metadata["content_hash"]` over `ContentHash(source.Body)` for current-hash comparison and allows `source.PreviousIndexedHash` to overwrite `state.ContentHash`. Additionally, `inheritedMetadata` never writes `indexed_at` into chunk metadata, leaving `indexStateFromChunkMetadata` with zero timestamps for chunks not produced through `Builder.deriveSource`.

## Top-Level Alignment
Task 6 of the approved architecture assigns `internal/index/` the responsibility to fix stale-index false positives. The Component Impact delta `internal-index-delta-1` requires `indexed_at` set to current time on index completion and staleness determined solely by content hash comparison against the canonical source body hash. The decommission targets `decommission-6` (zero-time `indexed_at`, hash mismatch false positives).

## Tasks

### Task 1: Use body hash for staleness in freshness
Outcome IDs: outcome-6
Outcome Role: primary_product
Decommission IDs: decommission-6
Change Type: change
Description: `BuildFreshnessReport` computes `currentHash` from `source.Metadata["content_hash"]` first and falls back to `ContentHash(source.Body)`, and allows `source.PreviousIndexedHash` to overwrite `state.ContentHash`. Both paths mask staleness — the body-derived hash must be authoritative over metadata-stored hash, and `PreviousIndexedHash` must not contaminate the indexed hash used for comparison.
Existing Behavior / Reuse: `ContentHash` in `builder.go` is the canonical body hasher used by `Builder.build` and tests. `IndexState.ContentHash` is set from `parsed.ContentHash` in `Builder.deriveSource` and is the correct indexed hash. `source.PreviousIndexedHash` is used only in `Builder.build` for incremental skip decisions and must not appear in freshness comparison. The fix removes this field and its contamination path.
Detailed Design: In `BuildFreshnessReport` within `stale.go`, change the `currentHash` computation to prefer `ContentHash(source.Body)` when `source.Body != ""` and fall back to `source.Metadata["content_hash"]` only when the body is empty. Delete the block that assigns `source.PreviousIndexedHash` to `state.ContentHash`. Remove the `PreviousIndexedHash` field from `SourceRecord` struct in `types.go` and remove all remaining reads of it across the index package. The incremental skip in `Builder.build` continues using `contentHash` computed locally from `ContentHash(source.Body)`, which is independent of `PreviousIndexedHash`. Remove the `withPreviousHash` test helper and the `source.PreviousIndexedHash` assignment from `index_test.go`; the incremental skip test should instead verify that `Builder.build` compares the computed body hash against the stored `IndexState.ContentHash` from the prior build's derived record.
Acceptance Criteria: A test runner invokes `go test -run TestFreshnessReportClassifications ./internal/index/` with a source whose body content hash matches the chunk `ContentHash`; the produced `IndexFreshnessRecord.State` equals `IndexFreshnessFresh`. A test runner invokes `go test -run TestFreshnessReportClassifications ./internal/index/` with a source whose body hash differs from chunk `ContentHash`; the record state is `IndexFreshnessStaleByContent`. The `PreviousIndexedHash` field is removed from `SourceRecord` and `go test ./internal/index/` passes.
Workload: 0.15 MM

### Task 2: Write indexed_at into chunk metadata
Outcome IDs: outcome-6
Outcome Role: primary_product
Decommission IDs: decommission-6
Change Type: change
Description: `inheritedMetadata` in `chunks.go` writes `source_updated_at` but not `indexed_at` into `Chunk.InheritedMetadata`. When `indexStateFromChunkMetadata` reconstructs `IndexState` from chunks, `IndexedAt` remains `time.Time{}` for chunks not produced through `Builder.deriveSource`, causing false staleness for chunk-index-only reconstruction paths.
Existing Behavior / Reuse: `inheritedMetadata` already sets `source_updated_at` into chunk `InheritedMetadata`. `indexStateFromChunkMetadata` already reads `chunk.InheritedMetadata["indexed_at"]` and populates `state.IndexedAt` when the state's `IndexedAt` is zero. `Builder.deriveSource` already sets `IndexState.IndexedAt` to `time.Now().UTC()`. Only the write side in `inheritedMetadata` is missing.
Detailed Design: In `inheritedMetadata` function in `chunks.go`, add an `indexed_at` key write alongside the existing `source_updated_at` write: `metadata["indexed_at"] = time.Now().UTC().Format(time.RFC3339Nano)`. The `indexed_at` value is written together with `source_updated_at` so every chunk carries both timestamps. `indexStateFromChunkMetadata` in `stale.go` already handles the read side — it reads `indexed_at` from chunk metadata and merges it into `state.IndexedAt` when the state's `IndexedAt` is zero. The merge rule in `indexStateFromChunkMetadata` (zero-precedence) ensures chunk metadata never overwrites an already-set `IndexState.IndexedAt`. Update the `freshnessChunk` test helper in `freshness_test.go` to include `"indexed_at"` in `InheritedMetadata` so tests reflect the post-fix chunk shape.
Acceptance Criteria: A test runner invokes `go test -run TestChunkDeterminism ./internal/index/` and each produced `Chunk.InheritedMetadata` contains the key `indexed_at` with a non-zero RFC3339Nano timestamp. A test runner invokes `go test -run TestFreshnessReportClassifications ./internal/index/` with chunks carrying `indexed_at` in `InheritedMetadata`; the `DOC-FRESH` record has `IndexedAt` not equal to zero time and `State` equals `IndexFreshnessFresh`. The pre-existing `TestChunkPolicyDeterminismAndMetadata` test in `chunk_policy_test.go` passes and verifies `indexed_at` is present in chunk metadata.
Workload: 0.10 MM

## Cross-Cutting Constraints
- Cache-first determinism — index freshness must work with fixture-backed cache data and must not require live GitCode access for `go test ./...`.
- Hash-based staleness — `stale_index` is based on actual current source body hash versus indexed hash, while version and link warnings remain separate states.
- Public-safe diagnostics — freshness records and warnings expose hashes, source ids, timestamps, and states only; they must not introduce token, URL, or raw API output fields.

## Data And Control Flow
- Source body enters the index package as `SourceRecord.Body` — `ContentHash` computes the authoritative current hash before chunks are emitted.
- Chunk creation via `ChunkSourceWithOptions` → `inheritedMetadata` writes both `indexed_at` (new) and `source_updated_at` (existing) into `Chunk.InheritedMetadata` — cache storage persists those fields through the existing chunk record contract.
- `Builder.deriveSource` builds `IndexState` with `IndexedAt` set to `time.Now().UTC()` and `ContentHash` from `parsed.ContentHash` — this is the canonical indexed-at authority when `IndexStateReader` data is available.
- Freshness reporting reads sources and chunks — `BuildFreshnessReport` reconstructs `IndexState` from chunk stats, computes current hash from `ContentHash(source.Body)`, and classifies missing, stale-by-content, stale-by-version, link-stale, or fresh in that order.

## Component Interactions
- `internal/index` → `internal/cache` — chunks must carry `indexed_at` in `InheritedMetadata` so cache persistence can round-trip the timestamp for freshness reconstruction when `IndexStateReader` is unavailable.
- `internal/index` → `cmd/gitcode-mcp sync_status` — freshness records provide `State`, `WarningCode`, `CurrentContentHash`, `IndexedContentHash`, and `IndexedAt` for CLI status output.
- `internal/service` → `internal/index` — service indexing may call direct chunk builders or builder-derived paths, and both must produce the same hash/timestamp freshness invariants after this fix.

## Rationale
This component is affected because the approved top-level architecture places the stale-index false-positive fix in `internal/index/`. Two concrete bugs exist: (1) hash-selection priority in `BuildFreshnessReport` is inverted, preferring metadata over body hash; (2) `inheritedMetadata` never writes `indexed_at` into chunk metadata, leaving the reconstruction path with zero timestamps. Both bugs are localized to `stale.go`, `chunks.go`, and `types.go` within this component.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0650-run_attempt-1/final_message.txt`
