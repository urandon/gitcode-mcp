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
`correlation_ref`, `stage_ref`, `source_ref`, `previous_registration_ref`, and
`project_ref`. Local-project events use true project scope, never cache or
repository aliases; incompatible mixed scopes are rejected before reading.
Use only opaque refs already returned on listed local entities, incidents, or
events. These filters never resolve a private coordinate, filesystem path,
repository id, or caller-provided label. Unknown local authority omits
`observation_refs`; it does not invent a join key from a display name or path.
An incident's source ref selects that actual repository-document source, not
all sources under the parent repository registration. Listed documentation
sources publish their known cache, repository and registration refs alongside
the source ref, matching the same authority carried by retained source events.
The browser requires every supplied parent ref to match; an incomplete or
ambiguous bridge is unavailable, not permission to relax the join.

The initial `local_authority_committed` catalog observes newly proof-confirmed
tracker and Git-document commits with `authority_commit`/`document_commit`
phases and `committed` outcome. Private per-call capture records the actual
canonical transaction attempt/proof, not inference from the caller's return
code. The trusted coordinator emits only after the execution turn unwinds.
A proved commit with lost acknowledgement remains historical confirmation;
uncertain publication contributes count-only incomplete coverage, not success.
Precommit rollback, reads and settled original-key replay create no throughput.
Resuming pending document intent can first confirm its canonical commit, and
observes that actual effect once. Each event has an opaque admitted-project ref and a
fresh collector-owned correlation, with no source/entity UUID, key, actor,
payload or private coordinate. Collector contention/loss never changes the
canonical receipt. This historical evidence does not infer current project,
job or index health; existing remote incident projection rejects project scope.
Native delivery uses the optional authenticated bounded relay described in
[local authority contracts](local-authority-contracts.md#safe-project-commit-observations-l4-in-progress).
The strict child opens no observation files: only the existing parent collector
retains events. Missing frames/seal, queue overflow and channel failure make
coverage partial without changing canonical work. Actual product restart tests
verify retained parent-ledger reload and quiet original-key replay. Runtime
phases and exact current-owner health remain #183 work. Public local workflow/navigation remains gated in
#184/#185; the Service view cannot fall through to a remote entity when a
project bridge is unavailable. Stdio/HTTP discovery can accept the opaque
`project_ref` selector; no read starts or enrolls a local runtime.

`local_authority_phase_changed` records confirmed `index_job` and
`knowledge_recovery` states (`queued`, `running`, `cancelling`, `cancelled`,
`succeeded`, `failed`, `interrupted`), and `projection_repair` states
(`prepared`, `quarantined`, `confirmed`). These are project-scoped historical
proof boundaries, not a remote job bridge or current-state health. Observation
admission order is not durable job-generation order. Validated ledger transitions
are captured at the real rename/proof boundary, before a later acknowledgement
can fail; empty namespace provisioning and replay produce no transition.
Each owned call/worker turn buffers at most eight finite phase records, with a
saturating 255-record overflow lower bound. Unproved publication/overflow counts
incomplete coverage only. Buffers drain after source/ledger locks release but
before joined owner lifetime registration ends; canonical commit proof shares
that release boundary. Long index jobs flush per turn, not into a lifetime-sized
buffer. Loss never retries a job, changes its receipt or delays its cancellation.

`since` and `until` must be supplied together as ordered RFC3339Nano UTC (`Z`)
timestamps spanning at most seven days. Omit both to read retained evidence only.
The default result limit is 50; explicit limits must be 1 through 200.

The opaque cursor binds the whole normalized filter, durable ledger generation,
and loss epoch. Keep every selector identical on later pages. Sparse pages still advance
the bounded scan; an empty page with `has_more` is not the end of retained data.
Do not combine a cursor with a changed source or time range.

`invalid_cursor` reports an invalid encoding or changed filter. A
`snapshot_required` response means the retained snapshot is no longer usable:
restart without the cursor and show the discontinuity. Do not silently claim
continuous coverage after an unclean restart, rotation, loss, or eviction. A
clean same-ledger restart does not itself invalidate a cursor when its generation,
loss epoch and retained position still match.

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

The Service Admin cohort uses these three endpoints without awaiting the broad
cache/credential snapshot. Its summary adds browser-only version and reviewed
catalog metadata; strict IPC/CLI/MCP DTOs stay unchanged. Browser state retains
one event page, explicit sparse continuation, independently fresh nullable
metrics, exact-only entity joins and bounded safe copy. See
[Service observation](admin-ui.md#service-observation) for navigation and recovery.
Transport or UI availability alone does not qualify whole-feature rollout;
final cross-surface qualification remains a separate gate.

## Diagnose a failed or waiting job

1. Open **Jobs**, select the retained job, and follow its exact observation
   link to **Service**. Read execution health first; logging availability is a
   separate question. A missing join is unavailable, not permission to choose
   a similarly named job or source.
2. Inspect the collection incident, its last confirmed success, retry deadline
   and phase. `waiting_writer` means fetched work is staged but not published;
   it is not a provider timeout. A scheduled retry is not an attempted retry.
3. Filter the timeline by the returned opaque job/source references. Compare
   actual provider outcomes, stage transitions and confirmed commits. Missing
   events with partial coverage do not override durable job/frontier truth.
4. **Copy safe context** copies only bounded typed evidence. Use it for agent
   handoff or feedback; never paste a cache path, raw log or provider response.
5. `observation_busy` calls for **Refresh observation**, or repeating the same
   bounded CLI/MCP read. It does not authorize retrying execution. A changed
   boot, retention boundary or loss epoch requires **Restart retained timeline**;
   keep the discontinuity visible rather than claiming continuous history.

A later confirmed success can recover a retained incident. Reloading the
daemon's durable job state must not erase a prior failure, invent provider
attempts, or promote staged work to success. No observation command performs
that recovery: it merely reports the execution owner's current evidence.

## Integrated qualification boundary

The O5 suite is in `internal/qualification/observation_test.go`. It uses real
local HTTP adapter requests, temporary SQLite stores, durable job owners,
writer admission, a durable owner and same-ledger reload, private IPC, CLI JSON, MCP stdio and
authenticated Admin HTTP. It compares the same Reader DTOs, verifies prior
success and writer-wait recovery, and checks that observation does not increase
provider traffic. Disabled, closed and storage-unavailable collectors are
negative controls. Unsampled resource gauges and history remain missing rather
than becoming fabricated zeroes; confirmed execution counters may already exist
before the first resource sample. A closed collector is not an admission-drop
test: post-seal emissions are deliberately ignored.

The real Admin revision poller shares nonblocking read locks. Qualification
retries only typed `observation_busy` reads, at most five attempts separated by
10 ms; unknown errors, cancellation and budget exhaustion still fail. This
test-only handling never retries sync or relaxes DTO/privacy/provider-count
assertions. Transport controls reject raw-text and mixed success/error envelopes.

The Admin read helper likewise retries only a structured HTTP 503
`observation_busy`, at most five GET attempts with 10 ms spacing inside one
four-second deadline. It keeps the exact URL/selectors/cursor and joins backoff
timers on cancellation or navigation. Exhausted busy reads remain visible;
auth, unknown errors and cursor discontinuities are never automatically retried.
This lets a selected incident's safe-copy recover from transient local read
contention after native SSE without a new execution action or manual Refresh.

`web/tests/observation-integrated.spec.ts` compiles that test-only fixture and
drives the embedded Admin bundle against its real authenticated API. It does
not replace HTTP responses or EventSource with mocks. It checks the selected
incident's reason/last-success and the correlated terminal event's typed safe
copy, including its byte ceiling. The fixture's synthetic
launch authority and stdin continuation protocol are confined to this test;
there is no additional production endpoint. The ordinary mocked semantic
suite remains useful for rare cursor/error/selection states.

Run the joined gates explicitly:

```sh
go test ./internal/qualification -run TestO5 -count=3
go test -race ./internal/qualification -run TestO5
cd web
npm run test:e2e -- tests/observation-integrated.spec.ts
```

These tests are not a whole-feature completion receipt. The issue tracker must
retain the following distinctions until exact-SHA qualification is complete:

| Original #101 criterion | Evidence boundary / remaining qualification |
|---|---|
| Admin diagnosis without shell/file inspection | Real embedded browser reads/copy, active retry/native SSE, and actual listed-entity/two-Git-source navigation; native upgrade remains separate. |
| Bounded managed diagnostic data | Collector/output-writer tests and sanitized DTO contracts; native legacy-upgrade qualification is distinct from simulated platform responses. |
| Units, reset and history semantics | Fixed metric catalog/history tests; actual-owner transport suite must not infer samples from counters or replay. |
| Stable cross-surface correlation | Same actual-owner Reader across IPC/CLI/MCP/Admin; actual ordinary Admin source bridges and embedded entity navigation are separately qualified. |
| Retention, cursors and restart | Actual stopped-worker retry recovery, admission-drop reload/cursor parity and joined whole-footprint rotation diagnosis; native legacy-upgrade qualification remains separate. |
| Observation is read-only | Isolated configuration, nil cache service in MCP, fixed Admin GETs and provider-call accounting; no install, sync or hidden repair. |
| Go/frontend/browser contracts | Full repository, race, asset, type/unit and semantic gates plus independent exact-SHA review and head/main CI are required; individual focused runs are insufficient. |
| Ownership and operations documentation | This playbook, architecture and contracts; final pass/skipped/blocked disposition belongs in issue comments, not an inferred release claim. |

Launchd/systemd fixture tests establish the platform command/state contract;
they do not by themselves certify an upgrade on an installed host. The explicit
native qualification below exercises actual platform owners in isolated fixture
namespaces; it does not upgrade the operator's canonical installed daemon.
Actual offline HTTP 401/403 controls preserve durable `live_auth_failure` as
the finite health reason `provider_forbidden` and terminal event class
`forbidden`. These names cover authentication and permission failures; they
do not distinguish an expired credential from insufficient access. Unknown
classes remain `operation_failed`. The actual-owner transport suite verifies
prior success, durable reload and confirmed recovery without erasing the cause;
the real embedded browser verifies selected incident and terminal safe-copy.
Actual timeout and truncated JSON controls also exhaust the default
four-attempt durable retry budget with real backoff and bounded request
deadlines. They retain `network_timeout`/`partial_response` as
`provider_timeout`/`provider_invalid` health reasons and `timeout`/`invalid`
terminal classes. Provider-call accounting includes all four failed attempts
and verifies that observation adds no request. These terminal controls do not
qualify an interrupted active retry.
The separate `TestO5ActualActiveRetryAndSSERecovery` and
`web/tests/observation-active.spec.ts` exercise actual HTTP 429/Retry-After,
the ordinary delayed scheduler and the same job's confirmed second attempt.
CLI, normal/minimal MCP and authenticated Admin identify the same retrying
incident and recovered job. The browser retains its selected incident and
updates from native SSE without navigation or manual refresh after the active
snapshot. Only actual admitted retries increment the counter; observation adds
no provider request. Qualification awaits the actual retained succeeded terminal
event, then requires a native invalidation matching the current revision with
that terminal sequence and recovered authority. This permits legitimate poller
coalescing without accepting an intermediate attempt-start invalidation or
requiring publication of every transient fingerprint.
`TestO5ActualStoppedWorkerRetryRecovery` terminates an owned test subprocess
only after a real delayed retry and a successful-empty sibling have reached
the ordinary durable job/checkpoint boundary. A fresh owner uses
`LoadAndMarkInterrupted` and `RecoverSyncStages`, preserves the same job,
retry timestamps and prior job's confirmed success, waits until the original
provider deadline, and admits only the pending collection's second attempt.
CLI, normal/minimal MCP and authenticated Admin compare interrupted, retrying
and recovered authority without provider requests. A new metrics boot does not
invent an attached origin from saved state; a real unclean ledger restart
invalidates the old cursor explicitly and retains causal recovery events.
Origin attachment and metric recording remain best effort: the crash suite
labels observed counts separately from explicit origin/recording loss. Missing
or undercounted metrics require reported loss; a later admission deficit needs
sufficient **new** aggregate loss since the pre-admission snapshot, not old
loss or per-counter attribution. Duplicate or fabricated counts still fail.
Separate deterministic scheduler and held-metrics-lock controls
verify strict `0` to `1` admission counting and null `origin_unavailable` with
exact loss respectively. Neither incomplete metrics nor observation failure
replace the mandatory job, checkpoint, provider-call and causal event evidence.
The successful sibling is not fetched again; terminal recovery replay is inert.
This is an isolated worker-crash qualification, not an installed daemon upgrade
or a browser reconnect qualification.

`TestO5ActualAdmissionDropPreservesExecutionTruth` holds a concrete ledger
reader's admitted lock while an actual HTTP sync worker completes and publishes
its SQLite data. Summary/events return typed `observation_busy` during the hold;
validated event rejection increments the concrete atomic `event_drops` origin,
while rejected untrusted input adds nothing. After release, dropped coverage and
the loss epoch conserve that count and invalidate the pre-loss cursor. The
confirmed job/cache result remains healthy and identical across CLI, normal and
minimal MCP, private IPC and authenticated Admin, without another provider call.
Clean ledger reload retains historical loss: the pre-loss cursor remains
invalid, while an already-known post-loss partial cursor can continue when
generation and retention still match. The new boot's admission-drop metric
starts at zero rather than replaying old loss. This proves real admission loss,
not ignored post-close emissions, and does not certify native upgrade, browser
navigation or complete disk bounds.

`TestO5ActualJobDiagnosisSurvivesWholeFootprintRotation` first confirms an actual
HTTP sync success and then a provider-forbidden failure. Separately identified
synthetic catalog pressure rotates all four ledger segments and both segments
of each managed output stream; it is not job throughput or fabricated recovery
evidence. The complete private inventory must have fixed names, regular `0600`
files, 1 MiB segment caps and at most 4 MiB ledger + 4 MiB streams + 8 KiB
metadata. Source text, provider bodies and private coordinates must be absent.
The evicted actual job's filtered timeline is exhaustively scanned with bounded
sparse continuation and must be empty. Its old cursor reports typed retention
discontinuity (or generation/loss change when concurrent admission loss occurs);
any such loss is conserved exactly, not silently ignored.
Durable job authority, the incident's cause and prior success survive rotation
and clean reload across CLI, normal/minimal MCP, IPC and authenticated Admin
without provider traffic. Reload may canonicalize failed progress prose to its
fixed safe message; every other persisted job/progress field remains exact.
A new confirmed sync recovers that same incident, updates last-success, and has
mandatory correlated admission/provider/commit/terminal events in the new boot.
These checks qualify isolated managed-file bounds and retained diagnosis, not a
native installed-service upgrade or browser entity/source navigation.

`internal/servicectl/observation_navigation_test.go` joins a real failed HTTP
sync, temporary SQLite binding, normal maintenance enrollment and two local Git
source registrations to the ordinary `Manager.adminObservation` snapshot. Real
Git inspection failures retain separate source-specific transitions. The
embedded browser cohort in `web/tests/observation-navigation.spec.ts` verifies
cache/repository/job/registration observation links, exact job/registration
back-links and both source-specific event back-links, with no API or EventSource
mocks. Each source DTO carries the full known parent-ref bridge; the strict UI
join is unchanged.

After selecting a source, an explicit test-only durable registry removal makes
that source unavailable through native snapshot SSE. The UI keeps the missing
selection visible and refuses a plan or sibling fallback. This removal is not
a new product endpoint or action. The fixture asserts one actual provider
request, joined job/collector/Admin/protocol owners, durable registry reload,
path-free snapshots and bounded safe-copy. Browser API requests after session
establishment are GET-only; no index, setup or execution action is invoked.
This qualifies local navigation, not native installed-service upgrade.

### Native owner upgrade qualification

`TestNativeObservationUpgradeRecovery` uses a daemon compiled from the exact
checkout before the test, creates a
private offline cache/configuration, and bootstraps a unique launchd label or
runtime-linked user-systemd unit. A test-only closed command adapter translates
only the expected production target to that fixture target. Unknown commands
fail closed. The child's environment is reset with `env -i`; HOME, config,
cache, runtime, working directory and credential policy are fixture-owned.
There is no provider setup, download, live sync, or canonical service mutation.

The test compares the platform's actual PID with the daemon's IPC status and
checks that its loaded stdout/stderr routing is null after replacement. It
requires legacy readiness, a read-only plan, stale-plan refusal before platform
effects, real quiescence and fixed-stream cleanup, an injected interruption
before restart, then fresh-plan recovery to a new boot and managed readiness.
The original cache UUID, completed local fixture job, and unrelated local file
must survive. Both fixed legacy files must stay absent after restart. Teardown
unloads the exact fixture and joins its process/socket before deleting private
files; failure to prove quiescence retains those files and fails qualification.
Platform output stays bounded in memory and is never printed.
Only a typed `observation_busy` from the fixture's `Observability.Summary` read
is retried, at most five attempts with 10 ms spacing inside the same three-second
call deadline. Execution RPCs, platform writes, cancellation, timeouts and unknown
failures are never retried. Default deterministic controls hold the actual RPC
collector read lock, then prove bounded read recovery and joined ownership.
Generic fixture job/status exchanges use the existing socket-deadline transport
with a 1 MiB response cap; cancellation closes the established stream. Silent-peer
deadline/cancel tests prove return, one request and joined socket ownership before
native teardown. This fixture boundary does not change generic production IPC.

Default tests skip this native operation. On a suitable macOS GUI session,
explicitly run:

```sh
fixture_dir=$(mktemp -d)
go build -o "$fixture_dir/daemon" ./cmd/gitcode-mcp
GITCODE_MCP_NATIVE_FIXTURE_BINARY="$fixture_dir/daemon" GITCODE_MCP_NATIVE_OBSERVATION_UPGRADE=1 go test ./internal/servicectl -run '^TestNativeObservationUpgradeRecovery$' -count=1 -timeout=180s
```

An explicit run fails rather than skips if the native bus is unavailable. The
Linux CI gate runs on the ephemeral Ubuntu VM, establishes its user bus if
necessary, and requires this exact fixture test to pass. macOS qualification is
a separate exact-SHA receipt on a suitable host. This is native lifecycle proof,
not a claim that every operator installation was migrated; production upgrades
still require the normal explicit plan/confirmation flow.

Linux additionally requires `GITCODE_MCP_NATIVE_SYSTEMD_ISOLATED=1`, an explicit
attestation that the user manager belongs to a disposable test environment.
Although fixture units are unique, `daemon-reload` is manager-wide; do not run
this qualification against an ordinary operator user manager. CI sets this
flag only inside its ephemeral VM. Native executable paths are pinned rather
than resolved through inherited PATH. An attempted runtime link is owned even
if its command times out, and teardown confirms the exact unit is not found
before removing fixture files. Positive native absence is checked before the
fixture claims its unique owner. Platform commands have finite context and
pipe-drain bounds. Building beforehand keeps compiler descendants outside the
native fixture lifecycle; the caller owns the separate build directory.

Current documentation source authority is also fenced by the UUID read from
the physical cache, not just its path and repository binding. Replacing a cache
file at the same path cannot attach historical registrations or source events
to the replacement container. Those registrations remain in maintenance
history; restoring the original cache makes its own source bridge available
again without enrollment or observation-triggered repair. A deterministic
actual SQLite close/replace/restore fixture covers this boundary, including
unknown identity and a historical same-path entry preceding the current one.

Collection authority retains bounded `first_retry_at`/`last_retry_at` timestamps
when scheduling is durably observed. Later success replaces current failure and
deadline but preserves this same-job recovery evidence across owner reload.
These fields are not reconstructed from logs, progress text or metrics; legacy
records without them acquire no invented history. Incomplete, inverted, future
or out-of-job-lifetime retry timestamps make health authority unavailable rather
than fabricate incidents. Retention/pruning still bounds the authority lifetime.
Timeout classification accepts both a context deadline and a typed timeout
inside the provider's network error, including on the Go 1.22 baseline. This
does not broaden status-based retry eligibility or interpret raw error text.
No release or installed-service upgrade is implied by running the O5 suite.
