# Architecture

## Goal

Provide a cache-first tooling layer that lets AI agents and humans search, inspect, link, export, and eventually synchronize GitCode tracker/wiki data even when live GitCode access is slow, flaky, or unavailable.

## Non-Goals

- Do not require live GitCode network access for routine reads.
- Do not make remote issue ids replace stable source ids such as `DOC-123`.
- Do not hide writes behind automatic background sync.

## Components

| Component | Purpose |
| --- | --- |
| Source ingest | Read markdown, tracker, or wiki exports and extract source/task/page metadata. |
| Local cache | Store normalized records, full text, backlinks, identity map, remote metadata, sync status, and conflicts. |
| Link resolver | Resolve legacy ids, local paths, wiki pages, and remote issue/page ids. |
| GitCode adapter | Encapsulate tracker/wiki API calls, pagination, auth, rate limits, attachments, and write semantics. |
| CLI | Provide explicit commands for sync, search, get, link-check, export, diff, and diagnostics. |
| MCP server | Expose read-first cache operations to agent sessions after the cache contract stabilizes. |
| Export snapshots | Produce deterministic markdown/JSON/SQLite snapshots for review, rollback, and audit. |

## Data Flow

```text
source markdown / tracker export / wiki export
        |
        v
source ingest -> local cache -> CLI / MCP reads
        |              |
        |              v
        |        export snapshots
        v
GitCode adapter <-> tracker/wiki remote state
```

Writes should flow through explicit sync commands and produce sync logs. Routine reads should flow through the local cache.
