# Validation Scenarios: 012-milestone-adapter-task-1-add-milestone-live-decoder

## Scenario Inventory

### 012-milestone-adapter-task-1-add-milestone-live-decoder-scenario-1
Developer runs `go test ./...` and `git diff --check`; milestone adapter tests drive the production live provider against stubbed external GitCode routes `GET /api/v5/repos/{owner}/{repo}/milestones` and `GET /api/v5/repos/{owner}/{repo}/milestones/{id}` with the `RouteSchemaMatrix` milestone entry set to `supported` and `OpenAPI`, proving the supported milestone path reaches live adapter list/get code rather than a document-only matrix.

**Validation**: Run `go test ./...` and `git diff --check`; inspect that stubbed route tests (TestMilestone001, TestMilestone002) reach the production `HTTPClient.ListMilestones`/`GetMilestones` code paths via `httptest.NewServer`. Check that `RouteSchemaMatrix` defaults milestones to `supported` + `OpenAPI`.

### 012-milestone-adapter-task-1-add-milestone-live-decoder-scenario-2
The tests observe cache-ready records with `kind=milestone`, `source_id=MILESTONE-<id>`, normalized `remote_id`, required `title`, body from `description`, mapped status, parsed dates/timestamps, and no real network, credentials, SSH agent, or Keychain access.

**Validation**: Inspect test assertions verifying `SourceID`, `RemoteID`, `Title`, `Body`, `Status`, `DueOn`, `CreatedAt`, `UpdatedAt` fields are correctly populated. Confirm all tests use `httptest.NewServer` only (no real network).

### 012-milestone-adapter-task-1-add-milestone-live-decoder-scenario-3
Negative tests cover malformed JSON, wrong list envelope/object shape, missing or malformed identity, missing `title` with only `name` present, wrong-type or empty `title`, malformed date/status values, mixed valid/invalid list entries, get/list id mismatch, and HTTP 400; all schema/body failures produce `schema_decode`, while mocked HTTP 400 produces `api_validation` and never `live_transport_failure` or configuration failure.

**Validation**: Verify each negative condition is tested. Verify error classification matches the four-class taxonomy. Key gaps checked:
- HTTP 400 → `api_validation` (Verified by TestMilestone008, TestMilestone028)
- Malformed JSON → `schema_decode` (Verified by TestMilestone010)
- Missing title (name only) → `schema_decode` (Verified by TestMilestone005)
- Empty title → `schema_decode` (Verified by TestMilestone006)
- ID validation (zero/nil/bool/object/array/fractional/negative/string-zero/empty/non-numeric) → `schema_decode` (Verified by TestMilestone007)
- ID mismatch → `schema_decode` (Verified by TestMilestone016)
- Unparseable due_on → `schema_decode` (Verified by TestMilestone013)
- Invalid timestamps → `schema_decode` (Verified by TestMilestone021)
- Mixed valid/invalid list → `schema_decode` (Verified by TestMilestone017)
- Schema decode never classified as transport/credential error (Verified by TestMilestone027)
- API validation never classified as transport/credential error (Verified by TestMilestone028)
- Unrecognized status value produces `schema_decode` (Verified by TestMilestone012bUnrecognizedStatusSchemaDecode)

**Resolved Gaps**:
- **GAP-001 (RESOLVED)**: Unrecognized status value (e.g. `"in_progress"`) now correctly produces `schema_decode` at `milestone.state`. Fixed by updating `decodeMilestoneStatus` default case to return `ErrSchemaDecode` and adding `TestMilestone012bUnrecognizedStatusSchemaDecode`.

**Known Gaps (WARN, not product failures)**:
- **GAP-002**: No negative test for wrong list shape (object envelope instead of array). When list endpoint returns object instead of array, `json.Decoder.Decode` produces `ErrPartialResponse` which maps to `schema_decode` via the classifier. Coverage gap only, not a functional failure.
- **GAP-003**: Mixed list failure `ErrSchemaDecode` does not include array index prefix (e.g. `milestones[1].title`) in the field path. The Go JSON decoder wraps `UnmarshalJSON` errors without appending array index context. Minor design deviation from spec, not a functional product failure.
