# Cache And Sync Model

## Local Cache Requirements

The cache should answer routine agent queries without network access:

- search tasks, source notes, and wiki-like pages;
- get a record by legacy id, path, remote id, or URL;
- resolve backlinks;
- explain sync status and conflicts;
- export deterministic snapshots;
- compare local/export/remote state when remote data is available.

## Candidate Storage

Start with SQLite plus optional deterministic markdown/JSON exports.

Candidate tables:

- `records`: normalized tasks, pages, sources, decisions, handoffs.
- `full_text`: search text and extracted headings.
- `identity_map`: legacy id, local path, remote id, remote URL, aliases.
- `links`: source record, target record, link kind, raw link text, resolved target.
- `remote_revisions`: remote revision metadata and fetch timestamps.
- `sync_events`: sync command, idempotency key, result, error class, evidence path.
- `conflicts`: local value, remote value, conflict class, resolution state.

## Sync Principles

- Reads are cache-first.
- Writes are explicit commands.
- Every remote write has an idempotency key or deterministic replay guard.
- Every sync creates reviewable evidence.
- Remote ids are aliases. Legacy ids remain stable migration keys.
- Failed writes stay visible until retried or deliberately dismissed.
