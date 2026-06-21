# Outcome Contract

Schema Version: `triborg.outcome-contract.v1`

## outcome-1
- Request Task: 1
- Role: `supporting_evidence`
- Request Item: Runtime provider-selection wiring for fixture default and live opt-in
- Target Surface: CLI command: gitcode-mcp sync --live, gitcode-mcp bind --live; service.New factory; go test ./...
- Actor/Trigger: Operator runs gitcode-mcp sync --live with a bound live repo vs go test ./...
- Expected Outcome: sync --live reaches GitCode API; sync without --live uses fixture; go test ./... stays offline and passes
- Evidence Type: CLI command
- Freshness: Current binary with --live flag support and live env vars set
- Mock Policy: external_dependencies_only


## outcome-2
- Request Task: 2
- Role: `primary_product`
- Request Item: Credential handling with env token baseline and optional native Keychain provider
- Target Surface: CLI command: gitcode-mcp auth status; credential pipeline; env var GITCODE_TOKEN; macOS Keychain integration
- Actor/Trigger: Operator runs gitcode-mcp auth status with and without GITCODE_TOKEN set, and with keychain-stored token
- Expected Outcome: auth status reports token source (env/keychain/none) with redacted value; no-token diagnostics list available sources; invalid token produces clear auth-failure diagnostic
- Evidence Type: CLI command
- Freshness: Current binary with auth status command and credential subsystem
- Mock Policy: external_dependencies_only


## outcome-3
- Request Task: 3
- Role: `supporting_evidence`
- Request Item: Live sync behavior for GitCode issues, comments, and wiki pages
- Target Surface: CLI command: gitcode-mcp sync --live; cache database; sync event records
- Actor/Trigger: Operator runs gitcode-mcp sync --live against a configured test repo
- Expected Outcome: Cache populates with real issue records, comments, wiki pages, and identities from live API; rate-limit handled gracefully; re-sync produces delta event without duplicates
- Evidence Type: CLI command
- Freshness: Current binary with live sync; live repo and token configured
- Mock Policy: external_dependencies_only


## outcome-4
- Request Task: 4
- Role: `supporting_evidence`
- Request Item: Live write behavior for issues, comments, wiki pages, and labels
- Target Surface: CLI command: gitcode-mcp create-issue --live; gitcode-mcp create-comment --live; gitcode-mcp create-wiki-page --live; audit trail; cache database
- Actor/Trigger: Operator runs create-issue --live with idempotency key, dry-run, and conflict scenarios
- Expected Outcome: Issue created on GitCode with cached record; duplicate idempotency key reports 'already applied'; dry-run validates without remote call; conflict detected and reported
- Evidence Type: CLI command
- Freshness: Current binary with live write support; live repo and token configured
- Mock Policy: external_dependencies_only


## outcome-5
- Request Task: 5
- Role: `supporting_evidence`
- Request Item: Redacted two-cache e2e harness proving remote source of truth
- Target Surface: Go test: go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/; cache databases A and B
- Actor/Trigger: Operator runs e2e test with live env vars set
- Expected Outcome: Two independent caches from same live repo contain equivalent data; output contains no private URLs, tokens, or raw API responses; caches cleaned up on completion
- Evidence Type: API test
- Freshness: Current e2e test binary with live repo and token env vars
- Mock Policy: external_dependencies_only


## outcome-6
- Request Task: 6
- Role: `primary_product`
- Request Item: Fix stale-index false positives after sync and index
- Target Surface: CLI command: gitcode-mcp sync_status; index engine freshness contract
- Actor/Trigger: Operator runs sync --index or index --full and checks sync_status
- Expected Outcome: sync_status reports zero stale_index entries after fresh sync/index; reports only modified sources after content change
- Evidence Type: CLI command
- Freshness: Current binary with fixed index freshness
- Mock Policy: no_mocks


## outcome-7
- Request Task: 7
- Role: `supporting_evidence`
- Request Item: Fix source search so search_sources works after sync/index
- Target Surface: CLI command: gitcode-mcp search_sources; MCP tool: search_sources
- Actor/Trigger: Operator runs search_sources after sync/index; MCP client invokes search_sources tool
- Expected Outcome: search_sources returns non-empty results for matching queries; returns empty set for non-matching queries; no cache_empty error
- Evidence Type: CLI command
- Freshness: Current binary with fixed search_sources; synced cache
- Mock Policy: no_mocks


## outcome-8
- Request Task: 8
- Role: `supporting_evidence`
- Request Item: Fix MCP tool schemas and docs for real source kind enums
- Target Surface: MCP tool schemas for list_sources, search_sources, search_chunks; MCP inspector
- Actor/Trigger: MCP client queries tool schemas; operator runs gitcode-mcp --mcp and inspects with MCP inspector
- Expected Outcome: Kind enums include issue and wiki; legacy enums replaced or supplemented
- Evidence Type: API test
- Freshness: Current MCP server binary
- Mock Policy: no_mocks


## outcome-9
- Request Task: 9
- Role: `primary_product`
- Request Item: Fix CLI help and command discovery for valid subcommands
- Target Surface: CLI help: gitcode-mcp --help, gitcode-mcp <subcommand> --help
- Actor/Trigger: Operator runs --help on root and every registered subcommand
- Expected Outcome: All subcommands return valid help text, exit 0, no invalid-query diagnostics
- Evidence Type: CLI command
- Freshness: Current binary
- Mock Policy: no_mocks


## outcome-10
- Request Task: 10
- Role: `primary_product`
- Request Item: Cache schema compatibility and migration for old databases
- Target Surface: CLI command: gitcode-mcp (implicit on open); gitcode-mcp migrate-cache; cache database
- Actor/Trigger: Operator opens an iteration 1 or 2 cache database with iteration 3 binary; operator runs migrate-cache against old cache
- Expected Outcome: Schema version mismatch reported with actionable message; migration upgrades schema in place without data loss for compatible schemas; iteration 1 cache reports incompatibility
- Evidence Type: CLI command
- Freshness: Current binary; iteration 1 and 2 cache database files
- Mock Policy: no_mocks


## outcome-11
- Request Task: 11
- Role: `primary_product`
- Request Item: Doctor/readiness output for operator self-service setup
- Target Surface: CLI command: gitcode-mcp doctor
- Actor/Trigger: Operator runs gitcode-mcp doctor with various configurations (live, no-binding, no-token)
- Expected Outcome: Doctor reports version, config, cache, repo binding, token source (redacted), live provider, auth probe, last sync, index freshness, MCP transport — all public-safe; no-binding reports 'no repo bound'; no-token reports available sources
- Evidence Type: CLI command
- Freshness: Current binary with doctor command
- Mock Policy: external_dependencies_only


## outcome-12
- Request Task: 12
- Role: `supporting_evidence`
- Request Item: MCP parity validation for connected-client reads with live cache
- Target Surface: MCP server: gitcode-mcp --mcp over stdio and HTTP/SSE; all MCP read tools
- Actor/Trigger: MCP client connects to gitcode-mcp --mcp after live sync and invokes all read tools
- Expected Outcome: All MCP read tools return correct results against live-synced cache; HTTP/SSE and stdio wrapper paths work
- Evidence Type: API test
- Freshness: Current MCP binary; live-synced cache
- Mock Policy: external_dependencies_only


## outcome-13
- Request Task: 13
- Role: `supporting_evidence`
- Request Item: Documentation updates after iteration 3
- Target Surface: docs/architecture.md, docs/live-readiness.md, docs/cache-and-sync-model.md, README.md, MCP tool schema docs
- Actor/Trigger: New operator reads docs/live-readiness.md and follows setup guide; maintainer reviews architecture.md
- Expected Outcome: Operator can configure repo, supply token, run live sync, perform live write, and validate cache from docs alone; architecture.md includes provider-selection and credential flow; public-safety rules documented; live sync semantics documented
- Evidence Type: local command
- Freshness: Current docs in repository after iteration 3 implementation
- Mock Policy: no_mocks
