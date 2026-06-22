# Design Package Component: fixture-provider

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Fixture Provider

## Summary
The fixture-provider remains the deterministic offline data source for ordinary non-live commands. Its component-local change is to make the fixture boundary explicit enough that live routes cannot consume fixture records or fixture read-only write behavior as successful live behavior.

## Top-Level Alignment
This component implements the approved architecture’s offline fixture-provider boundary. It supports default `sync` while giving `cli-startup`, `sync-service`, `write-service`, and integration tests a clear way to detect fixture fallback on explicit live routes.

## Tasks

### Task 1: Guard fixture boundary
Outcome IDs: outcome-1, outcome-6, outcome-7
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-4
Change Type: change
Description: The fixture-provider currently owns deterministic offline issue/wiki data and read-only write responses. This task keeps those behaviors available only as offline fixture behavior and makes accidental live-route consumption detectable by the rest of the runtime. The local fixture entities must expose stable fixture identity and typed read-only failure semantics without changing live-provider ownership.
Existing Behavior / Reuse: Reuse the existing `fixtureProvider`/`NewFixtureProvider` fixture API behavior, the service-level `sanitizedFixtureClient`, deterministic issue `ISSUE-42`, wiki `WIKI-HOME`, and read-only fixture write responses. Existing source inspection confirms the component already has fixture data and read-only write errors, but it does not provide a single explicit fixture-boundary contract that live routes can reject uniformly.
Detailed Design: Add a small fixture-boundary contract owned by this component, implemented by both `fixtureProvider` and `sanitizedFixtureClient`, that exposes provider mode `offline-fixture`/`fixture`, deterministic fixture marker IDs, and a typed read-only fixture write classification. Keep read methods deterministic and unchanged for offline sync: issue number 42 maps to `ISSUE-42`, wiki slug `Home` maps to `WIKI-HOME`, comments remain sanitized, and no network-capable state is added. Normalize all fixture write methods such as create issue, update issue, comments, labels, and wiki writes to return the same typed fixture read-only error so callers can distinguish intentional offline fixture write rejection from live provider failures. Delete no fixture data; instead, explicitly keep it internal to fixture-mode reads and expose only non-secret marker metadata for fallback detection. The negative invariant is that any live-mode caller receiving fixture marker IDs or the typed fixture read-only error must be able to classify it as `fixture_fallback_detected`, while default offline callers continue to receive existing fixture-backed records and read-only write behavior.
Acceptance Criteria: Operator runs `gitcode-mcp sync` without `--live` against the product CLI with a configured repository and an available mock server; the sync product route completes through fixture-backed offline behavior, cache contains the expected fixture records, and the mock server request count remains zero in a Go CLI integration test. Operator runs `gitcode-mcp sync --live` through the product CLI against a stubbed external GitCode HTTP server; if fixture IDs `ISSUE-42` or `WIKI-HOME` are produced by this component on the live route, the executable integration test fails with fixture fallback detection. Operator runs `gitcode-mcp create-issue --live` through the product CLI; if the fixture-provider read-only write error reaches the live product surface as the command result, the executable integration test fails and classifies the condition as fixture fallback rather than successful live behavior.
Workload: 0.5 MM

## Cross-Cutting Constraints
- Fixture data remains public-safe and sanitized — the component owns deterministic offline records used by tests and development.
- Explicit live mode is fail-closed — fixture-provider outputs must be detectable as invalid on live routes rather than accepted as live success.
- Default non-live behavior remains network-free — ordinary sync keeps fixture-provider semantics and zero HTTP interaction.

## Data And Control Flow
- Non-live sync selects fixture-provider — `cli-startup` and `sync-service` call fixture reads, then `cache-runtime` stores deterministic fixture records.
- Live sync must not select fixture-provider — if fixture marker IDs appear after a live command, downstream validation treats them as fallback detection.
- Live create issue must not use fixture writes — the fixture read-only classification is valid only for offline fixture contexts and becomes fallback evidence on live routes.

## Component Interactions
- `fixture-provider` -> `sync-service` — supplies deterministic offline issue/wiki/comment records only for non-live sync.
- `fixture-provider` -> `write-service` — supplies typed fixture read-only errors that live write paths can reject as fixture fallback.
- `fixture-provider` -> `cli-integration-tests` — exposes stable fixture markers `ISSUE-42`, `WIKI-HOME`, and read-only write classification for executable fallback assertions.

## Rationale
The fixture-provider is affected because the architecture preserves it for default offline sync while decommissioning any silent fixture fallback on explicit live routes. A single component-local boundary contract is the smallest change that preserves existing fixture behavior and enables live-route rejection.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0239-run_attempt-1/final_message.txt`
