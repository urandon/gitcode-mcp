# Scenarios: 013-cli-label-mutation-commands-task-1-change-cli-label-issue-writes

## Scenario 1: 013-cli-label-mutation-commands-task-1-change-cli-label-issue-writes-scenario-1

Operator runs `gitcode-mcp create-issue --live --repo fixture-a --title t --labels bug,enhancement` against a stubbed GitCode issue route; the target product surface `POST /api/v5/repos/{owner}/{repo}/issues` receives JSON where `labels` is the string `["bug","enhancement"]`, the CLI reports a succeeded write, and a stubbed-external-provider CLI/server test proves the payload shape.

### Product Surface

GitCode live issue create endpoint `POST /api/v5/repos/{owner}/{repo}/issues` with labels encoded as native JSON array in request body.

### Steps

1. Run `go test ./internal/gitcode/... -count=1 -run "TestScenario013001CreateIssueLabelsAsNativeJSONArray" -v`
2. Verify the httptest.Server captures the request body and asserts `labels` is a native JSON array (`["bug","enhancement"]`)
3. Verify `CreateIssue` returns a confirmed write result
4. Run `go test ./internal/gitcode/... -count=1 -run "TestLabel013ArrayLabelsAccepted" -v`
5. Verify the subtest "array_labels_via_DTO_accepted" passes (native array accepted)
6. Verify the subtest "double-encoded_string_labels_rejected" passes (string-encoded labels rejected with 400)

### Expected Result

- `TestScenario013001CreateIssueLabelsAsNativeJSONArray` passes: request body contains `"labels":["bug","enhancement"]` as a native JSON array
- `TestLabel013ArrayLabelsAccepted/array_labels_via_DTO_accepted` passes
- `TestLabel013ArrayLabelsAccepted/double-encoded_string_labels_rejected` passes: double-encoded strings are rejected

### Decommission Check

Labels must NOT be sent as double-encoded strings (e.g., `"labels":"[\"bug\",\"enhancement\"]"`). The double-encoded test proves this is rejected.

## Scenario 2: 013-cli-label-mutation-commands-task-1-change-cli-label-issue-writes-scenario-2

Operator runs `gitcode-mcp update-issue --live --repo fixture-a --number 42 --labels bug` against a stubbed GitCode issue route; the target route `/api/v5/repos/{owner}/{repo}/issues/42` receives string-encoded labels and the refreshed cache record contains the returned issue labels.

### Product Surface

GitCode live issue update endpoint `PATCH /api/v5/repos/{owner}/{repo}/issues/42` with labels encoded as native JSON array in request body.

### Steps

1. Run `go test ./internal/gitcode/... -count=1 -run "TestScenario013002UpdateIssueLabelsAsNativeJSONArray" -v`
2. Verify the httptest.Server captures the request body at `PATCH /api/v5/repos/example-owner/example-repo/issues/42`
3. Verify the request body contains `"labels":["bug"]` as a native JSON array
4. Verify `UpdateIssue` returns a confirmed write result with the returned issue labels normalized

### Expected Result

- `TestScenario013002UpdateIssueLabelsAsNativeJSONArray` passes
- Request body contains `"labels":["bug"]` as a native JSON array (not double-encoded)
- The returned issue labels from the GitCode response are correctly deserialized

## Scenario 3: 013-cli-label-mutation-commands-task-1-change-cli-label-issue-writes-scenario-3

Operator runs `gitcode-mcp add-label --live --repo fixture-a --number 42 --label bug`; the command either succeeds through the selected issue-update route or returns the designed unsupported diagnostic, and executable tests fail if the old `/api/v5/repos/{owner}/{repo}/issues/{number}/labels` route is used as a successful product mutation.

### Product Surface

CLI `add-label` command routing through service `AddLabel` -> `executeWrite` -> `callWriteAdapter`, and liveProvider `AddLabel` method.

### Steps

1. Run `go test ./internal/service/... -count=1 -run "TestAddLabelDryRunNoMutation" -v`
2. Verify dry-run returns `ErrUnsupportedCapability` with capability key `add_label`
3. Verify the fake client's `addLabelCalls` counter is 0 (no adapter call made)
4. Run `go test ./internal/service/... -count=1 -run "TestAddLabelLiveUnsupportedCapability" -v`
5. Verify live mode returns `ErrUnsupportedCapability` with capability key `add_label`
6. Verify the fake client's `addLabelCalls` counter is 0
7. Run `go test ./internal/gitcode/... -count=1 -run "TestScenario013004AddLabelReturnsUnsupportedCapability" -v`
8. Verify liveProvider.AddLabel returns `ErrUnsupportedCapability` without making any HTTP call to the old add-label endpoint
9. Run `go test ./internal/gitcode/... -count=1 -run "TestScenario013006AddLabelEndpointAbsentFromProvider" -v`
10. Verify liveProvider.AddLabel returns `ErrUnsupportedCapability` without reaching any server

### Expected Result

- `TestAddLabelDryRunNoMutation` passes
- `TestAddLabelLiveUnsupportedCapability` passes
- `TestScenario013004AddLabelReturnsUnsupportedCapability` passes
- `TestScenario013006AddLabelEndpointAbsentFromProvider` passes
- The old `POST /api/v5/repos/{owner}/{repo}/issues/{number}/labels` route is NEVER called as a successful write mutation
- All add-label paths return the designed `ErrUnsupportedCapability` diagnostic

### Decommission Check

The `addLabelEndpoint` (`POST /api/v5/repos/{owner}/{repo}/issues/{number}/labels`) must never be invoked as a successful product mutation path. Tests that call liveProvider.AddLabel must fail with `ErrUnsupportedCapability` before any HTTP request is made.

## Scenario 4: 013-cli-label-mutation-commands-task-1-change-cli-label-issue-writes-scenario-4

Developer runs `go test ./...` and `git diff --check`; tests pass offline without credentials, network, SSH agent, or Keychain, including regression cases for JSON-array label payloads and stale add-label routes.

### Product Surface

Repository-wide offline test suite, label normalizer unit tests, client label serialization tests, service add-label tests, CLI write command tests.

### Steps

1. Run `go test ./... -count=1`
2. Verify all 14 packages pass
3. Run `git diff --check`
4. Verify no whitespace violations
5. Run `go test ./internal/gitcode/... -count=1 -run "TestLabel" -v`
6. Verify all label normalizer tests pass (EncodeIssueLabels, NormalizeLabels, NormalizeSingleLabel, schema_decode vs transport error classification)
7. Run `go test ./internal/gitcode/... -count=1 -run "TestScenario013" -v`
8. Verify all 7 scenario-013 tests pass (SCN-001 through SCN-008)
9. Run `go test ./internal/service/... -count=1 -run "TestAddLabel" -v`
10. Verify both add-label tests pass
11. Run `go test ./internal/cli/... -count=1 -run "TestQueryCommandsUseServiceOnly" -v`
12. Verify CLI write commands dispatch to service without adapter calls for dry-run

### Expected Result

| Check | Expected |
|---|---|
| `go test ./...` | All packages pass (exit code 0) |
| `git diff --check` | No output (no whitespace violations) |
| `TestLabel001` through `TestLabel015` | All pass |
| `TestLabel013ArrayLabelsAccepted` | Both subtests pass: native array accepted, double-encoded rejected |
| `TestScenario013001` through `TestScenario013008` | All 7 tests pass |
| `TestAddLabelDryRunNoMutation` | Passes |
| `TestAddLabelLiveUnsupportedCapability` | Passes |
| `TestQueryCommandsUseServiceOnly` | Passes (all write commands dry-run correctly) |
| No credentials required | All tests use httptest.Server or fake client |
| No network access | All tests run offline |
| No SSH agent / Keychain | Not used in any test |

### Scenario IDs Exercised

- `TestScenario013001CreateIssueLabelsAsNativeJSONArray`: Create-issue labels as native JSON array (SCN-001)
- `TestScenario013002UpdateIssueLabelsAsNativeJSONArray`: Update-issue labels as native JSON array (SCN-002)
- `TestScenario013003CreateIssueNoLabelsOmitsField`: No labels omits `labels` field (SCN-003)
- `TestScenario013004AddLabelReturnsUnsupportedCapability`: Add-label returns unsupported with httptest guard (SCN-004)
- `TestScenario013006AddLabelEndpointAbsentFromProvider`: Add-label unreachable from live provider (SCN-006)
- `TestScenario013007CreateIssueWithLabelsNormalizedInResponse`: Response labels normalized to cache []string (SCN-007)
- `TestScenario013008EncodeIssueLabelsOutputIsJSONArray`: EncodeIssueLabels produces valid JSON array (SCN-008)
- `TestLabel011CreateRequestLabelString`: Label string encoding on create
- `TestLabel012CreateRequestEmptyLabels`: Empty labels produce `[]`
- `TestLabel013ArrayLabelsAccepted`: Array labels accepted, string labels rejected
- `TestLabel014SchemaDecodeDistinctFromTransport`: Schema decode distinct from transport error
- `TestLabel015ObjectLabelWithMissingIDReturnsSchemaDecode`: Missing label ID -> schema_decode
- `TestAddLabelDryRunNoMutation`: Service add-label dry-run returns unsupported
- `TestAddLabelLiveUnsupportedCapability`: Service add-label live returns unsupported
- `TestQueryCommandsUseServiceOnly`: CLI write commands dispatch to service

### Decommission Check

The old add-label endpoint (`POST /api/v5/repos/{owner}/{repo}/issues/{number}/labels`) is absent from any successful execution path. Double-encoded string labels are rejected by the API (confirmed by `TestLabel013ArrayLabelsAccepted/double-encoded_string_labels_rejected`). All label mutation paths use native JSON array encoding via `EncodeIssueLabels` producing `json.RawMessage`.
