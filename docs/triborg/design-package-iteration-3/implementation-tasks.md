# Design Package Implementation Tasks

This file is copied from the approved Triborg design package during implementator preflight.

# Implementation Tasks

Run ID: gitcode-mcp-live-readiness-design-agent-20260621T160404Z-gitcode_mcp_live_readiness_iteration_3

1. [ ] Add CLI provider mode resolution via --live flag a (`001-cmd-gitcode-mcp-task-1-add-cli-provider-mode-resolution-via---live-flag-a`)
   - Component: `cmd-gitcode-mcp`
   - Change Type: add
   - Source: `components/cmd-gitcode-mcp.md#task-1-add-cli-provider-mode-resolution-via---live-flag-a`

2. [ ] Add auth status CLI command (`002-cmd-gitcode-mcp-task-2-add-auth-status-cli-command`)
   - Component: `cmd-gitcode-mcp`
   - Change Type: add
   - Source: `components/cmd-gitcode-mcp.md#task-2-add-auth-status-cli-command`

3. [ ] Add Live write commands (create-issue, create-comm (`003-cmd-gitcode-mcp-task-3-add-live-write-commands-create-issue-create-comm`)
   - Component: `cmd-gitcode-mcp`
   - Change Type: add
   - Source: `components/cmd-gitcode-mcp.md#task-3-add-live-write-commands-create-issue-create-comm`

4. [ ] Add doctor command (`004-cmd-gitcode-mcp-task-4-add-doctor-command`)
   - Component: `cmd-gitcode-mcp`
   - Change Type: add
   - Source: `components/cmd-gitcode-mcp.md#task-4-add-doctor-command`

5. [ ] Add migrate-cache CLI command (`005-cmd-gitcode-mcp-task-5-add-migrate-cache-cli-command`)
   - Component: `cmd-gitcode-mcp`
   - Change Type: add
   - Source: `components/cmd-gitcode-mcp.md#task-5-add-migrate-cache-cli-command`

6. [ ] Change CLI help wiring for all subcommands (`006-cmd-gitcode-mcp-task-6-change-cli-help-wiring-for-all-subcommands`)
   - Component: `cmd-gitcode-mcp`
   - Change Type: change
   - Source: `components/cmd-gitcode-mcp.md#task-6-change-cli-help-wiring-for-all-subcommands`

7. [ ] Service factory selects provider mode (`007-internal-service-task-1-service-factory-selects-provider-mode`)
   - Component: `internal-service`
   - Change Type: change
   - Source: `components/internal-service.md#task-1-service-factory-selects-provider-mode`

8. [ ] Add ProviderMode enum and provider factory dispatc (`008-internal-provider-task-1-add-providermode-enum-and-provider-factory-dispatc`)
   - Component: `internal-provider`
   - Change Type: add
   - Source: `components/internal-provider.md#task-1-add-providermode-enum-and-provider-factory-dispatc`

9. [ ] Add Live GitCode REST API adapter package (`009-internal-provider-live-task-1-add-live-gitcode-rest-api-adapter-package`)
   - Component: `internal-provider-live`
   - Change Type: add
   - Source: `components/internal-provider-live.md#task-1-add-live-gitcode-rest-api-adapter-package`

10. [ ] Add Credential pipeline with env→keychain→none fal (`010-internal-credential-task-1-add-credential-pipeline-with-env-keychain-none-fal`)
   - Component: `internal-credential`
   - Change Type: add
   - Source: `components/internal-credential.md#task-1-add-credential-pipeline-with-env-keychain-none-fal`

11. [ ] Add Auth status reporting and invalid-token diagno (`011-internal-credential-task-2-add-auth-status-reporting-and-invalid-token-diagno`)
   - Component: `internal-credential`
   - Change Type: add
   - Source: `components/internal-credential.md#task-2-add-auth-status-reporting-and-invalid-token-diagno`

12. [ ] Add Schema version detection and compatibility che (`012-internal-cache-task-1-add-schema-version-detection-and-compatibility-che`)
   - Component: `internal-cache`
   - Change Type: add
   - Source: `components/internal-cache.md#task-1-add-schema-version-detection-and-compatibility-che`

13. [ ] Add Version-2-to-version-3 cache schema migration (`013-internal-cache-task-2-add-version-2-to-version-3-cache-schema-migration`)
   - Component: `internal-cache`
   - Change Type: add
   - Source: `components/internal-cache.md#task-2-add-version-2-to-version-3-cache-schema-migration`

14. [ ] Add SyncResources for partial-failure collection (`014-internal-sync-task-1-add-syncresources-for-partial-failure-collection`)
   - Component: `internal-sync`
   - Change Type: add
   - Source: `components/internal-sync.md#task-1-add-syncresources-for-partial-failure-collection`

15. [ ] Add StartedAt/CompletedAt to SyncEvent (`015-internal-sync-task-2-add-startedat-completedat-to-syncevent`)
   - Component: `internal-sync`
   - Change Type: change
   - Source: `components/internal-sync.md#task-2-add-startedat-completedat-to-syncevent`

16. [ ] Add ZeroDelta to SyncResult with persistent event (`016-internal-sync-task-3-add-zerodelta-to-syncresult-with-persistent-event`)
   - Component: `internal-sync`
   - Change Type: add
   - Source: `components/internal-sync.md#task-3-add-zerodelta-to-syncresult-with-persistent-event`

17. [ ] Add Live sync: pagination, partial failure collect (`017-internal-provider-live-task-2-add-live-sync-pagination-partial-failure-collect`)
   - Component: `internal-provider-live`
   - Change Type: add
   - Source: `components/internal-provider-live.md#task-2-add-live-sync-pagination-partial-failure-collect`

18. [ ] Add Live write: idempotency gate and conflict dete (`018-internal-provider-live-task-3-add-live-write-idempotency-gate-and-conflict-dete`)
   - Component: `internal-provider-live`
   - Change Type: add
   - Source: `components/internal-provider-live.md#task-3-add-live-write-idempotency-gate-and-conflict-dete`

19. [ ] Use body hash for staleness in freshness (`019-internal-index-task-1-use-body-hash-for-staleness-in-freshness`)
   - Component: `internal-index`
   - Change Type: change
   - Source: `components/internal-index.md#task-1-use-body-hash-for-staleness-in-freshness`

20. [ ] Write indexed_at into chunk metadata (`020-internal-index-task-2-write-indexed_at-into-chunk-metadata`)
   - Component: `internal-index`
   - Change Type: change
   - Source: `components/internal-index.md#task-2-write-indexed_at-into-chunk-metadata`

21. [ ] Change search_sources FTS query routing and empty- (`021-internal-search-task-1-change-search_sources-fts-query-routing-and-empty`)
   - Component: `internal-search`
   - Change Type: change
   - Source: `components/internal-search.md#task-1-change-search_sources-fts-query-routing-and-empty`

22. [ ] Change search_sources CLI command handler (`022-cmd-gitcode-mcp-task-7-change-search_sources-cli-command-handler`)
   - Component: `cmd-gitcode-mcp`
   - Change Type: change
   - Source: `components/cmd-gitcode-mcp.md#task-7-change-search_sources-cli-command-handler`

23. [ ] Add Audit trail with idempotency key storage and w (`023-internal-audit-task-1-add-audit-trail-with-idempotency-key-storage-and-w`)
   - Component: `internal-audit`
   - Change Type: add
   - Source: `components/internal-audit.md#task-1-add-audit-trail-with-idempotency-key-storage-and-w`

24. [ ] Add Doctor aggregator with subsystem introspection (`024-internal-doctor-task-1-add-doctor-aggregator-with-subsystem-introspection`)
   - Component: `internal-doctor`
   - Change Type: add
   - Source: `components/internal-doctor.md#task-1-add-doctor-aggregator-with-subsystem-introspection`

25. [ ] Change MCP server tool registration and parity val (`025-internal-mcp-task-1-change-mcp-server-tool-registration-and-parity-val`)
   - Component: `internal-mcp`
   - Change Type: change
   - Source: `components/internal-mcp.md#task-1-change-mcp-server-tool-registration-and-parity-val`

26. [ ] Change MCP tool schemas with corrected kind enums (`026-internal-mcp-tools-task-1-change-mcp-tool-schemas-with-corrected-kind-enums`)
   - Component: `internal-mcp-tools`
   - Change Type: change
   - Source: `components/internal-mcp-tools.md#task-1-change-mcp-tool-schemas-with-corrected-kind-enums`

27. [ ] Add HTTP/SSE transport handler with health/readine (`027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine`)
   - Component: `internal-mcp-transport`
   - Change Type: add
   - Source: `components/internal-mcp-transport.md#task-1-add-http-sse-transport-handler-with-health-readine`

28. [ ] Add two-cache live e2e test harness (`028-internal-e2e-task-1-add-two-cache-live-e2e-test-harness`)
   - Component: `internal-e2e`
   - Change Type: add
   - Source: `components/internal-e2e.md#task-1-add-two-cache-live-e2e-test-harness`

29. [ ] Add Redaction filter for log/print/output intercep (`029-internal-diagnostics-task-1-add-redaction-filter-for-log-print-output-intercep`)
   - Component: `internal-diagnostics`
   - Change Type: add
   - Source: `components/internal-diagnostics.md#task-1-add-redaction-filter-for-log-print-output-intercep`

30. [ ] Create live-readiness operator guide (`030-docs-task-1-create-live-readiness-operator-guide`)
   - Component: `docs`
   - Change Type: add
   - Source: `components/docs.md#task-1-create-live-readiness-operator-guide`

31. [ ] Document provider and credential flow (`031-docs-task-2-document-provider-and-credential-flow`)
   - Component: `docs`
   - Change Type: change
   - Source: `components/docs.md#task-2-document-provider-and-credential-flow`

32. [ ] Document sync and migration policy (`032-docs-task-3-document-sync-and-migration-policy`)
   - Component: `docs`
   - Change Type: change
   - Source: `components/docs.md#task-3-document-sync-and-migration-policy`

33. [ ] Create sanitization rules document (`033-docs-task-4-create-sanitization-rules-document`)
   - Component: `docs`
   - Change Type: add
   - Source: `components/docs.md#task-4-create-sanitization-rules-document`
