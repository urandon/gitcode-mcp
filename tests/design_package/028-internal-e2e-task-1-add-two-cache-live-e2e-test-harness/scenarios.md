# Validation scenarios for 028-internal-e2e-task-1-add-two-cache-live-e2e-test-harness

## 028-internal-e2e-task-1-add-two-cache-live-e2e-test-harness-scenario-1

An operator runs `go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/` with `GITCODE_TOKEN`, `GITCODE_E2E_OWNER`, and `GITCODE_E2E_REPO` set in the environment.

Offline materialization: `run.sh` first runs the command without those env vars and requires a clean skip naming only missing env var names. It then runs the same command with those env vars pointed at a local stub GitCode-compatible HTTP server.

## 028-internal-e2e-task-1-add-two-cache-live-e2e-test-harness-scenario-2

Evidence type: API test.

Offline materialization: the e2e test uses the production `*gitcode.HTTPClient` against a local `httptest.Server` that implements the GitCode API routes used by the harness. The server is a stubbed external provider only; the product client, service, and cache runtime paths are not mocked.

## 028-internal-e2e-task-1-add-two-cache-live-e2e-test-harness-scenario-3

Product surface: the e2e test binary exercises the live `*gitcode.HTTPClient` against the GitCode REST API and two independent `*cache.SQLiteStore` cache databases wired through `*service.Service`.

Offline materialization: `run.sh` injects a test-only stub server into the copied worktree, sets `GITCODE_E2E_BASE_URL` to the server URL, and invokes `TestE2ELiveTwoCache`. The harness creates two temp SQLite cache files through production cache constructors and wires both through production `service.NewWithClient`.

## 028-internal-e2e-task-1-add-two-cache-live-e2e-test-harness-scenario-4

Expected outcome: both caches contain equivalent `cache.Record` content including the created issue, as verified by `(type, remote_id) -> {type, title, normalized_body, status}` digest comparison and comment count parity; test output contains no raw token value and no `Authorization:` header patterns, as verified by post-hoc scan of all recorded log/error messages against the raw token string (exact match) and the `Authorization:\s*\S+` regex.

Offline materialization: the stub server returns deterministic issue, comment, and wiki data, accepts one created issue, and then serves that created issue to both cache sync paths. The production e2e harness performs its digest comparison, comment count parity check, and redaction scan. `run.sh` additionally scans captured test output for the offline token and Authorization header pattern.
