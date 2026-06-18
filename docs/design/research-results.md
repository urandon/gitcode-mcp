# Research Results

## Research Index
# Research Index

## Competitor Research
- Model Context Protocol: `source document`
- GitHub CLI: `source document`
- GitLab CLI: `source document`

## External Research

## Competitor Analysis

### Reference Study: GitHub CLI

GitHub CLI implements issue/search/API workflows as Cobra command packages backed by injected factories, typed option structs, API clients, and output exporters.
The key architectural choice is to keep command parsing, business flow, API transport, pagination, auth/config, and presentation in separable layers connected through small interfaces and function injection.
The strongest transferable idea for `gitcode-mcp` is to expose cache-backed CLI commands through the same service layer as adapters and machine-readable JSON exporters, while keeping writes explicit and isolated.

### Reference Study: GitLab CLI

GitLab CLI implements issue and merge-request commands as noun-first Cobra command groups backed by a factory that supplies repo context, API clients, config, IO, and git state.
Its key architectural choice is to wrap `gitlab-org/api/client-go` lightly, put most command behavior in command packages, and centralize host/repo/auth resolution in `cmdutils`, `api`, and `glrepo`.
The strongest transferable idea for `gitcode-mcp` is a shared service/factory boundary that lets CLI commands resolve target project context, auth, pagination, JSON output, and safe/destructive metadata consistently.

### Reference Study: Model Context Protocol

MCP defines a JSON-RPC protocol for lifecycle negotiation, transports, tools, and resources that directly informs `gitcode-mcp`’s MCP read server boundary. Its key architectural choice is a thin protocol layer: initialize capabilities first, then expose model-controlled tools and application-driven resources over stdio or Streamable HTTP. The strongest transferable idea is to keep `gitcode-mcp`’s MCP server as an adapter over cache services, with deterministic `tools/list`, typed schemas, structured tool results, and stdio-first operation.

## External Research

## Executive Summary

- The strongest external evidence supports the MCP boundary, not the cache/index/sync architecture. MCP is JSON-RPC based, uses server capability negotiation, and exposes tools through `tools/list` and `tools/call` with JSON Schema input definitions (`ext1`, `ext2`, `ext7`, `ext8`).
- External evidence supports modeling `gitcode-mcp` agent access as explicit read tools over a local service boundary: MCP servers expose capabilities, resources, and tools, and tools are discoverable/invocable without implying any required live upstream network call (`ext2`, `ext8`, `ext11`).
- Side-effecting operations should be treated separately from routine reads. MCP safety guidance says tools that can take actions require explicit user consent, which supports making GitCode writes and sync operations explicit, gated paths rather than hidden behavior behind read tools (`ext6`).
- The evidence base does **not** establish GitCode `/api/v5` issue/wiki/comment/attachment behavior, SQLite schema details, deterministic chunking, cache freshness state machines, or plaintext knowledge-layer replacement mechanics. Those are design-critical local requirements that must be validated by fixtures and tests rather than borrowed as settled precedent.
- The highest-confidence research conclusion is therefore architectural separation: protocol surface, CLI surface, cache/query services, ingest/index pipeline, and live GitCode adapter should remain decoupled. MCP evidence strongly supports the server/tool contract; the rest must be treated as implementation policy with fixture-backed validation.

## Research Scope And Source Strategy

- Scope: external mechanisms relevant to a cache-first Go CLI/MCP tool for offline-capable GitCode tracker/wiki reads.
- Primary source family: official MCP specification, schema, and documentation because MCP protocol behavior is the most externally evidenced part of the request (`ext1`, `ext2`, `ext7`, `ext8`).
- Secondary source family: MCP reference/server ecosystem and SDK-style repositories, used only as lower-authority implementation context (`ext9`, `ext10`, `ext11`).
- Excluded from firm claims:
  - GitCode API specifics, because no retained source directly covers `https://docs.gitcode.com/docs/apis/` or `/api/v5`.
  - SQLite/FTS/cache-state-machine precedents, because no retained source covers them.
  - RAG chunk provenance precedents, because no retained source covers deterministic chunking, offsets, embeddings, or citation anchors.
  - Plaintext agent workflow replacement behavior, because it is a local/domain requirement rather than an external mechanism.

## Relevant Existing External Mechanisms

| Mechanism | External evidence | Relevance to later design | Limits |
|---|---|---|---|
| MCP JSON-RPC base protocol | MCP messages follow JSON-RPC 2.0 and use protocol-defined method names/capabilities (`ext7`, `ext8`). | Tool calls, errors, lifecycle, and integration tests should use JSON-RPC request/response framing. | Does not define cache semantics or GitCode behavior. |
| MCP server capabilities | `ServerCapabilities` includes a standard `tools` capability and is extensible (`ext1`). | A read-first server can advertise only read tools initially and add optional capabilities later. | Does not choose transport or Go package layout. |
| Tool discovery | MCP clients discover tools through `tools/list` (`ext2`). | Acceptance tests can assert all read tools appear in `tools/list`. | Tool names and semantics are product-specific. |
| Tool invocation | MCP clients invoke tools through `tools/call` (`ext2`). | Cache-backed read operations can be exposed as callable tools. | Does not require tools to be side-effect-free; design must enforce read/write separation. |
| Tool schema | MCP tool definitions include `name`, optional `description`, and `inputSchema` as JSON Schema (`ext2`). | Each agent-facing query must have a stable schema usable by clients. | Does not define response shape beyond MCP result conventions. |
| Resources vs tools | MCP servers can expose data through resources and functions through tools (`ext8`, `ext11`). | First slice can use tools for query operations; resources can later expose documents directly. | `ext11` is lower authority than official spec/docs. |
| Tool safety | MCP safety guidance says tools that take actions require explicit user consent (`ext6`). | GitCode writes and possibly live sync should be explicit, gated, logged operations. | Guidance is broad; idempotency keys and WAL behavior are local policy. |
| Reference server ecosystem | MCP reference servers include filesystem/git-style servers and other examples (`ext9`). | It is credible to expose local data/search behavior through MCP server tools. | Does not provide GitCode-specific or cache-first design evidence. |
| Go MCP protocol implementations | Third-party Go MCP protocol modules describe JSON-RPC, stdio/SSE support, and server/client contracts (`ext10`). | Go implementation is feasible, but the official protocol remains the authority. | Lower-authority source; should not override official schema/docs. |
| Streaming/progress discussions | Streaming tool result issues exist as discussion, not stable core mechanism (`ext12`). | Long-running sync/export progress should not be required for first slice. | Issue discussion is not a stable specification. |

## Design-Critical Unknowns

- GitCode API unknowns:
  - Auth header format, pagination envelope, issue field names, wiki page model, comments, attachments, search behavior, status codes, rate-limit headers, and conflict/error payloads are not established by retained external evidence.
  - Later design should isolate these behind adapter interfaces and fixture-backed contract tests.
- Cache/storage unknowns:
  - SQLite table shape, FTS choice, WAL mode, file locking, migrations, deterministic export ordering, and corruption recovery have no external support in this evidence set.
  - Later design should validate these by executable tests, not by citation.
- Knowledge-layer replacement unknowns:
  - `find`, `rg`, `sed`, backlink grep, stale pointer search, and coordinator intake workflows are local behavioral requirements.
  - Later design should document them as product contracts and prove them with offline walkthrough tests.
- RAG substrate unknowns:
  - Chunking granularity, byte offset calculation, heading path normalization, deterministic chunk IDs, embedding compatibility, and citation guarantees are local design decisions.
- Go implementation unknowns:
  - No high-authority official Go MCP SDK evidence was retained. Go transport/package decisions should remain loosely coupled to the protocol schema.

## Architecture Alternatives And Trade-Offs

### Alternative A: MCP-first live adapter

- Description: MCP tools call GitCode live APIs directly.
- External support: MCP tools can execute server-side functionality through `tools/call` (`ext2`).
- Trade-offs:
  - Strong protocol fit.
  - Poor fit for offline requirement.
  - Higher exposure to GitCode API ambiguity.
  - Harder to preserve stable local citation anchors.
- Applicability: useful only for optional explicit refresh/write tools, not routine reads.

### Alternative B: Cache-first service core with MCP and CLI adapters

- Description: ingest/sync populate a local cache; CLI and MCP call the same query services.
- External support: MCP does not prescribe that tools must call live services; tools are server-defined functions with JSON Schema inputs (`ext2`, `ext8`).
- Trade-offs:
  - Best fit for offline reads and deterministic exports.
  - Requires local schema/index/sync policy not externally evidenced.
  - Supports consistent CLI/MCP answers.
- Applicability: strongest fit for the requested read-first product slice.

### Alternative C: Plaintext-compatible filesystem layer only

- Description: preserve markdown repo semantics and wrap search/snippet commands.
- External support: MCP reference ecosystem includes filesystem/git-style local servers, showing local data exposure through MCP is plausible (`ext9`).
- Trade-offs:
  - Simple and familiar.
  - Does not solve identity maps, remote aliases, sync events, deterministic cache state, or future GitCode writes.
  - Weak foundation for RAG-ready provenance.
- Applicability: useful as behavioral exemplar, not as final substrate.

### Alternative D: RAG/vector-first query substrate

- Description: embed all records and make semantic retrieval the primary query path.
- External support: none in retained evidence.
- Trade-offs:
  - May improve discovery later.
  - Poor first-slice fit because exact ID lookup, backlinks, snippets, and freshness must remain authoritative.
  - Adds provider/vector dependencies before cache correctness.
- Applicability: defer; semantic search should consume authoritative cache/chunk records later.

## Primary Decision Hierarchy

1. Protocol correctness before feature richness:
   - MCP integration must follow JSON-RPC lifecycle and tool discovery/invocation contracts (`ext2`, `ext7`, `ext8`).
2. Offline cache reads before live GitCode convenience:
   - Routine search/get/backlink/resolve/status tools should be cache-backed; live behavior belongs behind explicit sync/write boundaries.
3. Stable identity before remote aliases:
   - Remote GitCode ids should be aliases over durable source ids; this is local policy, not externally evidenced.
4. Deterministic rebuildability before clever ranking:
   - Derived indexes and chunks should be rebuildable from raw cached records.
5. Explicit side effects before automation:
   - Sync/write operations should be explicit and consent-gated, aligning with MCP tool safety guidance (`ext6`).
6. Fixture contracts before public API assumptions:
   - GitCode adapter behavior should be discovered through sanitized fixtures because official API evidence is missing from the retained set.

## Low-Level Implementation Details

### MCP surface details

- Tool definitions should include:
  - `name`
  - `description`
  - `inputSchema`
- This follows the MCP tool definition structure (`ext2`).
- Server initialization should advertise a tools capability when the server offers tools (`ext1`).
- Tool calls should be handled through JSON-RPC request/response framing (`ext7`, `ext8`).
- First-slice read tools should return deterministic JSON-compatible results because MCP validates protocol structures with JSON Schema concepts (`ext7`, `ext8`).

### Cache/query service details

- External evidence does not define the cache schema.
- Locally required tables or records should likely include:
  - normalized records
  - identity map
  - aliases
  - full-text/search documents
  - links and backlinks
  - chunks and chunk provenance
  - sync events
  - conflicts
  - export snapshots
- These are inference from requirements, not external precedent.

### Adapter details

- External evidence does not establish GitCode API behavior.
- Later design should avoid leaking guessed GitCode JSON envelopes into query services.
- The adapter boundary should normalize live/fixture responses into internal records and typed errors.

### Write/sync details

- External MCP safety guidance supports explicit gating for side-effecting operations (`ext6`).
- Idempotency keys, write-ahead logs, conflict records, retries, backoff, and lock files are local policy choices without retained external support.

### RAG-ready details

- External evidence does not support a specific chunking model.
- Later design should treat chunk records as cache-derived provenance objects, not as vector-store-owned truth.
- Future semantic retrieval should return candidate chunk/source ids; authoritative citation should still use exact cache tools.

## Evidence Coverage

| Concern | Evidence strength | Covered by |
|---|---:|---|
| MCP JSON-RPC framing | High | `ext7`, `ext8` |
| MCP tool discovery/invocation | High | `ext2` |
| MCP tool schema shape | High | `ext2` |
| MCP server tools capability | High | `ext1` |
| Read/write separation via safety posture | Medium | `ext6` |
| Go MCP implementation feasibility | Low/medium | `ext10`, `ext11` |
| Reference server ecosystem | Medium | `ext9` |
| Streaming/progress first-slice deferral | Low/medium | `ext12` |
| GitCode `/api/v5` behavior | None | — |
| SQLite/FTS/cache schema | None | — |
| Deterministic export/diff | None | — |
| RAG chunk provenance | None | — |
| Agent plaintext workflow replacement | None external | — |

## Evidence Gaps And Negative Findings

- No retained GitCode API, auth scheme, pagination parameters, wiki model, issue schema, attachment behavior, rate-limit headers, or error envelopes.
- No retained SQLite/FTS, WAL mode, migration strategy, or lock behavior.
- No retained Go CLI architecture
- No retained deterministic export
- No retained RAG/chunking, offsets, heading paths, and future embedding fields must be validated locally.
- No retained source for the current plaintext knowledge layer:
  - Shell-equivalent workflows should be treated as user-provided behavioral requirements.
- MCP streaming/progress should not be treated as stable first-slice requirement:
  - Available evidence is issue-discussion level, not core protocol authority (`ext12`).

## Findings By Component Or Concern

### MCP server

- Direct evidence:
  - MCP tools are discovered through `tools/list` and invoked through `tools/call` (`ext2`).
  - Tool definitions use `name`, optional `description`, and `inputSchema` (`ext2`).
  - MCP messages follow JSON-RPC 2.0 (`ext7`, `ext8`).
  - Server capabilities include a tools capability (`ext1`).
- Inference:
  - Read-first tools such as search, get, backlinks, resolve, export, and diff can be implemented as MCP tools over a local cache.
- Applicability limit:
  - MCP does not specify GitCode semantics or local cache freshness.

### CLI surface

- Direct evidence:
  - None specific to Go CLI architecture in retained sources.
- Inference:
  - CLI should call the same service layer as MCP to avoid divergent search/get/backlink behavior.
- Applicability limit:
  - Flag names, compact output, JSON output, and command grouping are local design contracts.

### Cache store

- Direct evidence:
  - MCP result structures must be JSON-compatible because protocol messages and schemas are JSON-based (`ext7`, `ext8`).
- Inference:
  - Cache records should have stable, serializable DTOs decoupled from SQL rows.
- Negative finding:
  - No source validates a specific SQLite schema.

### GitCode adapter

- Direct evidence:
  - None retained for GitCode.
- Inference:
  - Adapter isolation is essential because API behavior is unknown.
- Negative finding:
  - Public API details remain the largest research risk.

### Sync and writes

- Direct evidence:
  - MCP tool safety guidance supports explicit user consent for action-taking tools (`ext6`).
- Inference:
  - Writes should be explicit, idempotent, logged, and absent from routine read paths.
- Negative finding:
  - Idempotency keys, conflict records, retries, and lock contention are not externally evidenced.

### Derived indexing

- Direct evidence:
  - MCP can expose query tools with structured arguments (`ext2`).
- Inference:
  - Derived search/backlink/stale reports should be rebuildable from raw cached records and exposed through both CLI and MCP.
- Negative finding:
  - No retained source covers incremental indexing or stale-index computation.

### RAG-ready corpus

- Direct evidence:
  - MCP separates server-provided context/capabilities from model behavior (`ext8`, `ext11`).
- Inference:
  - RAG should be a later consumer of cache chunks, not the authoritative citation/freshness layer.
- Negative finding:
  - No retained source supports a specific chunk schema.

### Fixtures and testing

- Direct evidence:
  - MCP `tools/list`/`tools/call` and JSON-RPC framing provide concrete integration-test hooks (`ext2`, `ext7`, `ext8`).
- Inference:
  - GitCode behavior should be contract-tested with sanitized HTTP fixtures.
- Negative finding:
  - Fixture sanitization rules are local/public-safety policy, not external precedent.

## Comparative External Systems

| System/source family | Useful mechanism | What transfers | What does not transfer |
|---|---|---|---|
| Official MCP schema/docs | JSON-RPC messages, capabilities, tools, input schemas (`ext1`, `ext2`, `ext7`, `ext8`) | MCP boundary, tool contracts, integration-test shape | Cache schema, GitCode adapter, CLI commands |
| MCP reference servers | Servers can expose local/system data through MCP (`ext9`) | Local cache/search server is plausible | Specific GitCode tracker/wiki behavior |
| Third-party Go MCP modules | Go implementation of MCP-style contracts is feasible (`ext10`) | Possible implementation direction | Authority is lower than official schema |
| MCP client/server SDK-style docs | Resources as GET-like data and tools as executable functions (`ext11`) | Helps reason about resources vs tools | Not a source of product-specific behavior |
| MCP streaming issue discussion | Progress/streaming exists as a debated concern (`ext12`) | Can justify deferring streaming from first slice | Not stable enough for core requirements |

## Engineering Implications For Later Design

- Keep MCP protocol types near the server boundary; do not let MCP request structs become cache/storage structs.
- Keep CLI and MCP thin over shared query services so output differences are presentation-only.
- Keep GitCode adapter response models isolated and fixture-tested because official API evidence is missing.
- Treat the local cache as the routine read source of truth; live sync should update the cache explicitly.
- Build deterministic projections and exports because external sources do not provide a replacement for local reviewability.
- Use typed errors across adapter/sync/cache layers so CLI and MCP can map failures predictably.
- Hide optional write tools unless explicitly enabled; this aligns with MCP tool safety posture (`ext6`).
- Defer streaming/progress until the base read tools are stable; available streaming evidence is discussion-level (`ext12`).
- Store chunk provenance now if RAG is desired later, but do not make embeddings authoritative for citations.

## Follow-Up Questions For Later Design

- Which exact GitCode `/api/v5` endpoints and response envelopes are available for issues, wiki pages, comments, attachments, and search?
- What auth modes and rate-limit headers does GitCode actually expose?
- Are wiki pages versioned, and can remote revisions be fetched reliably?
- Should MCP expose raw documents as resources in addition to query tools?
- What compact output format best preserves grep compatibility while remaining stable for agents?
- What SQLite FTS mode and tokenizer are acceptable for mixed markdown/tracker/wiki text?
- What line-ending normalization is required for deterministic byte offsets and chunk IDs?
- How should remote-deleted records be represented without losing local provenance?
- Should sync freshness be per record, per source family, or per adapter cursor?
- What fixture sanitizer patterns are sufficient for public-safe GitCode captures?

## Source Quality Notes

- Strongest sources:
  - `ext1`, `ext2`, `ext7`, `ext8` are official MCP schema/spec/docs and should anchor protocol claims.
- Medium sources:
  - `ext6` is official MCP-era documentation and useful for safety posture, but safety guidance is broad.
  - `ext9` is useful ecosystem context, not normative specification.
- Lower-authority sources:
  - `ext10` and `ext11` are implementation-context sources and should not override official MCP documents.
- Discussion-level

## Runner Evidence
- Final message: `runa/calls/call-0049-run_external_research/attempt-1/final_message.txt`
