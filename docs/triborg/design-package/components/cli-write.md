# Design Package Component: cli-write

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: CLI Write Surface

## Summary
The CLI write surface is detailed because `cli-write-delta-1` requires replacing scaffolded or fake-success write behavior with explicit, repo-scoped, audited dry-run/live GitCode writes. The scope is limited to issue create/update, issue comment add, and wiki page create/update.

## Top-Level Alignment
`cli-write` owns the user-facing write command contract and orchestration for Task 9. It coordinates with `repo_binding`, `config_credential`, `gitcode_adapter`, and `cache_sync`, but does not own repository lookup, token sourcing, adapter HTTP behavior, cache schema, or audit storage implementation.

## Tasks

### Task 1: Gate WriteExecutor Live Writes
Outcome IDs: outcome-9
Outcome Role: primary_product
Decommission IDs: decommission-2
Change Type: add
Description: Replace the current CLI write product path with a `WriteExecutor` that requires configured `repo_id`, exactly one of `--dry-run` or `--live`, and adapter-confirmed execution for live writes. The owned live write surface covers only issue create, issue update/body update, issue comment add, wiki page create, and wiki page update. Label mutation is not part of `cli-write-delta-1`; any existing `add-label` command must not be converted into a live product write and must not return fake success.
Existing Behavior / Reuse: Reuse existing write command concepts such as `WriteCommandRequest`, `WriteCommandResult`, write dispatch, and adapter write request/result types where present. Replace synthetic `queued` or scaffold success behavior in the product runtime with dry-run validation, live adapter mutation, audit handoff, and cache refresh handoff. Existing `add-label` behavior is left unsupported/deferred for live product use; if still exposed, it returns a typed unsupported error rather than calling `AddLabel` or rendering success.
Detailed Design: Add or change `WriteCommandRequest` so it is built from mandatory `--repo <repo_id>` plus command payload and `WriteMode` values `dry_run` or `live`; the parser rejects missing repo, raw owner/name/API-base write scope, missing mode, both modes, unsupported command, and command-specific invalid payloads before dispatch. `cli-write` calls `repo_binding` to resolve `repo_id` into owner, repository name, API base URL, enabled scopes, and display metadata, and adapter inputs are built only from that resolved binding. Add a `WriteExecutor` flow for `CreateIssue`, `UpdateIssue`, `AddIssueComment`, `CreateWikiPage`, and `UpdateWikiPage`: normalize payload, validate target scope, compute or accept an idempotency key/source fingerprint from command, `repo_id`, target identity, and payload hash, then branch by mode. In `dry_run`, return `WriteCommandResult{status: dry_run_valid, repo_id, command, idempotency_key, generated_at}` and do not call the adapter, write `audit_trail`, or refresh cache. In `live`, require token availability through `config_credential`, check any completed `audit_trail` entry for the same `repo_id` and idempotency key, call only the matching `gitcode_adapter` method when no completed replay exists, and require adapter-confirmed remote identity before any success path. After adapter confirmation, invoke `cache_sync.RecordAuditEvent` for an `AuditEvent` distinct from `sync_events`, with fields including `repo_id`, `command`, `target_type`, `target_remote_id`, `idempotency_key`, `source_fingerprint`, `adapter_operation_id` when available, `remote_revision`, `status`, `started_at`, `completed_at`, and redacted error metadata; then request affected-record cache refresh/upsert. If adapter confirmation fails, return the mapped typed error and write no success audit row. If remote mutation is confirmed but audit persistence fails, attempt a best-effort `AuditEvent` with failed/pending status; if no durable audit is possible, return a typed `write_partial_remote_confirmed_audit_failed` error containing only public-safe remote identity, `repo_id`, and idempotency key, and never render `succeeded`. If audit succeeds but cache refresh fails, persist or update the audit status as `remote_confirmed_cache_refresh_failed`, return typed `write_partial_cache_refresh_failed`, and on retry use the same idempotency key to refresh cache without issuing a second remote mutation when a durable audit row already records adapter confirmation. Duplicate idempotency/source fingerprint replay first consults `audit_trail`; a completed success returns the prior confirmed result with replay metadata and no adapter call, while partial rows drive reconciliation or typed conflict rather than fake success.
Acceptance Criteria: A developer runs `gitcode-mcp create-issue --repo <repo_id> --title "T" --body "B" --dry-run --format json`; the CLI returns `dry_run_valid` with `repo_id` and an idempotency key, the stubbed external provider call count is zero, and a temporary cache contains no new `audit_trail` or refreshed record, proven by a CLI/API test. A developer runs `gitcode-mcp create-issue --repo <repo_id> --title "T" --body "B" --live --format json` with a successful stub provider and configured token; the CLI returns `succeeded`, includes adapter-confirmed remote identity, writes an `AuditEvent` into `audit_trail`, and refreshes/upserts cache state, proven by a stubbed-external-provider test inspecting the temporary cache. A developer runs live issue update, issue comment add, wiki create, and wiki update commands with missing `--repo`, raw owner/repo-only scope, missing mode, or both modes; the CLI exits nonzero with validation errors before adapter invocation, proven by command tests over each supported write command. A developer runs any live write without a configured token; the CLI exits nonzero with a typed credential error before network/provider invocation, and no `succeeded` output or audit-success row exists, proven by a command test with mocked credential lookup. A developer runs a live write against unavailable, conflict-injecting, unauthorized, rate-limited, or network-failing stub providers; the CLI exits nonzero with actionable typed errors, no success result, and no successful audit row, proven by stubbed-external-provider tests. A system test injects adapter-confirmed remote mutation followed by `RecordAuditEvent` failure; the CLI returns `write_partial_remote_confirmed_audit_failed`, includes redacted remote identity and idempotency key, does not render `succeeded`, and a retry with the same key follows the reconciliation/idempotency path, proven by a temporary-cache test. A system test injects adapter-confirmed remote mutation and successful audit persistence followed by cache refresh failure; the CLI returns `write_partial_cache_refresh_failed`, records audit status for the partial cache-refresh state, does not render `succeeded`, and a retry refreshes cache without a second adapter mutation, proven by adapter call counts and cache inspection. A developer repeats a successful live command with the same idempotency key/source fingerprint; the CLI returns deterministic replay metadata from `audit_trail`, does not call the adapter again, and preserves the prior cache/audit state, proven by a duplicate-replay CLI/API test. A developer invokes existing `add-label --live` if the command is still present; the CLI returns a typed unsupported/deferred error and does not call `AddLabel`, proven by a command test.
Workload: 2.5 MM

## Cross-Cutting Constraints
- `repo_id` is the only accepted write scope — preserves the approved repository binding model and prevents raw owner/repo bypass.
- Live mutation requires explicit `--live` and configured credentials — keeps routine use safe and prevents accidental network writes.
- Dry-run and live share validation and idempotency fingerprinting — makes dry-run a faithful preview of the live payload without mutation.
- Success requires adapter confirmation plus durable audit/cache handoff — enforces the no fake-success decommission invariant.
- `AuditEvent`/`audit_trail` is distinct from `sync_events` — separates user write accountability from sync/bootstrap history.

## Data And Control Flow
- User invokes supported write command — CLI parser — rejects unsupported command, missing `--repo`, invalid mode flags, and invalid payload before service dispatch.
- CLI builds `WriteCommandRequest` — `repo_binding` — resolves `repo_id` to configured repository metadata before any adapter input is constructed.
- `WriteExecutor` normalizes payload — idempotency logic — computes or validates source fingerprint and checks durable replay state before live mutation.
- Dry-run branch — CLI renderer — returns `dry_run_valid` and admits no adapter call, audit row, or cache refresh.
- Live branch — credential lookup, adapter, audit, cache refresh — renders `succeeded` only after adapter confirmation, `AuditEvent` persistence, and affected-record cache refresh complete.
- Partial-success branch — CLI renderer and retry path — returns typed partial errors, exposes idempotency key for reconciliation, and never converts partial remote confirmation into `succeeded`.

## Component Interactions
- `cli-write` -> `repo_binding` — resolves mandatory `repo_id` into configured owner/name/API base and enabled scopes; raw owner/repo write targeting is rejected before adapter construction.
- `cli-write` -> `config_credential` — checks live-mode token availability and returns typed missing-credential errors before provider/network invocation.
- `cli-write` -> `gitcode_adapter` — invokes only `CreateIssue`, `UpdateIssue`, `CreateIssueComment`, `CreateWikiPage`, and `UpdateWikiPage` in live mode; adapter confirmation is required before audit/cache handoff.
- `cli-write` -> `cache_sync` — invokes `RecordAuditEvent`/`audit_trail` persistence and affected-record refresh/upsert; `cli-write` depends on but does not own cache schema, WAL behavior, or storage implementation.
- `cli-write` -> `CLI renderer` — maps validation, credential, adapter, conflict, busy, partial-audit, and partial-cache errors to nonzero exits without printing token values, request bodies, or `succeeded` for failed paths.

## Rationale
The approved architecture materially affects `cli-write` because Task 9 requires replacing fake-success write behavior with explicit dry-run/live writes, adapter-confirmed mutation, audit persistence, cache refresh, idempotency, and typed errors. The component impact has one detailed delta, so one component-local implementation task covers the required product change and its executable evidence.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0222-run_attempt-1/final_message.txt`
