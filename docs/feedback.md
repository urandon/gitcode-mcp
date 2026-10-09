# Structured Feedback

`gitcode-mcp` can turn reproducible agent or human dogfood friction into a consistent, public-safe issue. The feature is opt-in and deliberately separates preparation from submission. Report preparation is always available; external submission has an explicit runtime readiness state.

Use feedback for reusable product observations: an MCP action required a CLI, browser, or human fallback; an error was generic or misleading; setup was hidden; retries were required; useful evidence was missing; or a workflow exposed a feature gap. Do not use it for raw prompts, conversation transcripts, credentials, cookies, private repository content, environment dumps, or full API payloads.

The intake is a worksheet, not a one-line complaint. A useful report states the
goal, concrete circumstances, observed and expected behavior, impact, shortest
reproduction, fallback used (or that none was available), and an observable
acceptance signal. `prepare_feedback` does not invent missing details: it
returns `status=needs_context`, stable `missing_fields`, and targeted
`follow_up_questions`. `submit_feedback` treats that state as a hard no-write
boundary.

## Build-owned destination

Official builds send feedback to `urandon/gitcode-mcp`. This repository is
product identity, not an operator-selected destination. Downstream distributions
can override `gitcode-mcp/internal/buildinfo.FeedbackRepository` with Go linker
metadata (`-ldflags '-X gitcode-mcp/internal/buildinfo.FeedbackRepository=example/distribution'`).
The release builder accepts `RELEASE_FEEDBACK_REPOSITORY` and includes the selected
identity in the archive README. No runtime environment variable retargets feedback.

Only enablement and duplicate handling are runtime policy:

```yaml
feedback:
  enabled: true
  duplicate_policy: suggest
```

Submission is opt-in (disabled by default); enabling it never submits automatically.
The bound repository, credential, explicit live intent, idempotency, redaction,
audit, provider confirmation and sanitized readback gates still apply.

Inspect side-effect-free readiness with `gitcode-mcp feedback status --format json`.
Blocking precedence is `configuration_conflict`, `disabled`,
`repository_unbound`, `credential_missing`, `provider_unavailable`, then
`ready`. The legacy `sink_missing` state remains readable for older snapshots.
Preparation is available in every state; no status call probes GitCode or writes config.

Absent legacy fields use the build identity. Matching legacy `repo_id`,
`sink: gitcode_issues` and `labels: feedback|dogfood` are accepted.
Conflicting values (including an explicitly empty sink) block submission with
`configuration_conflict`; their raw values are not returned in readiness.
Review the old intent and remove conflicting legacy fields explicitly before
enabling submission. An upgrade never silently redirects a write.

For compatibility, `gitcode-mcp feedback setup` renders an enablement plan for
the already-bound build destination. Applying it still requires `--yes`, the
exact `--plan-id` and `--idempotency-key`. The optional legacy `--repo`
can only match the build destination. Conflicting configs are rejected without
mutation. New setup writes only enablement and duplicate policy, preserves
unrelated YAML, uses private atomic replacement and bounded durable receipts
(90 days, at most 256 claims). Retained receipts replay without another write;
pending crash claims are reconciled before compaction.

Admin **Maintenance → Feedback delivery** shows the destination and readiness
read-only, with prerequisite handoffs. It has no destination selector, setup
plan, confirmation or report submission control. Deprecated setup backend
calls remain guarded against runtime retargeting for older clients.

## MCP workflow

First call `feedback_status` when submission may be needed. It is read-only and
available in read-only MCP sessions. `tools/list` also annotates
`submit_feedback` with the current state and remediation, so agents can avoid
selecting an unavailable write. Then call `prepare_feedback`; preparation is
also read-only and remains available in every state:

```json
{
  "summary": "Bulk issue sync returned malformed JSON",
  "category": "bug",
  "surface": "sync",
  "reporter_type": "agent",
  "goal": "Refresh cached issues before autonomous backlog triage",
  "circumstances": "During a write-enabled MCP session, after the cached issue head became stale, the agent requested a bounded live issue sync",
  "observed": "sync_live failed with failure_class=partial_response",
  "expected": "The bounded issue collection sync completes",
  "impact": "The agent had to use one exact issue sync",
  "reproduction_steps": [
    "Call sync_live for the issue collection",
    "Observe partial_response before any usable collection result"
  ],
  "fallback_used": "Exact sync with remote_alias=issue:N",
  "tool_name": "sync_live",
  "failure_class": "partial_response",
  "acceptance_signal": "Bulk sync returns a complete result or a bounded typed partial result"
}
```

Preparation validates the shape, redacts secrets, replaces URLs outside approved public GitCode/GitHub hosts, strips URL credentials/query/fragment components and private paths, records sanitized runtime context, renders deterministic Markdown, computes a fingerprint, and checks cached open feedback issues. The result includes the same readiness DTO and returns one of:

- `prepared`: a new report is prepared; inspect its independent readiness DTO before submission;
- `needs_context`: the worksheet is missing actionable circumstances; ask the returned follow-up questions and prepare it again;
- `configuration_required`: useful draft, but the trusted sink policy is disabled or incomplete;
- `duplicate`: exact fingerprint match, so no new issue is needed;
- `duplicate_candidates`: likely matches require review.

`configured` describes the trusted sink policy only; it is not cleared merely
because a credential or live provider is currently unavailable. Duplicate
classification is likewise preserved while submission is unavailable.

After external issue creation is authorized, call `submit_feedback` with the same fields plus:

```json
{
  "write_mode": "live",
  "idempotency_key": "dogfood-sync-partial-20260818"
}
```

The submission re-prepares the report, resolves only the configured sink, creates the issue through the normal audited write lifecycle, performs sanitized cache readback, and returns the issue receipt. Replaying the same idempotency key does not repeat the provider write even when the generated observation timestamp changes. With the default `duplicate_policy: suggest`, pass `duplicate_override: "create"` only after reviewing likely candidates and confirming the report is distinct. `return_existing` instead returns the strongest likely match without writing. An exact fingerprint match is never duplicated.

### Agent worksheet

Before calling `prepare_feedback`, answer these questions with observed facts:

1. `summary`: What searchable symptom or friction occurred?
2. `goal`: What was the user or agent trying to accomplish?
3. `circumstances`: At which workflow stage and in which mode/state did it occur, and what triggered it?
4. `observed`: What exactly happened, including a stable error or state transition when available?
5. `expected`: What observable behavior should have happened instead?
6. `impact`: What was blocked, delayed, repeated, or handed to a human?
7. `reproduction_steps`: What is the shortest deterministic sequence that reproduces it?
8. `fallback_used`: Which fallback was used? If none was available, say that explicitly.
9. `acceptance_signal`: What result would prove the problem is addressed?

Avoid low-information values such as `failed`, `does not work`, `same as
above`, `unknown`, or `TBD`. Optional bounded evidence and an
implementation-neutral proposal can follow, but they do not replace the
worksheet. If a fact is unavailable, leave it absent and ask the reporter the
returned question instead of guessing.

If a non-duplicate report is prepared while runtime submission is unavailable,
the submit call returns `status=submission_unavailable` plus the readiness
remediation and performs no provider write. Cached duplicate receipts remain
available even in that state.

## CLI workflow

The CLI accepts individual flags or a JSON draft. Preparation does not require live mode:

```sh
gitcode-mcp feedback prepare --input feedback.json --format json
```

Submission is explicit:

```sh
gitcode-mcp feedback submit \
  --input feedback.json \
  --live \
  --idempotency-key dogfood-sync-partial-20260818 \
  --format json
```

`feedback submit --dry-run` prepares and validates the report without writing.

## Intake immutability

The generated issue description is the fixed intake report. Add later design, progress, verification, and corrections as issue comments instead of rewriting the original description.
