# Design Package Implementation Tasks

This file is copied from the approved Triborg design package during implementator preflight.

# Implementation Tasks

Run ID: gitcode-mcp-live-operations-design-agent-20260624T183454Z-gitcode_mcp_live_operations_iteration_6

1. [ ] Change MCP tool registry (internal/mcp/tools.go) (`001-internal-mcp-task-1-change-mcp-tool-registry-internal-mcp-tools.go`)
   - Component: `internal-mcp`
   - Change Type: change
   - Source: `components/internal-mcp.md#task-1-change-mcp-tool-registry-internal-mcp-tools.go`

2. [ ] Add Lifecycle MCP tools (internal/mcp/lifecycle_to (`002-internal-mcp-task-2-add-lifecycle-mcp-tools-internal-mcp-lifecycle_to`)
   - Component: `internal-mcp`
   - Change Type: add
   - Source: `components/internal-mcp.md#task-2-add-lifecycle-mcp-tools-internal-mcp-lifecycle_to`

3. [ ] Add Unsupported capability handler (internal/mcp/u (`003-internal-mcp-task-3-add-unsupported-capability-handler-internal-mcp-u`)
   - Component: `internal-mcp`
   - Change Type: add
   - Source: `components/internal-mcp.md#task-3-add-unsupported-capability-handler-internal-mcp-u`

4. [ ] Add Minimal MCP server construction path (internal (`004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal`)
   - Component: `internal-mcp`
   - Change Type: add
   - Source: `components/internal-mcp.md#task-4-add-minimal-mcp-server-construction-path-internal`

5. [ ] Add Startup diagnostic injection (internal/mcp/sta (`005-internal-mcp-task-5-add-startup-diagnostic-injection-internal-mcp-sta`)
   - Component: `internal-mcp`
   - Change Type: add
   - Source: `components/internal-mcp.md#task-5-add-startup-diagnostic-injection-internal-mcp-sta`

6. [ ] Validate MCP lifecycle tool integration tests (int (`006-internal-mcp-task-6-validate-mcp-lifecycle-tool-integration-tests-int`)
   - Component: `internal-mcp`
   - Change Type: validate
   - Source: `components/internal-mcp.md#task-6-validate-mcp-lifecycle-tool-integration-tests-int`

7. [ ] Validate MCP startup/readiness diagnostic tests (i (`007-internal-mcp-task-7-validate-mcp-startup-readiness-diagnostic-tests-i`)
   - Component: `internal-mcp`
   - Change Type: validate
   - Source: `components/internal-mcp.md#task-7-validate-mcp-startup-readiness-diagnostic-tests-i`

8. [ ] Add SyncBounds struct and bounded sync (internal/s (`008-internal-service-task-1-add-syncbounds-struct-and-bounded-sync-internal-s`)
   - Component: `internal-service`
   - Change Type: add
   - Source: `components/internal-service.md#task-1-add-syncbounds-struct-and-bounded-sync-internal-s`

9. [ ] Change index_repo service delegation (internal/ser (`009-internal-service-task-2-change-index_repo-service-delegation-internal-ser`)
   - Component: `internal-service`
   - Change Type: change
   - Source: `components/internal-service.md#task-2-change-index_repo-service-delegation-internal-ser`

10. [ ] Add Empty wiki diagnostic routing (internal/servic (`010-internal-service-task-3-add-empty-wiki-diagnostic-routing-internal-servic`)
   - Component: `internal-service`
   - Change Type: add
   - Source: `components/internal-service.md#task-3-add-empty-wiki-diagnostic-routing-internal-servic`

11. [ ] Change Bounded wiki tree traversal in ListWikiPage (`011-internal-gitcode-task-1-change-bounded-wiki-tree-traversal-in-listwikipage`)
   - Component: `internal-gitcode`
   - Change Type: change
   - Source: `components/internal-gitcode.md#task-1-change-bounded-wiki-tree-traversal-in-listwikipage`

12. [ ] Add Empty wiki detection (internal/gitcode/wiki_ad (`012-internal-gitcode-task-2-add-empty-wiki-detection-internal-gitcode-wiki_ad`)
   - Component: `internal-gitcode`
   - Change Type: add
   - Source: `components/internal-gitcode.md#task-2-add-empty-wiki-detection-internal-gitcode-wiki_ad`

13. [ ] Validate Bounded sync and partial state tests (int (`013-internal-service-task-4-validate-bounded-sync-and-partial-state-tests-int`)
   - Component: `internal-service`
   - Change Type: validate
   - Source: `components/internal-service.md#task-4-validate-bounded-sync-and-partial-state-tests-int`

14. [ ] Change Wiki path normalization (internal/gitcode/w (`014-internal-gitcode-task-3-change-wiki-path-normalization-internal-gitcode-w`)
   - Component: `internal-gitcode`
   - Change Type: change
   - Source: `components/internal-gitcode.md#task-3-change-wiki-path-normalization-internal-gitcode-w`

15. [ ] Add Wiki create-page write confirmation (internal/ (`015-internal-gitcode-task-4-add-wiki-create-page-write-confirmation-internal`)
   - Component: `internal-gitcode`
   - Change Type: add
   - Source: `components/internal-gitcode.md#task-4-add-wiki-create-page-write-confirmation-internal`

16. [ ] Change Issue label omission serialization (interna (`016-internal-gitcode-task-5-change-issue-label-omission-serialization-interna`)
   - Component: `internal-gitcode`
   - Change Type: change
   - Source: `components/internal-gitcode.md#task-5-change-issue-label-omission-serialization-interna`

17. [ ] Change Add-comment response decoding (internal/git (`017-internal-gitcode-task-6-change-add-comment-response-decoding-internal-git`)
   - Component: `internal-gitcode`
   - Change Type: change
   - Source: `components/internal-gitcode.md#task-6-change-add-comment-response-decoding-internal-git`

18. [ ] Add PR adapter with list/detail/comments routes (i (`018-internal-gitcode-task-7-add-pr-adapter-with-list-detail-comments-routes-i`)
   - Component: `internal-gitcode`
   - Change Type: add
   - Source: `components/internal-gitcode.md#task-7-add-pr-adapter-with-list-detail-comments-routes-i`

19. [ ] Validate Adapter integration tests (internal/gitco (`019-internal-gitcode-task-8-validate-adapter-integration-tests-internal-gitco`)
   - Component: `internal-gitcode`
   - Change Type: validate
   - Source: `components/internal-gitcode.md#task-8-validate-adapter-integration-tests-internal-gitco`

20. [ ] Change Cache lock strategy and WAL mode (internal/ (`020-internal-cache-task-1-change-cache-lock-strategy-and-wal-mode-internal`)
   - Component: `internal-cache`
   - Change Type: change
   - Source: `components/internal-cache.md#task-1-change-cache-lock-strategy-and-wal-mode-internal`

21. [ ] Add Schema version check against version 11 (inter (`021-internal-cache-task-2-add-schema-version-check-against-version-11-inter`)
   - Component: `internal-cache`
   - Change Type: add
   - Source: `components/internal-cache.md#task-2-add-schema-version-check-against-version-11-inter`

22. [ ] Validate Cache concurrency and lock diagnostics te (`022-internal-cache-task-3-validate-cache-concurrency-and-lock-diagnostics-te`)
   - Component: `internal-cache`
   - Change Type: validate
   - Source: `components/internal-cache.md#task-3-validate-cache-concurrency-and-lock-diagnostics-te`

23. [ ] Add Shared CredentialResolver struct (internal/aut (`023-internal-auth-task-1-add-shared-credentialresolver-struct-internal-aut`)
   - Component: `internal-auth`
   - Change Type: add
   - Source: `components/internal-auth.md#task-1-add-shared-credentialresolver-struct-internal-aut`

24. [ ] Validate Credential resolver parity tests (interna (`024-internal-auth-task-2-validate-credential-resolver-parity-tests-interna`)
   - Component: `internal-auth`
   - Change Type: validate
   - Source: `components/internal-auth.md#task-2-validate-credential-resolver-parity-tests-interna`
