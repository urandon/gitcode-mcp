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
| `GET /snapshot` | Coarse overview: service, attention, caches/repositories, jobs, maintenance, feedback readiness, diagnostics, capabilities, and a content revision. |
| `GET /caches` and `/caches/{cache_ref}` | Managed cache topology and safe storage/readiness metadata. |
| `GET /caches/{cache_ref}/repositories/{repo_id}` | Binding, collection counts, five independent coverage lanes, active work, contention, retry, and last-stage errors. |
| `GET /jobs` and `/jobs/{job_id}` | Structured bounded job history and progress; list filters accept state, type, cache, repository, and failure class. |
| `POST /jobs/{job_id}/cancel` | Request graceful cancellation of an active daemon-owned sync or RAG job. |
| `POST /jobs/{job_id}/retry` | Request one safe current maintenance reconciliation for a terminal sync or RAG job; an active retained backoff rejects the write, otherwise equivalent active work is coalesced. |
| `GET /maintenance` | Read-only canonical registrations, aliases, legacy redirects, conflict state, and policy summaries. |
| `POST /maintenance/plan` | Render the current maintenance policy effect ledger for one opaque cache/repository target. |
| `POST /maintenance/apply` | Re-plan and apply the exact confirmed maintenance plan with an idempotency key. |
| `POST /maintenance/{registration_id}/conflict-resolution/plan` | Revalidate one explicitly selected sanitized candidate against its private cache/config/source bundle and render the generation-fenced canonicalization effects. |
| `POST /maintenance/{registration_id}/conflict-resolution/apply` | Re-plan and atomically promote the confirmed candidate, redirects, history, clone fences, and durable domain receipt. |
| `POST /maintenance/{registration_id}/disable` | Disable one retained registration and return a durable receipt. |
| `POST /maintenance/{registration_id}/reconcile` | Reconcile current truth; equivalent active work is coalesced. |
| `POST /bindings/plan` | Validate and render an add/update/no-op binding plan without contacting GitCode. |
| `POST /bindings/apply` | Re-plan and atomically write the confirmed local binding with an idempotency key. |
| `POST /feedback/setup/plan` | Render a trusted global feedback-sink setup plan for a repository already bound in the effective cache. |
| `POST /feedback/setup/apply` | Re-plan and atomically apply the exact feedback setup intent with a durable idempotency receipt; never submits an issue. |
| `POST /search/compare` | Run the same bounded query as full-text and requested hybrid retrieval against one managed cache; never syncs or repairs. |
| `POST /rag/provider/smoke` | Probe configured embedding-provider/model metadata without sending cached source text. |
| `POST /rag/repair/plan` | Inspect current namespace and generation coverage and render an explicit bounded repair effect ledger. |
| `POST /rag/repair/apply` | Re-plan and enqueue at most the confirmed number of missing or stale chunks with an idempotency key. |
| `GET /diagnostics` | Current/recovered typed failures and sanitized remediation. |
| `GET /capabilities` | Capability and safety catalog with explicit UI availability. |
| `GET /events?after=CURSOR` | Bounded SSE invalidation/progress replay. |

Event cursors are opaque and monotonic. A reconnect within the retained window replays missed compact events. An expired cursor produces `snapshot_required`, after which the browser reloads the coarse snapshot. The stream never carries raw logs or cached record bodies.

Every mutation requires the authenticated session cookie, a same-origin request, the session CSRF value returned by `GET /session`, a public target id, and a fresh idempotency key. The daemon persists only hashes of idempotency material and bounded sets of 256 public receipts. Replaying a retained key returns the original receipt after restart; reusing it for a different target, action, or plan is rejected. A cancel receipt distinguishes a completed cancellation from a request still converging. Retry and reconcile do not force an identical historical execution: they reconcile current truth, then report whether work was created, coalesced, or no longer needed. A new retry intent is rejected before receipt reservation or maintenance work while its retained stage, collection, or legacy progress deadline is still in the future. An already prepared durable intent may still settle after restart without duplicating work.

Maintenance apply always renders the plan again and rejects changes to cache identity, canonical repository binding, effective non-secret configuration, provider/model revision, daemon protocol/state, or requested policy as `stale_plan`. Its effect ledger labels inspection, local configuration writes, job enqueueing, provider data transfer, local service changes, and downloads separately. Machine-level effects remain blocked with an exact CLI handoff. Alias-derived legacy registrations render as one canonical row with known aliases; old public registration references redirect to that row.

A conflicting migration remains disabled until the dedicated conflict-resolution plan is rendered and confirmed. The UI distinguishes harmless aliases, policy/source conflicts, temporarily unresolved identity, and UUID-global clone conflicts. It presents paired sanitized candidates and preselects none. Source-authority hashes and opaque source references are visible so a source-only choice is informed. Clone choices are physical cache-authority cards with the complete list of repository/policy/config/source members that will be retained; selecting a path cannot merge distinct repositories.

Planning reopens the selected private cache, verifies its UUID, current canonical repository bindings, config snapshot hashes, and exact candidate generation. Apply repeats that verification behind a cache-writer admission fence and remains blocked until active workers or synchronous Admin cache writers quiesce. It atomically persists the selected repository authority (or complete clone path cohort), canonical/legacy redirects, path-bound repository-document admissions and enrollment receipts, conservative retry state, unselected clone-path retirement, and the domain idempotency receipt in `managed-caches.json`. A persistence failure rolls all mutations back; replay after restart returns the retained receipt. Typed stale/candidate-change responses close the confirmation dialog, invalidate the old plan, refresh the snapshot, clear the stale candidate selection, and require a new explicit review. The browser never receives a cache/repository path, config reference/snapshot, or source path. Legacy conflicts that predate lossless candidate storage stay blocked with an explicit recovery message instead of fabricating a selectable winner.

Binding plan/apply accepts `cache_ref`, never `cache_path`. An omitted API URL uses the effective GitCode v5 default for a new binding and preserves an existing custom route on update. Owner/name derivation, normalized scopes, unique aliases, schema compatibility, and ambiguous cache identity are checked before the atomic SQLite transaction. Applying a stale plan, an alias collision, or a reused key with changed intent produces a typed conflict. Unbind remains deliberately unavailable.

Feedback readiness is a side-effect-free snapshot projection with six stable
states: `disabled`, `sink_missing`, `repository_unbound`,
`credential_missing`, `provider_unavailable`, and `ready`. The Maintenance
workbench keeps sanitized report preparation visibly available in every state
and enables issue submission only for `ready`. Setup targets come only from
repository identities already bound in the effective cache; the browser cannot
enter an arbitrary repository, credential, endpoint, or path. Plan/apply writes
only the trusted global feedback section and never creates an issue. A stale
plan is invalidated and refreshed. An ambiguous response keeps the exact plan
and idempotency key in the confirmation dialog so the durable receipt can be
replayed safely after the config has already advanced.

Search comparison is observation-only. It opens an already managed cache read-only and runs full-text and requested hybrid retrieval side by side. It reports requested/effective mode, typed fallback reason, RAG state, namespace and generation coverage, lexical/semantic/fusion scores, ranks, and bounded citations. A comparison never performs GitCode reads, sync, provider setup, model download, indexing, or repair. Hybrid retrieval sends only the query to the configured embedding provider; full-text retrieval is local.

Provider smoke asks only for model metadata and sends no cached source text. If the provider or model is unavailable, the UI returns a sanitized failure class and the fixed `gitcode-mcp rag setup --yes` CLI handoff. It never installs, starts, or downloads provider components.

RAG repair is a separate explicit plan/confirm/apply action. Its ledger names the configured-provider data boundary and the maximum cached-text chunk count. Apply recomputes the current plan, rejects stale coverage or provider state, records a durable idempotent receipt, and enqueues one background RAG job limited to the confirmed slice. Deferred or failed chunks keep coverage partial and remain visible for a later bounded repair. Repair does not contact GitCode and cannot purge or rebuild every namespace.

Ordinary responses expose `cache_ref` and a one-way path fingerprint, never an absolute cache path. Current coverage truth is kept separate from active contention, a scheduled retry, and the last stage error, so a transient maintenance failure cannot hide a still-current RAG namespace.

## UX cohort coverage

A feature creates a new UX cohort when it introduces a distinct user-visible resource, state, action, lifecycle, query scope, or remediation path. Its design and implementation decomposition must explicitly assess the embedded admin UI rather than assume that CLI or MCP coverage is sufficient.

For a user-facing cohort, the decomposition must include an independently trackable admin-UI task. The task should cover observation and permitted controls, navigation and deep-link state, capability and API projection, loading/empty/partial/stale/error states, accessibility, tests, embedded asset checks, and release gates. The UI may be capability-gated or delivered after its backend dependency, but that dependency and rollout gate must be explicit.

If a cohort is intentionally transport-only, internal, or CLI-only for safety, the active issue must record that rationale and describe any UI observation or handoff that remains necessary. A feature must not silently disappear from the operator console merely because its backend or CLI path shipped first.

## Read-only operator views

The observation UI is organized around product state rather than CLI command groups:

- **Overview** leads with current attention, service/cache readiness, active work, cache/repository cohort summaries, and recovered failures.
- **Caches** shows the safe cache → repository topology. Repository details keep head freshness, tail completeness, secondary coverage, projection generation, and RAG generation as five independent lanes.
- **Collections** shows bounded per-kind counts and head/tail frontier evidence. A bounded tail stop is always labelled partial; only end-of-collection evidence is presented as complete.
- **Documentation** shows repository-owned policy, exact commit or tracked-overlay authority, metadata/vector coverage, namespace, revision-set history, and daemon jobs. Initial worktree registration remains a local CLI action because the browser cannot choose or receive arbitrary absolute filesystem paths. After registration, the UI can confirm immediate HEAD/policy reconciliation, open the scoped index job for ordinary cancel/inspection controls, and search an explicit Git revision with digest-verified citations. Offline full-text becomes available from the registered Git authority even before a semantic set is published; semantic readiness is a separate signal and hybrid requests visibly fall back to lexical retrieval. The browser sends only an opaque registration id plus bounded search intent; the daemon resolves local paths from its private registry.
- **Search status / Search Lab** first explains full-text versus hybrid readiness, then lets an operator run a bounded side-by-side experiment. Score provenance, citations, fallback, namespace/generation coverage, provider smoke, and bounded repair remain distinct actions with explicit data boundaries.
- **Activity** presents bounded structured history. **Maintenance** adds capability-derived policy, binding, and feedback-delivery workbenches: edit intent, render the effect ledger, confirm the exact plan id, and inspect receipts or CLI handoffs. Feedback shows preparation/submission separately, all six readiness states, and only bound setup targets. Maintenance also shows canonical repository identity, aliases, legacy registration redirects, explicit conflict kinds, and the candidate plan/confirm workflow when `admin_maintenance_conflict_resolution` is enabled. **Jobs** adds URL-preserved filters and detail, retained progress, throughput/ETA where derivable, rate-limit/retry/interruption state, and explicit cancel/retry controls only where the daemon reports them safe. Each list row exposes an explicit detail affordance, typed failure/collection, and the next safe action before navigation. Active backoff disables whole-job and affected-collection retry and names the exact deadline plus coalescing behavior. Its lifecycle panel exposes the effective success/diagnostic TTLs, terminal and diagnostic caps, oldest retained time, and expiry/truncation evidence. An expired job deep link is explicitly labelled not retained and explains that cache/frontier/audit state is unaffected.
- **Diagnostics** separates current from recovered typed failures, gives fixed public-safe CLI handoffs, and shows the capability/safety catalog.

Local deep links use URL search parameters: `view`, opaque `cache`, public repository id `repo`, canonical or legacy `registration`, repository `tab`, diagnostic state, public `job`, job state/type/cache/repository/failure filters, and Search Lab `q`, `kind`, `provenance`, and `limit`. A legacy registration deep link is replaced with its canonical public id and an in-page redirect explanation. No cache path, credential, provider endpoint, raw source body, or session material is included. Browser reload and back/forward navigation preserve this state. The UI explicitly renders loading, empty, partial/degraded, stale, recovered, interrupted, waiting, and API-version-mismatch states; status meaning is always present as text rather than color alone.

## Themes

The visible selector has exactly three choices: **Light**, **Dark**, and **System**. **System is the default** when no preference is saved or an invalid value is found. It follows `prefers-color-scheme`; explicit Light and Dark choices are stored only in browser-local storage. A small external head script applies the choice before the application starts, avoiding a first-paint theme flash while keeping the CSP free of inline script allowances.

Typography, icon roles, surface hierarchy, component cohorts, and cross-view visual QA are specified in the [Admin UI design system](admin-ui-design-system.md). New screens and tabs must use that shared system rather than introduce view-local density or icon conventions.

## Release and upgrade checks

The committed static assets are part of the Go binary's reviewed source surface. Before an admin-UI release, run `scripts/check-admin-ui-assets.sh`, the frontend unit and Playwright suites, and the Go test/race/vet suites. Dependency licenses, accessibility states, binary/asset size, startup, and idle RSS have explicit budgets and measurement notes in [Admin UI release gates](admin-ui-release-gates.md).

Upgrades replace the binary and its embedded assets atomically; there is no separate web deployment or Node.js runtime. Existing sessions are intentionally short-lived and API-version mismatch forces a reload. To disable the UI, restart the daemon without `--admin` and do not run `admin open`; cached data, bindings, jobs, and maintenance registrations remain intact.
