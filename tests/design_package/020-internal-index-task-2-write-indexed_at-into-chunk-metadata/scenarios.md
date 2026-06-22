# Validation Scenarios: 020 internal-index indexed_at chunk metadata

## 020-internal-index-task-2-write-indexed_at-into-chunk-metadata-scenario-1

A test runner invokes `go test -run TestChunkDeterminism ./internal/index/` against the production `internal/index` package. The exercised test must build chunks through the production chunking path, and each produced `Chunk.InheritedMetadata` must contain `indexed_at` as a non-zero timestamp parseable with `time.RFC3339Nano`.

## 020-internal-index-task-2-write-indexed_at-into-chunk-metadata-scenario-2

A test runner invokes `go test -run TestFreshnessReportClassifications ./internal/index/` against the production `internal/index` package. The exercised freshness report must consume chunks carrying `indexed_at` in `InheritedMetadata`; the `DOC-FRESH` record must have non-zero `IndexedAt` and `State` equal to `IndexFreshnessFresh`.

## 020-internal-index-task-2-write-indexed_at-into-chunk-metadata-scenario-3

A test runner invokes `go test -run TestChunkPolicyDeterminismAndMetadata ./internal/index/` against the production `internal/index` package. The pre-existing policy determinism and metadata test must pass while verifying that `indexed_at` is present in chunk metadata and excluded from deterministic identity comparisons.
