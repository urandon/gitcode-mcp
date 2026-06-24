# Outcome Contract

Schema Version: `triborg.outcome-contract.v1`

## outcome-1
- Request Task: 1
- Role: `supporting_evidence`
- Request Item: Produce a route/schema matrix for Issues, Labels, Milestones, Pull Requests, Comments, and Wiki.
- Target Surface: GitCode live adapter route/schema coverage for Issues, Labels, Milestones, Pull Requests, Comments, and Wiki
- Actor/Trigger: Developer runs go test ./... against mocked GitCode live routes
- Expected Outcome: Each selected or deferred surface is exercised through target runtime code paths and either parses GitCode-shaped responses or returns intended unsupported diagnostics
- Evidence Type: runtime/compiler test
- Freshness: Current source and test run evidence from the implementation branch
- Mock Policy: external_dependencies_only


## outcome-2
- Request Task: 2
- Role: `supporting_evidence`
- Request Item: Issue live paths reached the real service, but implementation must tolerate numeric id and string number.
- Target Surface: /api/v5/repos/{owner}/{repo}/issues live provider read path and cache/MCP issue reads
- Actor/Trigger: Sync actor runs mocked live issue read tests with numeric id and string number payloads
- Expected Outcome: Issue records are accepted, normalized into stable source/cache identifiers, and exposed through CLI or MCP cache reads; malformed identity fields are classified as schema diagnostics
- Evidence Type: API test
- Freshness: Current mocked live-shaped fixture and target runtime test evidence
- Mock Policy: external_dependencies_only


## outcome-3
- Request Task: 3
- Role: `supporting_evidence`
- Request Item: Specify label request and response models that match GitCode, including normalization into cache source labels; decide add-label behavior.
- Target Surface: CLI issue create/update/add-label commands and GitCode issue/label adapter models
- Actor/Trigger: Operator runs stubbed CLI issue create/update/add-label flows
- Expected Outcome: Labels are encoded as GitCode-accepted JSON strings, returned label objects normalize into cache source labels, array-payload regressions fail, and unsupported old add-label route behavior is not silently used
- Evidence Type: CLI command
- Freshness: Current source and mocked live-shaped command tests
- Mock Policy: external_dependencies_only


## outcome-4
- Request Task: 4
- Role: `supporting_evidence`
- Request Item: Specify milestone adapter/cache behavior or explicitly defer it with documented rationale.
- Target Surface: Milestone-related CLI/MCP read/sync behavior and cache model
- Actor/Trigger: User triggers milestone-related target CLI or MCP read/sync path
- Expected Outcome: Implemented milestone records parse and cache correctly, or deferred milestone access returns clear unsupported capability diagnostics without misleading error classes
- Evidence Type: API test
- Freshness: Current target tests using mocked GitCode milestone responses or unsupported diagnostics
- Mock Policy: external_dependencies_only


## outcome-5
- Request Task: 5
- Role: `supporting_evidence`
- Request Item: Specify PR/comment adapter/cache behavior or explicitly defer it with documented rationale.
- Target Surface: Pull Request and comment CLI/MCP read/sync behavior and cache model
- Actor/Trigger: User triggers target MCP or CLI read/sync surfaces for Pull Requests and comments
- Expected Outcome: Implemented PR/comment routes produce cached readable records, or deferred routes return explicit unsupported capability diagnostics with no silent empty-cache success
- Evidence Type: API test
- Freshness: Current target tests using mocked external GitCode PR/comment responses or diagnostics
- Mock Policy: external_dependencies_only


## outcome-6
- Request Task: 6
- Role: `primary_product`
- Request Item: Decide wiki strategy using /api/v5 wiki-as-repository as primary candidate and browser web-api only after stable non-cookie credential validation.
- Target Surface: /api/v5/repos/{owner}/{repo}.wiki/contents, /contents/{path}, and /raw/{path} provider paths; optional wiki write commands/tools
- Actor/Trigger: Wiki actor triggers target sync/read and any exposed write path
- Expected Outcome: Wiki traversal, path encoding, raw decoding, base64 create/update, sha update/delete, and errors are handled on /api/v5 wiki-as-repository routes; browser web-api routes are not used by default product execution
- Evidence Type: API test
- Freshness: Current target tests using sanitized live-shaped wiki fixtures
- Mock Policy: external_dependencies_only


## outcome-7
- Request Task: 7
- Role: `supporting_evidence`
- Request Item: Specify error classification cleanup for 400 responses, malformed JSON, schema mismatches, and partial response diagnostics.
- Target Surface: CLI/MCP error reporting and GitCode live provider error taxonomy
- Actor/Trigger: Operator triggers CLI and MCP paths backed by mocked 400, malformed JSON, schema mismatch, credential, and network failures
- Expected Outcome: API validation, schema/decode, credential/configuration, and transport failures are visibly distinct; 400/schema/decode errors are not reported as network outages
- Evidence Type: API test
- Freshness: Current target source and regression tests
- Mock Policy: external_dependencies_only


## outcome-8
- Request Task: 8
- Role: `primary_product`
- Request Item: Specify cache provenance or live-cache reset/isolation behavior for fixture-to-live transition.
- Target Surface: Cache sync/read state, provenance metadata, and reset/isolation command or state contract
- Actor/Trigger: User runs target sync/cache commands across fixture-mode and live-mode inputs
- Expected Outcome: Cache reads expose provenance or isolation state, fixture-to-live transitions are deterministic, and stale fixture records cannot masquerade as live GitCode data
- Evidence Type: CLI command
- Freshness: Current target command/runtime tests
- Mock Policy: external_dependencies_only


## outcome-9
- Request Task: 9
- Role: `supporting_evidence`
- Request Item: Decide MCP write exposure for issue create/update/labels, or keep MCP read-only and document CLI writes as the supported mutation path.
- Target Surface: MCP tool registry and CLI mutation commands
- Actor/Trigger: MCP client triggers advertised target MCP tools and CLI user triggers mutation commands
- Expected Outcome: MCP write tools are either implemented with tested idempotent behavior or omitted/read-only with clear unsupported capability responses; CLI writes remain the supported mutation path if MCP writes are deferred
- Evidence Type: API test
- Freshness: Current MCP server and CLI tests
- Mock Policy: external_dependencies_only


## outcome-10
- Request Task: 10
- Role: `supporting_evidence`
- Request Item: Acceptance target: go test ./... passes without real credentials, network, SSH agent, or OS Keychain access; optional real-live smoke remains credential-gated and redacted.
- Target Surface: Repository validation workflow and optional live smoke workflow
- Actor/Trigger: Developer runs go test ./... and git diff --check locally; optional operator runs credential-gated live smoke
- Expected Outcome: Offline validation passes without external services; optional live smoke skips safely or runs with redacted output when credentials are present
- Evidence Type: local command
- Freshness: Current local command output from implementation branch
- Mock Policy: external_dependencies_only
