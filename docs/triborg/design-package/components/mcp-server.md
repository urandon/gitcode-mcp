# Design Package Component: mcp-server

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: MCP Server & Transport

## Summary
The MCP server component is materially affected because the approved architecture promotes it from a partial stdio read bridge to a repo-aware, cache-first MCP product surface with dual stdio and HTTP/SSE transports. The component work is limited to read-only MCP tools, transport/session behavior, readiness, request correlation, and cache-concurrency integration.

## Top-Level Alignment
This component owns architecture component `a8`: MCP tool parity with CLI reads, stdio and HTTP/SSE transports, health/readiness routes, correlation IDs, and multi-client shared-cache reads. It implements Component Impact deltas `mcp-server-delta-1`, `mcp-server-delta-2`, and `mcp-server-delta-3` without adding sync, write, migration, or live-network mutation paths.

## Tasks

### Task 1: Tool registry parity
Outcome IDs: outcome-6
Outcome Role: primary_product
Decommission IDs: decommission-1
Change Type: add
Description: Extend the MCP read tool surface so coordinator clients can perform the approved cache-first read workflow without falling back to shell-only commands. The local MCP tool registry, argument schemas, dispatcher, and deterministic response presenters become the component-owned contract for repo-aware read parity. This task covers only read tools and keeps MCP mutation-free.
Existing Behavior / Reuse: The existing MCP server already has stdio JSON-RPC handling, `initialize`, `tools/list`, `tools/call`, tool schema structs, domain error mapping, and some service-backed cache read tools. The current surface lacks the approved complete repo-aware tool set: `get_snippet`, `recent_changes`, `link_check`, `stale_index_report`, `cache_status`, `search_chunks` or `list_chunks`, and repo-aware `sync_status`. Existing JSON-RPC framing, read service calls, and domain error writer are reused; coordinator shell-only read reliance is replaced for the covered queries.
Detailed Design: Add or formalize a component-local `ToolRegistry` around the existing static tool definitions and dispatcher. Each registered tool has a name, JSON input schema, argument decoder, service request adapter, deterministic CLI/MCP result presenter, and structured content presenter. Register only read tools: existing cache read tools plus `get_snippet`, `recent_changes`, `link_check`, `stale_index_report`, `cache_status`, `search_chunks` or `list_chunks`, repo-aware `sync_status`, backlinks, export, and diff where already exposed as read operations.

Define MCP argument structs for every repo-scoped read tool with `repo_id` as a required field at the MCP schema boundary. Alias-bearing requests such as issue/wiki ids are rejected with JSON-RPC `-32602` when `repo_id` is absent; no MCP tool may rely on global alias resolution. The MCP adapter forwards `repo_id` into service and repo-binding request objects. Where an existing service request type currently lacks repo scope, introduce a repo-scoped request variant or add a required `RepoID` field at the service boundary used by MCP, and route unresolved downstream gaps as implementation dependencies for `repo_binding` or cache services rather than making MCP schema optional.

Use a shared deterministic read envelope for CLI-equivalent MCP responses. The envelope includes `repo_id`, source type, record/chunk/snapshot identifiers, payload data, warnings, stale or missing-index flags, pagination metadata, and typed error data where applicable. MCP structured content must match the equivalent CLI JSON payload for the same cache state whenever a CLI command has an MCP equivalent; if MCP also emits a text item, the text item is derived from the same envelope and must not omit warning state. Pagination uses bounded `limit` and `offset`; snippet ranges must be positive and ordered; chunk query/list tools require either query text or list pagination; malformed arguments return `-32602`.

Replace the product contract that coordinator reads require shell commands by making every covered query reachable through `tools/call`. Keep any CLI-oriented helpers internal to CLI tests or shared presenters and do not expose a coordinator-only shell path from MCP. Enforce the read-only invariant by keeping the dispatcher allowlist mutation-free: unknown tool names return `unknown_tool`, and sync/write/create/update/comment/migration tool names are not registered. Extend domain error mapping for stale-index, missing-index, busy/locked, not-found, and validation failures so clients receive typed JSON-RPC errors rather than generic failures.
Acceptance Criteria: An MCP client initializes the server over stdio, calls `tools/list`, then invokes existing and new read tools through `tools/call` with required `repo_id` filters against a two-repository fixture cache. Responses include snippet, recent changes, link check, stale-index report, cache status, chunk search/list, backlinks, export/diff where present, and sync status; each response carries repo scope, deterministic payload data, warnings, stale/missing-index fields, pagination metadata where applicable, and typed errors matching the shared read envelope. Equivalent CLI read commands over the same fixture cache produce byte-equivalent JSON payloads for the canonical envelope fields, including warnings, repo id, errors, and pagination metadata; executable evidence is an MCP JSON-RPC/server test plus a CLI parity test over issue and wiki records with network disabled.
Workload: 1.6 MM

### Task 2: HTTPSSE session transport
Outcome IDs: outcome-7
Outcome Role: primary_product
Decommission IDs: decommission-5
Change Type: add
Description: Add an HTTP/SSE MCP transport while retaining stdio for single local clients. The MCP server becomes a transport-neutral JSON-RPC runtime with stdio and HTTP/SSE adapters around the same tool registry and read service calls. This task owns localhost default binding, MCP-compatible SSE session establishment, `/message` routing, health/readiness routes, request correlation, and graceful shutdown.
Existing Behavior / Reuse: The current runtime starts MCP only through a stdio path and processes newline-delimited JSON-RPC frames from stdin/stdout. Existing JSON-RPC request/response structs, initialization behavior, tool dispatch, and service-backed read handlers are reused. There is no product HTTP/SSE server mode, session registry, `/health`, `/ready`, `/sse`, `/message`, request correlation, or multi-client entrypoint, so stdio-only as the sole product transport is replaced while stdio remains a compatibility transport.
Detailed Design: Split the server into a transport-neutral `RPCHandler` and two adapters: `StdioTransport` and `HTTPSSETransport`. `RPCHandler.Handle(ctx, clientSession, request)` accepts a decoded JSON-RPC request and returns a JSON-RPC response, notification outcome, or typed transport error. It owns `initialize`, `tools/list`, and `tools/call` dispatch and is shared by both transports. `StdioTransport` preserves the current newline-delimited single-client behavior and continues to reserve stdout for JSON-RPC frames.

Add `HTTPSSETransport` with `ServerConfig` carrying bind address, readiness probe, logger, shutdown context, request-id generator, session-id generator, and per-session queue limits. Register `GET /health`, `GET /ready`, `GET /sse`, and `POST /message`. `GET /sse` establishes an MCP SSE session, assigns an opaque server-owned `session_id` and internal client id, stores a `ClientSession` in a session registry, sends an initial SSE `endpoint` event containing the `/message?session_id=<id>` target, and then streams JSON-RPC responses or notifications as SSE events. The session registry owns lifecycle state, outbound response channel, cancellation context, last activity time, and cleanup on disconnect.

`POST /message` must be correlated to a live SSE session by explicit `session_id` query parameter or approved header. It rejects missing, unknown, closed, or expired sessions with a typed HTTP/JSON-RPC transport error and does not create implicit sessions. For a valid session, `/message` decodes exactly one JSON-RPC request, attaches the session context and request id, calls `RPCHandler`, and routes the JSON-RPC response to that session’s SSE stream rather than returning it as an ordinary one-request/one-response JSON body. The HTTP response to `/message` acknowledges receipt with accepted status and correlation headers; the actual MCP response is delivered on `/sse`. If the SSE stream disconnects before delivery, the request context is cancelled and the response is dropped with a structured log entry.

Implement correlation by reading `X-Request-ID`; when absent, generate an opaque id, attach it to the request context, include the same header on `/health`, `/ready`, `/sse`, and `/message` responses, and include it in structured logs. `/health` reports process liveness. `/ready` reports whether the cache is readable and at least one configured repo is available through the readiness probe. Add command/startup routing for `gitcode-mcp mcp serve --transport stdio` and `gitcode-mcp mcp serve --transport http-sse --bind 127.0.0.1:<port>`, with graceful shutdown on context cancellation or SIGINT/SIGTERM.

Clarify `decommission-5`: the replaced behavior is stdio-only as the sole product MCP transport. The retained behavior is stdio compatibility for one local client using the same `RPCHandler`. The negative invariant is that product multi-client MCP serving must not rely on stdio-only semantics; multi-client serving is provided by HTTP/SSE sessions only.
Acceptance Criteria: A developer starts `gitcode-mcp mcp serve --transport http-sse --bind 127.0.0.1:<port>` against a fixture cache. `GET /health` and `GET /ready` return successful status when the cache is readable; `GET /sse` establishes a session, emits an endpoint containing a session-correlated `/message` URL, and keeps valid SSE framing; `POST /message` with that session sends `initialize` and `tools/call` requests whose JSON-RPC responses are delivered on the matching SSE stream. Missing, unknown, or closed sessions are rejected with typed transport errors, two HTTP/SSE clients can issue concurrent read tool calls against the same cache, and `gitcode-mcp mcp serve --transport stdio` still works for one local client; executable evidence is a server/API test proving initialize and tool-call flow over `/sse` plus `/message`, and a local multi-client smoke command verifying `X-Request-ID` correlation in responses and logs.
Workload: 1.8 MM

### Task 3: Runtime cache readiness
Outcome IDs: outcome-7, outcome-8
Outcome Role: primary_product
Decommission IDs: decommission-5
Change Type: change
Description: Integrate MCP runtime readiness and multi-client reads with the shared SQLite cache concurrency model. The MCP server remains read-only, admits safe concurrent reads, and surfaces cache ownership or lock contention as typed runtime responses when those states affect readiness or read attempts. This task changes server admission, cancellation, and error behavior rather than adding cache locking.
Existing Behavior / Reuse: Existing MCP tool calls synchronously use a service interface backed by the cache store and already return JSON-RPC domain errors for some not-found or cache-empty states. The current stdio-only process has no multi-client admission layer, no readiness state tied to cache ownership, no HTTP request cancellation semantics, and no specific busy/owned response mapping for shared-cache operation. The cache component remains owner of WAL, writer locks, migrations, and busy semantics; MCP reuses those typed errors and does not implement its own database lock manager.
Detailed Design: Add an MCP-local `RuntimeState` or `ReadinessProbe` used by HTTP/SSE and stdio startup to report cache-open, repo-configured, and degraded states without performing network calls. Add an `AdmissionPolicy` for MCP requests with the invariant that all registered tools are read-only and admitted concurrently, while sync/write/migration requests are impossible because no such tools are registered. The policy marks readiness as not ready when the cache cannot be opened, migrations are required but blocked, or configured repo scope is unavailable; it keeps read requests admitted when the cache reports WAL-safe read availability.

Extend domain error translation to map cache busy/locked/owned errors to JSON-RPC server errors with structured `data.code` values such as `busy`, `cache_owned`, or `migration_blocked`, preserving active operation start time when the cache layer exposes it. `/ready` returns a non-ready status with the same typed reason while `/health` remains live if the process is running. Stdio uses the same mapping for equivalent cache failures, while preserving its single-client JSON-RPC flow.

Use per-request contexts derived from the HTTP request and SSE session context. If a `/message` request is cancelled, its downstream service call is cancelled and no response is enqueued after cancellation. If an SSE client disconnects, the session registry cancels the session context, closes its outbound channel, removes the session, and releases goroutines and timers. Slow or abandoned reads must not hold global transport locks; session writes are isolated per client with bounded queues, and a blocked or disconnected session cannot prevent other clients from receiving responses.

Enforce the read-only multi-client invariant from `decommission-5`: HTTP/SSE clients cannot trigger sync/index/write, cannot acquire the writer lock through MCP, and cannot turn a busy writer state into fake success. Cache WAL and writer ownership remain in the cache subsystem. MCP only observes readiness and maps typed errors from cache/service layers.
Acceptance Criteria: With one temporary SQLite cache, two MCP HTTP/SSE clients issue read tool calls while a sync/index operation in another process or test harness holds the writer lock; safe reads complete with normal JSON-RPC responses and any lock-affected operation returns typed `busy`, `cache_owned`, or `migration_blocked` error data instead of hanging or reporting success. `/ready` reflects unreadable or migration-blocked cache states, `/health` continues to report process liveness, and stdio returns the same typed JSON-RPC error mapping for equivalent cache failures. A runtime test cancels one HTTP `/message` request or disconnects one SSE client during a slow read, verifies the session is cleaned up and no goroutine/queue leak blocks the server, and verifies another client read succeeds concurrently; executable evidence is an MCP runtime concurrency test backed by the cache component’s temporary SQLite lock harness plus a server/API cancellation test.
Workload: 1.0 MM

## Cross-Cutting Constraints
- MCP read tools must remain cache-first and never call the live GitCode adapter — routine MCP reads must work offline after explicit sync.
- `repo_id` is mandatory for repo-scoped MCP read schemas — alias collision handling is owned by repository binding and cannot rely on global identity lookup.
- MCP and CLI equivalent reads must use the same deterministic result envelope or shared presenter semantics — parity includes repo scope, warnings, errors, and pagination metadata.
- HTTP/SSE must bind to localhost by default and include request correlation — this is part of the approved transport interoperability contract.
- HTTP/SSE session flow must establish a session on `/sse` and correlate `/message` to that session — this keeps the transport compatible with MCP SSE expectations.
- MCP must not expose write, sync, or migration tools — writer admission belongs to CLI/cache components while MCP is read-only.
- Shared-cache concurrency behavior must reuse SQLite WAL and typed cache errors rather than adding an MCP-owned lock manager — cache ownership remains with the cache subsystem.
- Tool responses must carry protocol version during initialization and keep schema changes explicit — protocol compatibility follows the MCP interoperability contract.

## Data And Control Flow
- MCP client sends `initialize` over stdio — `StdioTransport` decodes newline JSON-RPC — `RPCHandler` returns protocol version, server info, and tool capability on stdout.
- HTTP MCP client opens `GET /sse` — `HTTPSSETransport` creates `ClientSession`, stores session id, emits endpoint event, and holds the response stream until disconnect or shutdown.
- HTTP MCP client posts JSON-RPC to `/message?session_id=<id>` — transport validates live session, attaches `X-Request-ID`, calls `RPCHandler`, and routes JSON-RPC response to that session’s SSE stream.
- MCP client calls `tools/list` — `ToolRegistry` returns read-only tool definitions with required `repo_id` input schemas for repo-scoped reads — no service or cache mutation occurs.
- MCP client calls a read tool with `repo_id` — argument decoder validates bounds and scope — service read method queries cache/index/snapshot data — shared deterministic presenter emits structured content matching CLI-equivalent envelope.
- HTTP client calls `/ready` — `ReadinessProbe` checks cache-open, migration-blocked, and repo-configured state — route returns ready or typed degraded reason with `X-Request-ID`.
- Multiple HTTP/SSE clients call read tools concurrently — request contexts stay independent — cache WAL/read concurrency is provided by cache subsystem — MCP only maps typed contention errors.
- Client cancels `/message` or disconnects `/sse` — session/request context is cancelled, outbound queue is closed or drained, session registry removes the client, and other sessions continue independently.
- Process receives shutdown signal — HTTP/SSE transport stops accepting new sessions, closes SSE streams, lets in-flight reads finish within context deadline, and releases server resources.

## Component Interactions
- `mcp-server` -> `cache_sync` — MCP consumes cache-backed service reads, readiness probes, and typed cache errors; MCP never acquires writer ownership directly.
- `mcp-server` -> `index_chunking` — MCP chunk/snippet/stale-index tools expose deterministic chunk and index-readiness data produced by the index subsystem.
- `mcp-server` -> `cli_read` — MCP structured responses must match equivalent CLI read payloads through a shared result envelope or shared deterministic presenter where available.
- `cmd runtime` -> `mcp-server` — startup routing selects stdio or HTTP/SSE transport and passes cache path, config scope, logger, bind address, and shutdown context into the MCP server.
- `mcp-server` -> `repo_binding` — MCP tools require and forward `repo_id` so downstream reads are scoped and alias collisions are rejected by repository binding/cache logic.
- `mcp-server` -> `snapshot_diff` — export and diff read tools delegate stored snapshot lookup and not-found behavior to snapshot services while preserving MCP JSON-RPC error mapping.

## Rationale
The component is affected because Component Impact marks `mcp-server` as `detailed` and assigns it three product deltas: MCP read tool parity, dual stdio plus HTTP/SSE transport, and shared-cache concurrency integration. Existing behavior provides a partial stdio MCP JSON-RPC read surface, but the approved repo-aware tool set, mandatory repo-scoped schemas, MCP-compatible SSE sessions, HTTP readiness routes, correlation IDs, cancellation cleanup, and multi-client shared-cache behavior are absent or incomplete.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0226-run_attempt-1/final_message.txt`
