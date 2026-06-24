# Scenarios: cache provenance layer task 1

## 002-cache-provenance-layer-task-1-add-cache-provenance-schema-read-filters-sync-wr-scenario-1

Validates the full acceptance path for cache provenance, read filters, sync writes, and live reset/isolation per the architecture contract:

### A. Schema and provenance constants
1. Provenance constants `cache.ProvenanceFixture` (`fixture`) and `cache.ProvenanceLive` (`live`) exist.
2. `records.provenance` CHECK constraint accepts `fixture` and `live` values (schema v11 migration).
3. `sources.provenance` column exists with `'fixture'|'live'` CHECK constraint (schema v10+).

### B. Sync-provenance wiring
4. Fixture-mode sync writes `provenance=fixture` to both `sources` and `records` tables.
5. Live-mode sync writes `provenance=live` to both `sources` and `records` tables.
6. `SourceFilter` has a `Provenance` field and `SetProvenance` setter.
7. `SearchQuery` has a `Provenance` field and `SetProvenance` setter.
8. `RecordFilter` has a `Provenance` field and executable provenance filter.
9. `ListRecords` filters by provenance when `RecordFilter.Provenance` is set.
10. `SearchRecords` filters by provenance when `SearchQuery.Provenance` is set.

### C. Read exposure through service DTOs
11. `ListSources` exposes both fixture and live provenance in output.
12. `SearchSources` exposes provenance in `SearchSourceResult`.
13. `GetSource` exposes provenance in `SourceRecord`.
14. `GetSyncStatus` exposes provenance in `SyncStatusResult`.
15. Batch `SyncStatus` exposes non-empty provenance on every result.

### D. Reset and isolation
16. `ResetLive` is an executable store method on `Store` interface.
17. `ResetLive` clears live-origin records from both `sources` and `records`.
18. Live-origin sources are not readable after `ResetLive`.
19. Fixture-origin sources survive `ResetLive`.
20. After reset, `ListSources` shows only fixture sources.
21. After reset, `SearchSources` shows no live-origin results.
22. Fixture-origin records cannot masquerade as live through `GetSource` or `GetSyncStatus`.

### E. Offline gateway
23. `go test ./...` passes without credentials, network, SSH agent, or Keychain.
24. `git diff --check` passes.

Expected result: the script exits zero when all provenance behaviors are correctly implemented; non-zero when any behavior is missing or broken.
