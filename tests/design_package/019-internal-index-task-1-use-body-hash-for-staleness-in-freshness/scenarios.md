# Validation Scenarios: 019 internal-index body hash freshness

## 019-internal-index-task-1-use-body-hash-for-staleness-in-freshness-scenario-1

A test runner invokes `go test -run TestFreshnessReportClassifications ./internal/index/` against the production `internal/index` package. The exercised test includes a source whose body content hash matches the chunk `ContentHash`; the produced `IndexFreshnessRecord.State` must equal `IndexFreshnessFresh`.

## 019-internal-index-task-1-use-body-hash-for-staleness-in-freshness-scenario-2

A test runner invokes `go test -run TestFreshnessReportClassifications ./internal/index/` against the production `internal/index` package. The exercised test includes a source whose body hash differs from the chunk `ContentHash`, while source metadata contains a misleading stale `content_hash`; the record state must be `IndexFreshnessStaleByContent` and the current hash must be derived from `SourceRecord.Body`.

## 019-internal-index-task-1-use-body-hash-for-staleness-in-freshness-scenario-3

The validation script invokes an offline Go reflection test against the production `internal/index.SourceRecord` type and then invokes `go test ./internal/index/`. The reflection check must not find a `PreviousIndexedHash` field, and the package compile-and-test path must pass, proving package consumers no longer require the removed field.
