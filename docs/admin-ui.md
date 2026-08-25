# Embedded admin UI

The admin UI is a Svelte 5/SvelteKit static SPA compiled into `internal/adminui/assets` and embedded in the Go binary. A source install (`go install ./...`) does not require Node.js because the reviewed production assets are checked in. Frontend contributors use `scripts/build-admin-ui.sh`; CI uses `scripts/check-admin-ui-assets.sh` to reject stale generated assets.

## Runtime ownership

The existing `gitcode-mcp service run` process owns both the local JSON-RPC control socket and the optional admin HTTP listener. `gitcode-mcp admin open` connects to that daemon; it never starts a second coordinator or opens the cache independently.

```sh
gitcode-mcp service run
gitcode-mcp admin open
gitcode-mcp admin status --format json
```

`admin open` starts the listener lazily on `127.0.0.1` with a dynamic port. For foreground debugging, `service run --admin` starts it eagerly. `--admin-bind 127.0.0.1:9000` selects a stable loopback address. Non-loopback binds are rejected unless the operator also supplies the conspicuous `--admin-unsafe-allow-non-loopback` override; this override is intended only for controlled development environments and does not add TLS or remote-user authentication.

The listener is disabled at daemon startup by default. Its optional runtime configuration is deliberately explicit:

| `service run` option | Default | Effect |
| --- | --- | --- |
| `--admin` | off | Start eagerly instead of waiting for `admin open`. |
| `--admin-bind` | `127.0.0.1:0` | Select loopback host and port; port `0` asks the OS for a free port. |
| `--admin-unsafe-allow-non-loopback` | off | Permit a non-loopback bind without adding remote security. |

To keep the listener disabled, omit `--admin` and do not invoke `admin open`. A lazily or eagerly started listener stops with its owning daemon and is not persisted as a second service; restart the daemon without `--admin` to return to the disabled state.

Use `admin open --no-browser` when a browser must be opened manually. The printed URL contains a one-time token in its fragment. The SPA removes the fragment from browser history before exchanging it for a bounded SameSite Strict, HttpOnly session cookie. The launch token expires after one minute and cannot be replayed.

## Browser boundary

The admin listener applies these controls:

- loopback bind and Host validation by default;
- Origin and Fetch Metadata validation for mutations;
- a per-session CSRF token for unsafe requests;
- a generated hash-based Content Security Policy for the SvelteKit bootstrap, self-only resources, no CORS, no framing, and no referrer propagation;
- immutable caching for hashed SvelteKit assets, explicit no-store revalidation for `index.html`, and SPA fallback routing;
- readiness payloads that contain local operational state but never credentials, launch tokens, session cookies, or CSRF material.

## Observation and supervision API

The authenticated `/api/admin/v1` surface is an in-process transport over the coordinator. Observation requests do not invoke CLI or MCP subprocesses and do not contact GitCode or an embedding provider while rendering a page. Mutation endpoints are capability-gated, CSRF-bound, plan/apply controls; the browser never receives authority to install services, download models, change credentials, migrate or delete caches, unbind repositories, or choose an arbitrary filesystem path.

| Endpoint | Purpose |
| --- | --- |
| `GET /session` | Return the authenticated browser session's API version and CSRF value; never returns the session cookie material. |
| `GET /snapshot` | Coarse overview: service, attention, caches/repositories, jobs, maintenance, diagnostics, capabilities, and a content revision. |
| `GET /caches` and `/caches/{cache_ref}` | Managed cache topology and safe storage/readiness metadata. |
| `GET /caches/{cache_ref}/repositories/{repo_id}` | Binding, collection counts, five independent coverage lanes, active work, contention, retry, and last-stage errors. |
| `GET /jobs` and `/jobs/{job_id}` | Structured bounded job history and progress; list filters accept state, type, cache, repository, and failure class. |
| `POST /jobs/{job_id}/cancel` | Request graceful cancellation of an active daemon-owned sync or RAG job. |
| `POST /jobs/{job_id}/retry` | Request one safe current maintenance reconciliation for a terminal sync or RAG job; equivalent active work is coalesced. |
| `GET /maintenance` | Read-only registrations and policy summaries. |
| `POST /maintenance/plan` | Render the current maintenance policy effect ledger for one opaque cache/repository target. |
| `POST /maintenance/apply` | Re-plan and apply the exact confirmed maintenance plan with an idempotency key. |
| `POST /maintenance/{registration_id}/disable` | Disable one retained registration and return a durable receipt. |
| `POST /maintenance/{registration_id}/reconcile` | Reconcile current truth; equivalent active work is coalesced. |
| `POST /bindings/plan` | Validate and render an add/update/no-op binding plan without contacting GitCode. |
| `POST /bindings/apply` | Re-plan and atomically write the confirmed local binding with an idempotency key. |
| `GET /diagnostics` | Current/recovered typed failures and sanitized remediation. |
| `GET /capabilities` | Capability and safety catalog with explicit UI availability. |
| `GET /events?after=CURSOR` | Bounded SSE invalidation/progress replay. |

Event cursors are opaque and monotonic. A reconnect within the retained window replays missed compact events. An expired cursor produces `snapshot_required`, after which the browser reloads the coarse snapshot. The stream never carries raw logs or cached record bodies.

Every mutation requires the authenticated session cookie, a same-origin request, the session CSRF value returned by `GET /session`, a public target id, and a fresh idempotency key. The daemon persists only hashes of idempotency material and bounded sets of 256 public receipts. Replaying a retained key returns the original receipt after restart; reusing it for a different target, action, or plan is rejected. A cancel receipt distinguishes a completed cancellation from a request still converging. Retry and reconcile do not force an identical historical execution: they reconcile current truth, then report whether work was created, coalesced, or no longer needed.

Maintenance apply always renders the plan again and rejects changes to cache identity, repository binding, effective non-secret configuration, provider/model revision, daemon protocol/state, or requested policy as `stale_plan`. Its effect ledger labels inspection, local configuration writes, job enqueueing, provider data transfer, local service changes, and downloads separately. Machine-level effects remain blocked with an exact CLI handoff.

Binding plan/apply accepts `cache_ref`, never `cache_path`. An omitted API URL uses the effective GitCode v5 default for a new binding and preserves an existing custom route on update. Owner/name derivation, normalized scopes, unique aliases, schema compatibility, and ambiguous cache identity are checked before the atomic SQLite transaction. Applying a stale plan, an alias collision, or a reused key with changed intent produces a typed conflict. Unbind remains deliberately unavailable.

Ordinary responses expose `cache_ref` and a one-way path fingerprint, never an absolute cache path. Current coverage truth is kept separate from active contention, a scheduled retry, and the last stage error, so a transient maintenance failure cannot hide a still-current RAG namespace.

## Read-only operator views

The observation UI is organized around product state rather than CLI command groups:

- **Overview** leads with current attention, service/cache readiness, active work, cache/repository cohort summaries, and recovered failures.
- **Caches** shows the safe cache → repository topology. Repository details keep head freshness, tail completeness, secondary coverage, projection generation, and RAG generation as five independent lanes.
- **Collections** shows bounded per-kind counts and head/tail frontier evidence. A bounded tail stop is always labelled partial; only end-of-collection evidence is presented as complete.
- **Search status** explains full-text versus hybrid readiness without running a query, provider call, sync, or repair.
- **Activity** presents bounded structured history. **Maintenance** adds capability-derived policy and binding workbenches: edit intent, render the effect ledger, confirm the exact plan id, and inspect receipts or CLI handoffs. **Jobs** adds URL-preserved filters and detail, retained progress, throughput/ETA where derivable, rate-limit/retry/interruption state, and explicit cancel/retry controls only where the daemon reports them safe.
- **Diagnostics** separates current from recovered typed failures, gives fixed public-safe CLI handoffs, and shows the capability/safety catalog.

Local deep links use URL search parameters: `view`, opaque `cache`, public repository id `repo`, repository `tab`, diagnostic state, public `job`, and job state/type/cache/repository/failure filters. Browser reload and back/forward navigation preserve this state. The UI explicitly renders loading, empty, partial/degraded, stale, recovered, interrupted, waiting, and API-version-mismatch states; status meaning is always present as text rather than color alone.

## Themes

The visible selector has exactly three choices: **Light**, **Dark**, and **System**. **System is the default** when no preference is saved or an invalid value is found. It follows `prefers-color-scheme`; explicit Light and Dark choices are stored only in browser-local storage. A small external head script applies the choice before the application starts, avoiding a first-paint theme flash while keeping the CSP free of inline script allowances.
