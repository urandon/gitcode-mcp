# Design Package Component: source-ingest

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Source Ingest

## Summary
`source-ingest` is not an approved standalone component in the top-level architecture. The ingest behavior is distributed across `internal/service`, `internal/cache`, and `internal/index`, so no component-local task list can be produced for this configured component.

## Top-Level Alignment
The approved architecture defines ingestion as orchestration through `internal/service` with persistence in `internal/cache` and parsing/index work in `internal/index`. It does not define a package, runtime entity, interface, or durable component named `source-ingest`.

## Cross-Cutting Constraints
- Cache-first reads remain owned by `internal/service` and `internal/cache` — `source-ingest` has no independent read/write boundary in the approved architecture.
- Local markdown parsing and derived projection generation remain owned by `internal/index` — ingest-related parsing is not a standalone component responsibility.
- Component task design must be derived only from Component Impact deltas — Component Impact decision is `skip` with no deltas.

## Data And Control Flow
- `internal/service` receives explicit ingest or sync intent — it orchestrates cache writes and index triggering; no separate `source-ingest` controller exists.
- `internal/cache` persists normalized source records — it remains the sole SQLite writer through the Store interface.
- `internal/index` parses source content and computes derived records — it reads from cache and writes derived indexes back through cache.

## Component Interactions
- `internal/service` -> `internal/cache` — service-layer ingest/sync paths upsert normalized records through the Store interface.
- `internal/service` -> `internal/index` — service-layer flows trigger full or incremental index builds after source records exist.
- `internal/index` -> `internal/cache` — indexing reads source records and writes chunks, links, backlinks, and derived projections through cache-owned accessors.

## Rationale
The configured component is `source-ingest`, but the approved architecture and Component Impact JSON explicitly state that Source Ingest is not a standalone Go package or component. Creating detailed tasks would invent ownership not present in the architecture and would duplicate responsibilities already assigned to `internal/service`, `internal/cache`, and `internal/index`.

## Skip Rationale
Component Impact says `decision: skip` because `source-ingest` is not a standalone component in the architecture contract. Its responsibilities are distributed across `internal/service` (`SyncToCache` and orchestration), `internal/cache` (record insertion and storage), and `internal/index` (parsing, chunking, and derived projections), with no component-local deltas to implement.

## Runner Evidence
- Final message: `runa/calls/call-0107-run_attempt-1/final_message.txt`
