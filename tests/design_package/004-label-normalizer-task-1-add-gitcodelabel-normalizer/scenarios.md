# Scenarios: 004-label-normalizer-task-1-add-gitcodelabel-normalizer

## Scenario 1
`004-label-normalizer-task-1-add-gitcodelabel-normalizer-scenario-1`: Operator runs stubbed `create-issue --live --labels bug,enhancement` against httptest GitCode `/api/v5/repos/{owner}/{repo}/issues`; request body contains `"labels":"[\"bug\",\"enhancement\"]"` (string, not array).

**Test coverage:** `TestLabel011CreateRequestLabelString` in `internal/gitcode/client_test.go` — creates a `CreateIssueRequest` with `Labels` set to the result of `EncodeIssueLabels([]string{"bug","enhancement"})`, verifies the httptest server receives labels as a JSON string containing the serialized array.

## Scenario 2
`004-label-normalizer-task-1-add-gitcodelabel-normalizer-scenario-2`: Operator runs mocked live issue sync on `GET /api/v5/repos/{owner}/{repo}/issues`; label object `{"id":1,"name":"bug","color":"#FF0000"}` yields `cache.Record.Labels == []string{"bug"}`.

**Test coverage:** `TestLabel010IssueResponseObjects` in `internal/gitcode/client_test.go` — httptest server returns issue JSON with `"labels":[{"id":1,"name":"bug","color":"#FF0000"},{"id":2,"name":"enhancement","color":"#00FF00"}]`; validates `issue.Labels == []string{"bug", "enhancement"}` and `issue.GitCodeLabels` contains the full object data.

## Scenario 3
`004-label-normalizer-task-1-add-gitcodelabel-normalizer-scenario-3`: Label object with missing `name` returns visible `schema_decode` diagnostic distinct from transport/configuration failure.

**Test coverage:** `TestLabel007NormalizeMissingName` in `internal/gitcode/label_normalizer_test.go` — validates `NormalizeLabels` returns `*ErrSchemaDecode` with `DiagnosticCode() == "schema_decode"` and `Field == "labels[0].name"`.
**Test coverage:** `TestLabel015ObjectLabelWithMissingIDReturnsSchemaDecode` in `internal/gitcode/label_normalizer_test.go` — JSON unmarshal of issue with `{"id":0,"name":"bug"}` produces `*ErrSchemaDecode`.
**Test coverage:** `TestLabel014SchemaDecodeDistinctFromTransport` in `internal/gitcode/label_normalizer_test.go` — confirms schema decode errors do not match `ErrNetworkUnavailable`.

## Scenario 4
`004-label-normalizer-task-1-add-gitcodelabel-normalizer-scenario-4`: `create-issue --live --labels ""` succeeds with `"labels":"[]"`.

**Test coverage:** `TestLabel012CreateRequestEmptyLabels` in `internal/gitcode/client_test.go` — `EncodeIssueLabels([]string{})` produces `"[]"`, httptest verifies request body contains `"[]"` as the labels value.

## Scenario 5
`004-label-normalizer-task-1-add-gitcodelabel-normalizer-scenario-5`: Outgoing issue payloads with labels as JSON array cause test to fail.

**Test coverage:** `TestLabel013ArrayLabelsRejected` in `internal/gitcode/client_test.go` — httptest server returns 400 when labels is a JSON array (`["bug"]`); test asserts 400 response. The DTO type change from `[]string` to `string` on `CreateIssueRequest.Labels` and `UpdateIssueRequest.Labels` prevents array serialization at compile time.

## Scenario 6
`004-label-normalizer-task-1-add-gitcodelabel-normalizer-scenario-6`: `go test ./...` and `git diff --check` pass offline.

**Test coverage:** Executed `go test ./...` and `git diff --check` — all 14 packages pass, git diff check passes with no whitespace errors.
