# Scenarios: 005-issue-normalizer-task-1-change-live-issue-response-identity-decoder-and-ca

## Scenario 1
`005-issue-normalizer-task-1-change-live-issue-response-identity-decoder-and-ca-scenario-1`: Sync actor runs mocked live issue reads through `GET /api/v5/repos/{owner}/{repo}/issues` using production adapter code; numeric `id` and string `number` responses are cached and readable through CLI or MCP with stable source identifiers, while malformed identity fixtures produce `schema_decode` diagnostics under `go test ./...`.

**Test coverage:**
- `TestScenario004ReadRouteContract` in `internal/gitcode/client_test.go` — httptest server returns `[{"id":"MOCK-ISSUE-7","number":7,...},{"id":8,"number":"8",...}]` and validates `items[0].ID == "MOCK-ISSUE-7"`, `items[1].ID == "8"`, `items[1].Number == 8`.
- `TestIssueIdentity001NumericIDFloat64` in `internal/gitcode/issue_normalizer_test.go` — `{"id":42,"number":42}` decodes to `ID="42"`, `Number=42`.
- `TestIssueIdentity002StringID` in `internal/gitcode/issue_normalizer_test.go` — `{"id":"ISSUE-99","number":99}` decodes to `ID="ISSUE-99"`, `Number=99`.
- `TestIssueIdentity003StringNumber` in `internal/gitcode/issue_normalizer_test.go` — `{"id":"1","number":"7"}` decodes to `Number=7`.
- `TestIssueIdentity004MissingID` in `internal/gitcode/issue_normalizer_test.go` — missing `id` → `*ErrSchemaDecode{Field:"id"}`, `DiagnosticCode() == "schema_decode"`.
- `TestIssueIdentity005IDZero` in `internal/gitcode/issue_normalizer_test.go` — `id=0` → `*ErrSchemaDecode{Field:"id"}`.
- `TestIssueIdentity006MissingNumber` in `internal/gitcode/issue_normalizer_test.go` — missing `number` → `*ErrSchemaDecode{Field:"number"}`.
- `TestIssueIdentity007NumberZero` in `internal/gitcode/issue_normalizer_test.go` — `number=0` passes (valid).
- `TestIssueIdentity008NumberZeroString` in `internal/gitcode/issue_normalizer_test.go` — `number="0"` passes with `Number=0`.
- `TestIssueIdentity009RoundTripNumericIDStringNumber` in `internal/gitcode/issue_normalizer_test.go` — full round-trip `{"id":7,"number":"7","title":"round trip"}` decodes correctly for both `IssueSummary` and `Issue`.
- `TestIssueIdentity010MalformedPayloadSchemaDecodeDistinct` in `internal/gitcode/issue_normalizer_test.go` — malformed `number="abc"` → `*ErrSchemaDecode`, does NOT match `*ErrNetworkUnavailable`.
- `TestIssueIdentity011SchemaDecodeNotTransport` in `internal/gitcode/issue_normalizer_test.go` — empty `id=""` → `*ErrSchemaDecode`, does NOT match `*ErrNetworkUnavailable`.
- `TestIssueIdentity012IDZeroStringInvalid` in `internal/gitcode/issue_normalizer_test.go` — `id="0"` → `*ErrSchemaDecode{Field:"id"}`.
- `TestIssueIdentity013ExistingFixtureStringIDsStillWork` in `internal/gitcode/issue_normalizer_test.go` — existing fixture string IDs `"ISSUE-41"`, `"ISSUE-42"` decode correctly.
- `TestIssueIdentity014NilID` in `internal/gitcode/issue_normalizer_test.go` — `id:null` → `*ErrSchemaDecode{Field:"id"}`.
- `TestIssueIdentity015BoolIDRejected` in `internal/gitcode/issue_normalizer_test.go` — `id:true` → `*ErrSchemaDecode{Field:"id"}`.
- `TestIssueIdentity016LabelsStillWorkWithNumericID` in `internal/gitcode/issue_normalizer_test.go` — labels with numeric id decode correctly alongside normalized identity.

**Production code:**
- `decodeID`/`decodeNumber` in `internal/gitcode/models.go:209-246` — emit `*ErrSchemaDecode` for nil, zero (id), empty string, and unexpected types.
- `CodeSchemaDecode` in `internal/diagnostics/classifier.go:25` — `"schema_decode"` diagnostic code with routing in `codeFromError`, `classifyCode`, `messageFor`, and `exitClassFor`.
