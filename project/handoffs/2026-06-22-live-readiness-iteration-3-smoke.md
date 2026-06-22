# Handoff: live-readiness iteration 3 smoke

Task: iteration 3 live-readiness acceptance smoke

Commit checked: `ff65311`

Branch checked: `main`

## Context

The third implementation wave was merged to `main` and the operator installed the current binary with `go install`.

This smoke pass rechecked the same product path used after iteration 2:

- CLI discovery and readiness commands;
- credential diagnostics;
- repository binding and cache sync into a fresh cache;
- source search, chunk listing, and stale-index status;
- connected MCP reads;
- HTTP/SSE MCP transport;
- explicit `--live` provider selection behavior.

All live repository coordinates, tokens, and local operator paths are intentionally omitted from this report. Commands below use placeholders.

## Environment

- Binary: `gitcode-mcp 0.1.0`
- Installed binary path: Go bin install path
- Worktree state before and after smoke: clean `main`
- Default validation command: `./scripts/check.sh`
- Fresh smoke cache: temporary cache directory outside the repository

## Commands and results

### Offline validation

```sh
./scripts/check.sh
```

Result: passed.

Packages passed:

- `cmd/gitcode-mcp`
- `internal/audit`
- `internal/cache`
- `internal/cli`
- `internal/config`
- `internal/credential`
- `internal/diagnostics`
- `internal/doctor`
- `internal/gitcode`
- `internal/index`
- `internal/mcp`
- `internal/provider/live`
- `internal/service`
- `internal/testnet`

### CLI discovery

```sh
gitcode-mcp --version
gitcode-mcp --help
gitcode-mcp auth --help
gitcode-mcp doctor --help
gitcode-mcp repo --help
gitcode-mcp sync --help
gitcode-mcp create-issue --help
gitcode-mcp bind --help
gitcode-mcp mcp serve --help
gitcode-mcp --mcp --help
```

Result: mostly passed.

Observed improvements:

- root help advertises `--live`;
- `auth status` is discoverable;
- `doctor` is discoverable;
- `bind` compatibility help exists;
- `sync --help` documents `--live`;
- `create-issue --help` requires exactly one of `--dry-run` or `--live`;
- `mcp serve --help` exists;
- legacy `--mcp --help` still exists.

Observed gap:

```sh
gitcode-mcp mcp --help
```

returned `unknown command "mcp"` even though `gitcode-mcp mcp serve --help` works and root help advertises `mcp serve`.

## Credential and doctor diagnostics

```sh
gitcode-mcp auth status --format json
gitcode-mcp doctor --runtime-audit --format json
```

Result: passed for missing-token diagnostics.

Observed:

- `auth status` reports `source: missing`;
- `auth status` reports `error_class: token-missing`;
- remediation points to `GITCODE_TOKEN` or credential store;
- available sources include env token, keychain, and none.

With an invalid placeholder token set, `doctor --live --runtime-audit --repo <repo-id>` reported:

- `credential_source: env:GITCODE_TOKEN`;
- `token_present: true`;
- no raw token value.

Observed gap:

- `doctor --live --cache-path <temp-cache> --runtime-audit --format json` still reported the default cache path in its runtime-audit config block instead of the command-line `--cache-path` override.
- `doctor --live` output was mostly runtime-audit config; it did not yet provide the full repo/cache/index/MCP/live-provider readiness summary requested by the iteration 3 target.

## Fresh cache offline path

A fresh temporary cache was created and a sanitized repository binding was added:

```sh
gitcode-mcp --cache-path <temp-cache> repo add \
  --repo <repo-id> \
  --owner <owner> \
  --name <repo> \
  --api-base-url https://api.gitcode.com/api/v5 \
  --scopes issues,wiki \
  --alias <alias> \
  --format json
```

Result: passed.

Then fixture/default sync was run:

```sh
gitcode-mcp --cache-path <temp-cache> sync \
  --repo <repo-id> \
  --issues \
  --wiki \
  --index \
  --format json
```

Result: passed.

Observed:

- `success_count: 2`;
- issue and wiki sync results both succeeded;
- sync events now include `started_at`, `completed_at`, and `zero_delta`;
- default non-live sync remains offline/fixture-backed and does not require credentials.

## Cache, search, chunks, and stale index

Commands:

```sh
gitcode-mcp --cache-path <temp-cache> cache-status --repo <repo-id> --format json
gitcode-mcp --cache-path <temp-cache> list --repo <repo-id> --format json
gitcode-mcp --cache-path <temp-cache> search_sources --repo <repo-id> fixture --format json
gitcode-mcp --cache-path <temp-cache> list-chunks --repo <repo-id> --format json
gitcode-mcp --cache-path <temp-cache> stale-index --repo <repo-id> --format json
gitcode-mcp --cache-path <temp-cache> sync-status --repo <repo-id> <source-id> --format json
```

Result: passed when run sequentially.

Observed improvements:

- `search_sources` no longer returns `cache_empty` after sync/index;
- `search_sources` returns cached `issue` and `wiki` source records;
- `list-chunks` returns two indexed chunks with `indexed_at` metadata;
- `stale-index` reports `stale_count: 0` on the fresh indexed cache;
- `cache-status` reports `index_freshness_warnings: 0`;
- `sync-status` reports `freshness: fresh`.

Observed lock caveat:

- Parallel CLI reads on a freshly initialized SQLite cache can still return lock contention on a writer lock with operation `migration`.
- Sequential reads pass.
- The failure class was `internal_error`, so the operator UX is still rough.

## Connected MCP and HTTP/SSE transport

The already connected MCP server was probed through the Codex MCP tools.

Observed:

- `cache_status`, `list_sources`, `search_sources`, `stale_index_report`, and `list_chunks` respond;
- `search_sources` works and returns `issue` and `wiki`;
- the connected server appears to be using an older cache state where `stale_index_report` still reports stale chunks with zero `indexed_at`;
- one `cache_status` call was very slow.

A separate local HTTP/SSE MCP server was started over the fresh temporary cache:

```sh
gitcode-mcp --cache-path <temp-cache> mcp serve --transport http-sse --bind 127.0.0.1:<port>
```

Result: passed.

Transport checks:

```sh
curl http://127.0.0.1:<port>/health
curl http://127.0.0.1:<port>/ready
curl -N http://127.0.0.1:<port>/sse
```

Observed:

- `/health` returns 200 with `{"status":"ok"}`;
- `/ready` returns 200 with `{"ready":true}`;
- `/sse` emits an endpoint event for `/message?session_id=...`;
- JSON-RPC `initialize` succeeds;
- `tools/list` succeeds;
- `cache_status` tool succeeds;
- `stale_index_report` tool succeeds with `stale_count: 0` on the fresh cache.

MCP schema improvement:

- `search_sources`, `list_sources`, `search_chunks`, and related kind filters now expose `issue` and `wiki` enums.

Route naming note:

- The implemented health routes are `/health` and `/ready`, not `/healthz` and `/readyz`.

## Live provider selection check

The key live-readiness blocker remains.

### Live sync with no token

```sh
gitcode-mcp --cache-path <temp-cache> sync --live \
  --repo <repo-id> \
  --issues \
  --wiki \
  --index \
  --format json
```

Observed:

- command succeeded;
- returned fixture-shaped zero-delta results;
- did not fail with missing credentials.

Expected:

- explicit `--live` should require credentials and select the live GitCode provider;
- without credentials it should fail with a typed missing-credential diagnostic;
- it must not silently route to fixture/offline provider.

### Live sync with invalid placeholder token

Same command with `GITCODE_TOKEN` set to an invalid placeholder value.

Observed:

- command still succeeded;
- returned fixture-shaped zero-delta results.

Expected:

- command should attempt live HTTP provider and fail with auth/network/provider diagnostic;
- successful fixture result under `--live` is incorrect.

### Live write with invalid placeholder token

```sh
gitcode-mcp --cache-path <temp-cache> create-issue --live \
  --repo <repo-id> \
  --title <test-title> \
  --body <test-body> \
  --idempotency-key <test-key> \
  --format json
```

Observed:

- command failed with `fixture client is read-only`;
- failure was wrapped as `write_provider_error`;
- this proves write execution is still using the fixture client under explicit `--live`.

Expected:

- command should construct the live GitCode HTTP provider;
- with an invalid token it should fail as auth/network/provider error, not fixture read-only.

## Main findings

### Fixed since iteration 2

- Offline tests pass.
- Auth status surface exists.
- Doctor command exists.
- Fresh-cache `stale-index` false positives are fixed.
- `search_sources` works after sync/index.
- MCP kind enums include `issue` and `wiki`.
- HTTP/SSE MCP transport is usable on a fresh cache.
- Dry-run write validation works.
- Sync events now expose start/completion/zero-delta fields.

### Still blocking live usability

1. Explicit `--live` sync still silently uses fixture/offline behavior.
2. Explicit `--live` write still reaches `fixture client is read-only`.
3. Live provider selection is therefore not wired into the real sync/write service path.

### Secondary gaps

1. `gitcode-mcp mcp --help` fails while `gitcode-mcp mcp serve --help` works.
2. `doctor --live --cache-path ...` reports the default cache path in runtime-audit output.
3. `doctor --live` still lacks the full operator readiness summary.
4. Fresh-cache parallel CLI reads can hit migration writer-lock contention and report `internal_error`.
5. A long-running connected MCP server may continue to expose stale cache state until that cache is reindexed or rebuilt.

## Recommended next action

Focus the next implementation slice narrowly on provider wiring:

1. Add a failing test that runs `sync --live` with no token and asserts a missing-credential error, not fixture success.
2. Add a failing test that runs `sync --live` with an invalid token against a fake HTTP client and proves the live provider is selected.
3. Add a failing test that runs `create-issue --live` and proves it does not route to the fixture client.
4. Fix service/CLI construction so provider mode and credentials are injected into both sync and write paths.
5. Re-run fresh-cache CLI smoke and HTTP/SSE MCP smoke.
6. Only after provider wiring is proven, run credential-gated live two-cache e2e.

## Commands not executed

Credential-gated real live two-cache e2e was not executed in this smoke pass. The environment available to this session did not expose a usable real token through the normal shell path, and the explicit invalid-token tests were sufficient to prove provider selection still routes to fixture behavior.
