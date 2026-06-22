# Validation Scenarios: 017 Live sync pagination and partial failure collection

## 017-internal-provider-live-task-2-add-live-sync-pagination-partial-failure-collect-scenario-1

`sync --live` after a previous sync reflects a delta or zero delta when nothing changed, and unchanged records are not duplicated.

Offline materialization exercises the production service bulk live-sync path with a stubbed external GitCode client:

1. Configure an in-memory cache repository binding for issue scope.
2. Run `BulkSyncIssues` through the production `Service` with a paginated issue listing provided by the stub external provider.
3. Assert issue records are written to the real cache store.
4. Run `BulkSyncIssues` again with unchanged remote issue/comment data.
5. Assert every result reports `ZeroDelta`, fetched records are skipped rather than inserted/updated, and the cache still has exactly the original issue count.

## 017-internal-provider-live-task-2-add-live-sync-pagination-partial-failure-collect-scenario-2

Partial failure where some fetched resources fail reports success/failure counts and does not crash.

Offline materialization exercises the production service bulk live-sync path with a stubbed external GitCode client:

1. Configure an in-memory cache repository binding for wiki scope.
2. Run `BulkSyncWiki` through the production `Service` with a wiki listing containing one valid page and one missing page.
3. Assert the call returns a `PartialSyncError`, not an unhandled crash.
4. Assert the result summary reports one success and one failure.
5. Assert the human-readable error includes success/failure counts and the successfully fetched wiki page is committed to the real cache.
