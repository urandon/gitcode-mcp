# Daemon observation

Daemon observation is a bounded, local, read-only view of retained execution
evidence. It is not cache readiness, a GitCode health probe, or a repair action.
The daemon owns one `internal/observability.Reader`; Admin, private IPC, CLI,
and both MCP transports expose its DTOs rather than reconstructing health.

## Surfaces

| Data | Admin session GET | Private IPC | CLI | MCP |
|---|---|---|---|---|
| Summary | `/api/admin/v1/observability` | `Observability.Summary` | `service observe` | `service_observability` |
| Metrics | `/api/admin/v1/observability/metrics` | `Observability.Metrics` | `service metrics` | `service_metrics` |
| Events | `/api/admin/v1/observability/events` | `Observability.Events` | `service events` | `service_events` |

`Observability.Capabilities` advertises `observability.v1`, these three methods,
and the exact read limits. CLI/MCP negotiate it before querying. An older daemon
returns an explicit update handoff; an absent daemon returns an explicit start
handoff. Neither read installs, starts, restarts, or updates the daemon.

```sh
gitcode-mcp service observe --format json
gitcode-mcp service metrics --name runtime_heap --history-minutes 3 --format json
gitcode-mcp service events --component jobs --limit 50 --format json
```

MCP arguments are flat JSON objects; summary accepts only `{}`. Metric arguments
are optional `name` and `history_minutes`. Event arguments use the selectors
below and optional `cursor`/`limit`. Discovery describes finite values and
read-only hints. Unknown keys, duplicate keys, nested values, nulls, wrong types,
and unsupported selector combinations fail before connecting to the daemon or
reading retained state. CLI also rejects repeated flags and positional values.

## Separate execution and observation health

Summary schema 1 includes:

- `collector`: boot identity, generation, latest retained sequence, and ledger
  coverage/output state;
- `execution_health`: bounded incidents projected from retained scheduler/job
  authority, independently of logging and metrics health;
- `observation_health`: ledger, managed output, and metric availability/loss;
- `resources`: already sampled fixed gauges, with null unavailable values and
  finite reasons; no query invokes a resource probe;
- `metrics_boot_id`, reset/sample timestamps, published limits, and a bounded
  `copy_context` projection.

A healthy execution state does not imply complete observations. Conversely, a
missing metric sample or unavailable logging sink does not imply failed work.
Unknown authority and partial/truncated coverage remain explicit. Event severity
is not outcome: an info-severity provider completion can represent a failure.
Inspect `phase`, `outcome`, and `failure_class` instead of inferring success from
severity or message text. Legacy evidence can lack these dimensions.

Metrics expose a fixed catalog, finite label sets, boot/reset/sample identity,
loss, and minute buckets. Values, deltas, and rates stay null until sufficient
actual observations exist. Unsupported origins remain unavailable, not zero.
`name` is an optional exact catalog name, not a regex or arbitrary label. The
default history is 60 minutes; explicit windows must be 1 through 60.

## Events and opaque navigation

Finite selectors are `stream`, `severity`, `component`, `code`, `phase`,
`collection`, `outcome`, and `failure_class`. Full combinations must be compatible
with an admitted event shape, not merely contain individually valid strings.

Identity selectors are `job_ref`, `registration_ref`, `cache_ref`, `repo_ref`,
`correlation_ref`, `stage_ref`, `source_ref`, and `previous_registration_ref`.
Use only opaque refs already returned on listed local entities, incidents, or
events. These filters never resolve a private coordinate, filesystem path,
repository id, or caller-provided label. Unknown local authority omits
`observation_refs`; it does not invent a join key from a display name or path.
An incident's source ref selects that actual repository-document source, not
all sources under the parent repository registration.

`since` and `until` must be supplied together as ordered RFC3339Nano UTC (`Z`)
timestamps spanning at most seven days. Omit both to read retained evidence only.
The default result limit is 50; explicit limits must be 1 through 200.

The opaque cursor binds the whole normalized filter, boot/generation, and loss
epoch. Keep every selector identical on later pages. Sparse pages still advance
the bounded scan; an empty page with `has_more` is not the end of retained data.
Do not combine a cursor with a changed source or time range.

`invalid_cursor` reports an invalid encoding or changed filter. A
`snapshot_required` response means the retained snapshot is no longer usable:
restart without the cursor and show the discontinuity. Do not silently claim
continuous coverage after boot, rotation, loss, or eviction.

## Limits, contention, and cancellation

| Boundary | Limit |
|---|---|
| Validated request arguments | 8 KiB |
| Observation budget, including IPC capability negotiation and query | 2 seconds |
| Summary DTO | 128 KiB |
| Event page | At most 200 returned events and 64 KiB |
| Event scan | 64 KiB of encoded retained events, including nonmatching events |
| Full metric DTO | 4 MiB |
| Safe-copy JSON | 16 KiB |
| Cursor | 512 bytes |
| History | 60 minutes |
| Explicit event time span | 7 days |

Private IPC retains JSON-stream framing, with a 1 MiB cap per decoded RPC value;
observability additionally caps its envelope and validates the smaller argument
budget. Replies have method-specific caps plus bounded envelope overhead.
Established sockets have deadlines and cancellation closes them. Daemon shutdown
joins accepted IPC handlers and the owned Admin invalidation poller. No timed-out
snapshot is left running in a detached worker.

Snapshots use nonblocking lock admission and return `observation_busy` rather
than waiting behind execution work. Read contention does not manufacture lost
execution counters. Timeout/cancellation/busy are read failures, not evidence
that a job failed. Retry only the bounded read if it is still useful.

Admin requires the existing local session, valid Host, and same-origin checks
including Fetch Metadata. The three routes accept GET with no request body and
return `Cache-Control: no-store`. Invalid query/cursor is HTTP 400, stale snapshot
409, busy/unavailable/cancelled 503, and read timeout 504.

MCP preserves typed codes and cursor reasons in `error.data`. Unknown/raw daemon
errors never become copied provider text; compatibility failures use fixed safe
handoffs. Observation tools accept no live-write intent or idempotency key.

## MCP bootstrap isolation

Stdio and HTTP/SSE initialization, `tools/list`, and the three observation tools
do not create cache parents, open/migrate SQLite, resolve credentials, configure
a provider, or start a daemon. They remain available with a missing, damaged,
old, or incompatible cache and during another request's bootstrap. Invalid
effective configuration is a configuration diagnostic, not permission to guess
an alternate cache or daemon endpoint.

Only an explicitly requested cache/provider-dependent tool lazily opens its
store. Bootstrap admission is serialized and nonblocking; the store lease lasts
through the dependent handler and shutdown waits for leases. SQLite bootstrap
uses its request context. Explicit live operations resolve their credential at
that request boundary and retain provider confirmation, audit, and idempotency.
They never fall back to fixture writes when credentials are absent or offline
mode is selected. Observation's two-second budget does not change the existing
OS credential-provider contract for an explicitly requested live tool.
Concurrent first-use credential diagnostics are nonblocking: a contending caller
receives `credential-resolution-busy`, not a fabricated cached absence or another
OS provider call. Cancellation also remains transient, not a cached missing token.

HTTP `/ready` reports transport/diagnostic readiness without a cache probe.
`transport_ready` means cache/provider readiness has not yet been evaluated;
`bootstrap_busy` means another requested dependent tool is initializing. A known
bootstrap diagnostic can coexist with `ready: true` because discovery and daemon
observation remain available. It is not proof that cached reads or live writes
will succeed. `doctor` and dependent tools provide their own explicit diagnostic.

## Compact invalidation and safe copy

The existing authenticated SSE stream carries `observability_changed`
invalidations, at most once per second. Its revision includes stable collector
boot/generation/sequence/coverage and metric boot/sample/loss identity. It excludes
current time, uptime, and the observation request's own handler counter.
Registered observation reads/polling emit no self-events; an unrelated URL with
an observability-looking prefix is still an ordinary observable Admin request.
The invalidation carries no full metric series, event payload, or private lookup
authority. Clients refetch bounded DTOs.

`BuildSafeContext`/`MarshalSafeContext` project schema-1 JSON from already returned
summary evidence and at most one selected event or incident. The baseline summary
contains `copy_context`; UI selection uses the same projection rules, not a fourth
endpoint. Only finite health/catalog fields, timestamps, opaque identities, and
loss counters are copied. Free-form reasons, raw errors, cache/config paths,
provider bodies, credentials, display coordinates, and broad incident lists are
excluded. A missing/evicted selection fails explicitly instead of resolving a
private id or looking up the filesystem.

The dedicated Observability Admin UI cohort is tracked separately from this
transport contract. Transport availability alone does not qualify its navigation,
filtering, copy, stale-cursor recovery, or rollout.
