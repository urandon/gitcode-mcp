# Outcome Contract

Schema Version: `triborg.outcome-contract.v1`

## outcome-1
- Request Task: 1
- Role: `supporting_evidence`
- Request Item: MCP lifecycle/control-plane tool surface with repo_status, sync_live, index_repo, auth_status, doctor; unsupported_capability for writes; name-based tool registry
- Target Surface: MCP server tools/list and tools/call over stdio transport
- Actor/Trigger: MCP client calls tools/list against gitcode-mcp --mcp server with writable cache
- Expected Outcome: tools/list returns repo_status, sync_live, index_repo, auth_status, doctor; calling repo_status on empty cache returns nothing bound; calling sync_live --issues returns sync event with fresh count; calling index_repo invokes Service.Index; calling create_issue returns unsupported_capability without credential lookup or HTTP call; tool registry resolves handlers by name, not positional index
- Evidence Type: API test
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-2
- Request Task: 2
- Role: `primary_product`
- Request Item: MCP startup/readiness diagnostics with minimal MCP server fallback when cache/service init fails
- Target Surface: MCP server initialization and tools/list response over stdio transport
- Actor/Trigger: MCP client starts gitcode-mcp --mcp server with cache path pointing to read-only directory, or schema-incompatible cache, or writer-locked cache, or injected cache init failure before Service creation
- Expected Outcome: tools/list always returns at least doctor tool; tool result or server capability includes typed diagnostic (cache_path_unwritable, schema_incompatible, cache_lock_contention, or startup-failure); calling doctor returns structured diagnostic with actionable text
- Evidence Type: API test
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-3
- Request Task: 3
- Role: `supporting_evidence`
- Request Item: Bounded collection sync with cancellation, progress, partial state, and internal wiki traversal changes
- Target Surface: CLI sync command and MCP sync_live tool over context-propagated HTTP traversal
- Actor/Trigger: User runs sync command or MCP sync_live tool with bounded page size and context cancelled before page 4 (issues) or mid-tree-traversal (wiki) or --timeout 2s on slow fixture
- Expected Outcome: Issues sync stops before page 4, returns PartialSyncError with success_count=30 and sync_cancelled diagnostic; --timeout triggers sync_timeout diagnostic; progress channel emits at least one event per page; wiki tree walker stops mid-level and returns PartialSyncError with committed records
- Evidence Type: API test
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-4
- Request Task: 4
- Role: `supporting_evidence`
- Request Item: Empty wiki bootstrap behavior with typed empty_wiki diagnostic and optional POST bootstrap
- Target Surface: CLI sync command and MCP sync_live tool against empty wiki; CLI create-page --live against empty wiki
- Actor/Trigger: User syncs or creates page against repo with uninitialized {repo}.wiki
- Expected Outcome: Sync returns empty_wiki diagnostic with actionable remediation text; create-page either bootstraps wiki via POST Home.md (201 + follow-up GET confirm) or returns unsupported_wiki_uninitialized with remediation path
- Evidence Type: API test
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-5
- Request Task: 5
- Role: `supporting_evidence`
- Request Item: Initialized wiki path normalization (Home.md not Home.md.md) and create-page write confirmation decoding with follow-up GET verification
- Target Surface: Wiki sync cache record path and create-page write confirmation via POST /api/v5/repos/{repo}.wiki/contents/{path}
- Actor/Trigger: User syncs wiki with Home.md remote page or creates wiki page against provider returning 201 without path/sha in body
- Expected Outcome: Cached record path is wiki/Home.md; create-page confirms via follow-up GET returning expected content+sha, or returns write_confirmation_incomplete; if follow-up GET confirms, record is cached with http_attempted=true
- Evidence Type: API test
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-6
- Request Task: 6
- Role: `supporting_evidence`
- Request Item: Credential resolver parity: auth status, read probes, and write commands use same resolver pipeline
- Target Surface: CLI auth status, add-comment --live, create-issue --live commands with shared CredentialResolver
- Actor/Trigger: User runs auth status and then write command in same environment with env var GITCODE_TOKEN or multiple credential sources
- Expected Outcome: auth status reports credential present and add-comment --live includes bearer token; no-credential env fails both auth status and create-issue with credential_unavailable before HTTP call; multi-source env picks same source for both commands
- Evidence Type: CLI command
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-7
- Request Task: 7
- Role: `supporting_evidence`
- Request Item: Issue label omission (no labels key when unset) and add-comment response decoding with http_attempted and schema_decode classification
- Target Surface: Issue create/update POST payload serialization and add-comment POST /api/v5/repos/{owner}/{repo}/issues/{num}/comments response decoding
- Actor/Trigger: User creates issue without labels, updates issue title only, or posts comment against provider returning live-shaped or malformed response
- Expected Outcome: Issue create/update bodies omit labels key; add-comment decodes live {id, note_id, body, created_at, user} response with http_attempted=true and caches comment; malformed body yields schema_decode diagnostic with http_attempted=true
- Evidence Type: API test
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-8
- Request Task: 8
- Role: `supporting_evidence`
- Request Item: PR read/comment route schema and cache design with kind: pull_request, kind: pr_comment, and deployment-inhibited route exclusion
- Target Surface: GitCode live adapter GET/POST /api/v5/repos/{owner}/{repo}/pulls and /pulls/{number}/comments routes with cache projection
- Actor/Trigger: User lists PRs, fetches PR detail, lists PR comments, or posts PR comment via live adapter
- Expected Outcome: PR list caches pull_request records with number-derived source_id; PR detail returns cached record; PR comments caches pr_comment records linked to parent PR; PR comment write receives 201 with {id, note_id, body} and caches with http_attempted=true; deployment-inhibited routes never called
- Evidence Type: API test
- Freshness: current binary build with mocked Go tests
- Mock Policy: external_dependencies_only


## outcome-9
- Request Task: 9
- Role: `supporting_evidence`
- Request Item: Cache lock/concurrency diagnostic: typed cache_busy for lock contention, concurrent reads not blocked by writer, readers succeed while writer held
- Target Surface: Cache layer read/write paths under concurrent goroutine access
- Actor/Trigger: Multiple concurrent read commands (search_sources) against same cache; writer hold during concurrent reads; three goroutines (two readers + one writer)
- Expected Outcome: Two concurrent reads complete without internal_error; writer contention yields cache_busy not internal_error; writer hold does not block readers; readers complete while writer is held; writer diagnostic is typed cache_busy
- Evidence Type: runtime/compiler test
- Freshness: current binary build with mocked Go tests
- Mock Policy: no_mocks
