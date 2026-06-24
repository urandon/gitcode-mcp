# Design Package Component: label-normalizer

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Label Normalizer

## Summary
The label-normalizer component adds GitCode-shaped label response parsing, required-field validation, cache-compatible label-name output, and issue-label JSON-string encoding. It replaces GitHub-like label array assumptions at the component boundary while leaving cache record ownership to cache and service layers.

## Top-Level Alignment
This component implements the approved label contract: issue mutation labels are JSON-string encoded, and GitCode response label objects are normalized after validating `id` and `name`. It supports Task 3 directly and emits `schema_decode` evidence for Task 7 without owning the broader error classifier.

## Tasks

### Task 1: Add GitCodeLabel normalizer
Outcome IDs: outcome-3, outcome-7, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-2
Change Type: add
Description: Add GitCode label response DTO and issue-label payload encoder owned by the label-normalizer component. The component converts GitCode label objects into cache-compatible label names, validates malformed label objects as schema failures, and prevents GitHub-like issue mutation label arrays from crossing the live mutation boundary.
Existing Behavior / Reuse: Current `internal/gitcode/` models carry `Labels []string` on issue DTOs and serialize `Labels []string` as JSON array in issue create/update requests. Cache-facing `Record.Labels` and `Source.Labels` remain reusable as `[]string`. Existing diagnostics include transport/configuration/API buckets, but no component-local label validation that reports `schema_decode`. The `internal/errors/` package provides error type infrastructure that `ErrSchemaDecode` extends.
Detailed Design: Add `GitCodeLabel` struct in `internal/gitcode/` with fields: `ID int64`, `Name string`, `Color string`, `CreatedAt string`, `UpdatedAt string`, tagged `json:"id"`, `json:"name"`, `json:"color"`, `json:"created_at"`, `json:"updated_at"`. Change live issue response DTOs that decode labels as `[]string` so the live path decodes labels as `[]GitCodeLabel`; guard behind provider-mode check so fixture path still works with existing `[]string` decoding. Change issue create/update request DTOs so the GitCode-facing `labels` JSON field encodes as `string` (e.g., `"labels":"[\"bug\",\"enhancement\"]"`) not `[]string`. Add `EncodeIssueLabels(labels []string) string` that trims each label, drops empty entries, `json.Marshal`s remaining names, returns serialized string; all-empty returns `"[]"`. Add `NormalizeLabels(labels []GitCodeLabel) ([]string, error)` that preserves input order, returns empty non-nil `[]string{}` for empty input, validates each label: `ID != 0` (field path `labels[N].id`), `strings.TrimSpace(Name) != ""` (field path `labels[N].name`), returns first failure as `*ErrSchemaDecode`. Add `NormalizeSingleLabel(label GitCodeLabel) (string, error)` using same validation rules. Add `ErrSchemaDecode` struct to `internal/gitcode/errors.go` implementing the existing error interface with `DiagnosticCode() string` returning `"schema_decode"`, plus `Field`, `Expected`, `Received` fields. Register `ErrSchemaDecode` in the error classifier mapping so `DiagnosticCode() == "schema_decode"` routes to schema/decode failure bucket, distinct from transport and configuration. For decommission-2 enforcement, the product issue create/update path must fail tests when issue payload serializes labels as JSON array instead of JSON string; enforce through the DTO type change (string field) plus a mocked httptest route that inspects the incoming request body and returns 400 if `labels` is a JSON array value.
Acceptance Criteria: Operator runs stubbed `create-issue --live --labels bug,enhancement` against httptest GitCode `/api/v5/repos/{owner}/{repo}/issues`; request body contains `"labels":"[\"bug\",\"enhancement\"]"` (string, not array). Operator runs mocked live issue sync on `GET /api/v5/repos/{owner}/{repo}/issues`; label object `{"id":1,"name":"bug","color":"#FF0000"}` yields `cache.Record.Labels == []string{"bug"}`. Label object with missing `name` returns visible `schema_decode` diagnostic distinct from transport/configuration failure. `create-issue --live --labels ""` succeeds with `"labels":"[]"`. Outgoing issue payloads with labels as JSON array cause test to fail. `go test ./...` and `git diff --check` pass offline.
Workload: 2.25 MM

## Cross-Cutting Constraints
- Offline validation must exercise real target runtime paths with httptest GitCode routes; normal tests cannot require credentials, network, SSH agent, or Keychain — required for Task 10 evidence across the component boundary
- Outbound issue mutation labels must be GitCode JSON-string encoded, not GitHub-shaped JSON arrays — required by the decommissioned label payload contract
- Malformed GitCode label objects must surface as `schema_decode`, not transport or configuration failures — required for classifier-visible schema diagnostics
- Cache/source record ownership remains outside this component; label-normalizer emits `[]string` label names for existing cache-facing fields — preserves approved component responsibility

## Data And Control Flow
- CLI issue commands parse comma-separated labels into `[]string`; service mutation code calls `EncodeIssueLabels` before constructing GitCode live issue request DTOs — encoder owns GitCode payload shape
- GitCode issue and label responses decode into `GitCodeLabel` values; live adapter or service code calls `NormalizeLabels` or `NormalizeSingleLabel` before cache commit or write confirmation — normalizer owns response validation
- `NormalizeLabels` detects missing or malformed `id` or `name`; it returns `*ErrSchemaDecode`; diagnostics classify the error as `schema_decode` before CLI/MCP reporting — schema failures stay distinct from transport/configuration errors
- Existing cache `Record.Labels` and `Source.Labels` receive only normalized label names; provenance and record commit remain owned by sync/cache code — component boundary stays label-specific

## Component Interactions
- `cli_mutation_commands` -> `label-normalizer` — passes parsed label names to `EncodeIssueLabels` for GitCode issue mutation payloads
- `live_adapter` -> `label-normalizer` — passes decoded `GitCodeLabel` values from issue and label responses into normalization before cache commit or write confirmation
- `label-normalizer` -> `error_classifier` — emits `*ErrSchemaDecode` with diagnostic code `"schema_decode"`, which the classifier maps separately from transport/configuration failures
- `label-normalizer` -> `cache_provenance_layer` — provides cache-compatible label names while provenance remains owned by sync/cache commit code

## Rationale
The approved architecture assigns GitCode label object conversion and required-field validation to `label_normalizer`. Repository inspection confirms the current GitCode models still use `Labels []string` for issue responses and issue mutation requests, so the required GitCode-shaped normalizer and JSON-string encoder are concrete missing component-local work. The Component Impact delta `label-normalizer-delta-1` carries `supporting_evidence` role; the compiled task inherits that role.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0636-run_attempt-1/final_message.txt`
