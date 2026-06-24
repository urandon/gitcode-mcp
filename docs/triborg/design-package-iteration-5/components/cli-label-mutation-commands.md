# Design Package Component: cli-label-mutation-commands

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: CLI Label Mutation Commands

## Summary
CLI label mutation commands require a component-local change because current issue write requests still carry labels as `[]string` through the CLI/service/provider path and live add-label still targets the old issue-label route. This design changes the CLI issue create/update/add-label mutation surface so GitCode live writes use JSON-string label payload semantics and add-label is routed through issue update behavior or a typed unsupported diagnostic.

## Top-Level Alignment
This component implements the architecture’s CLI mutation command responsibility for Task 3: GitCode issue create/update labels must be encoded as JSON-string lists, and the old `/issues/{number}/labels` product mutation route must be removed from successful execution. It also preserves the read-oriented MCP decision by keeping CLI as the supported mutation surface.

## Tasks

### Task 1: Change CLI label issue writes
Outcome IDs: outcome-3, outcome-7, outcome-9, outcome-10
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-2
Change Type: change
Description: The CLI issue mutation surface owns user-facing `create-issue --live --labels`, `update-issue --live --labels`, and `add-label --live --label` behavior. Existing command parsing already collects comma-separated labels, but the live write path currently preserves GitHub-like `[]string` request labels and has an add-label provider call that maps to the old issue-label route. Change this component so CLI label mutations are represented as GitCode issue update/create label intent, with add-label either merged into an issue update payload or rejected with the designed unsupported capability diagnostic.
Existing Behavior / Reuse: Reuse the existing CLI command registry, write option validation, `writeRequest` label parsing, `WriteCommandRequest`, idempotency-key handling, audit/cache write confirmation flow, and `CreateIssue`/`UpdateIssue` service dispatch. Replace the existing live add-label execution that calls a dedicated provider `AddLabel` route, and replace the live issue mutation request shape that serializes labels as a JSON array. Confirmed existing source concepts include `create-issue`, `update-issue`, `add-label`, `Labels []string`, and an old add-label endpoint builder targeting `/api/v5/repos/{owner}/{repo}/issues/{number}/labels`.
Detailed Design: Add a component-local label payload conversion boundary between parsed CLI options and live provider issue mutation payloads: keep `WriteCommandRequest.Labels` as the parsed CLI intent, but require the live GitCode issue mutation DTO used by `CreateIssue` and `UpdateIssue` to expose `labels` as a string containing a JSON-encoded array when non-empty. The conversion algorithm trims labels, drops empty entries, preserves CLI order, JSON-encodes the resulting string slice, and sets the outgoing `labels` field to that encoded string; if there are no labels, the field is omitted. Change add-label live handling so it no longer calls a dedicated `AddLabel` provider operation: for a valid `--number` and `--label`, build an issue update label intent using the requested label and dispatch through the same issue update mutation path, or return the architecture’s unsupported capability/write diagnostic if current-label merge semantics cannot be safely derived from cache. Enforce the decommission invariants by deleting or disabling product runtime use of the old add-label endpoint builder/provider call from CLI write execution and by adding request-capture tests that fail when outgoing issue create/update JSON contains `labels` as an array or when add-label succeeds by calling `/issues/{number}/labels`. Preserve dry-run behavior by validating inputs and returning the existing dry-run result without invoking the provider.
Acceptance Criteria: Operator runs `gitcode-mcp create-issue --live --repo fixture-a --title t --labels bug,enhancement` against a stubbed GitCode issue route; the target product surface `POST /api/v5/repos/{owner}/{repo}/issues` receives JSON where `labels` is the string `["bug","enhancement"]`, the CLI reports a succeeded write, and a stubbed-external-provider CLI/server test proves the payload shape. Operator runs `gitcode-mcp update-issue --live --repo fixture-a --number 42 --labels bug` against a stubbed GitCode issue route; the target route `/api/v5/repos/{owner}/{repo}/issues/42` receives string-encoded labels and the refreshed cache record contains the returned issue labels. Operator runs `gitcode-mcp add-label --live --repo fixture-a --number 42 --label bug`; the command either succeeds through the selected issue-update route or returns the designed unsupported diagnostic, and executable tests fail if the old `/api/v5/repos/{owner}/{repo}/issues/{number}/labels` route is used as a successful product mutation. Developer runs `go test ./...` and `git diff --check`; tests pass offline without credentials, network, SSH agent, or Keychain, including regression cases for JSON-array label payloads and stale add-label routes.
Workload: 1.5 MM

## Cross-Cutting Constraints
- CLI live writes remain credential-gated and idempotent — label mutation changes must reuse the existing live/dry-run and idempotency command contract so CLI remains the supported mutation surface while MCP stays read-oriented
- GitCode label payloads use JSON-string encoding — issue create/update cannot serialize labels as JSON arrays in live product execution
- Old issue add-label route cannot be a successful product path — add-label must route through issue update behavior or return the designed diagnostic
- Offline validation is mandatory — all behavior must be proven with mocked GitCode routes under `go test ./...` and `git diff --check` without external credentials or network

## Data And Control Flow
- CLI parses `--labels A,B` or `--label L` — `writeRequest` creates label intent — service live write dispatch converts label intent into GitCode issue mutation payload before provider call
- `create-issue --live` — issue create route receives title/body plus optional JSON-string `labels` field — returned issue/label data refreshes cache through existing write confirmation flow
- `update-issue --live` — issue update route receives optional JSON-string `labels` field — returned issue state refreshes cache through existing write confirmation flow
- `add-label --live` — command validates repo, number, and label — execution uses issue update behavior or returns unsupported diagnostic without calling the decommissioned add-label route

## Component Interactions
- `CLI label mutation commands` -> `service write dispatch` — passes parsed label intent through `WriteCommandRequest` while preserving dry-run/live validation and idempotency metadata
- `service write dispatch` -> `GitCode live adapter` — sends issue create/update requests whose live DTO encodes `labels` as a JSON string when labels are present
- `GitCode live adapter` -> `label_normalizer` — returned GitCode label objects are normalized by adapter-owned label normalization before cache refresh, while CLI remains responsible for the outgoing mutation intent
- `CLI label mutation commands` -> `test_suite` — stubbed CLI/provider tests assert payload shape, route selection, diagnostics, and offline validation gates

## Rationale
The component is affected because the configured Component Impact delta specifically owns CLI issue create/update/add-label label mutation behavior. Existing code already has CLI write commands and live write plumbing, but the observed source anchors show missing GitCode-specific behavior: labels still flow as arrays and add-label still has a dedicated old route path.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0225-run_attempt-1/final_message.txt`
