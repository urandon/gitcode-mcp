# Structured Feedback

`gitcode-mcp` can turn reproducible agent or human dogfood friction into a consistent, public-safe issue. The feature is opt-in and deliberately separates preparation from submission. Report preparation is always available; external submission has an explicit runtime readiness state.

Use feedback for reusable product observations: an MCP action required a CLI, browser, or human fallback; an error was generic or misleading; setup was hidden; retries were required; useful evidence was missing; or a workflow exposed a feature gap. Do not use it for raw prompts, conversation transcripts, credentials, cookies, private repository content, environment dumps, or full API payloads.

## Configure the trusted sink

Inspect readiness before preparing a report:

```sh
gitcode-mcp feedback status --format json
```

The stable states, in blocking precedence order, are `disabled`,
`sink_missing`, `repository_unbound`, `credential_missing`,
`provider_unavailable`, and `ready`. Status is cache-first and side-effect-free:
it does not probe GitCode, start a provider, or write configuration.

For a repository already bound in the selected cache, render and apply the
trusted global setup plan:

```sh
gitcode-mcp feedback setup --repo example-owner/feedback-repo --format json

gitcode-mcp feedback setup \
  --repo example-owner/feedback-repo \
  --yes \
  --plan-id feedback-plan-EXACT_PLAN_ID \
  --idempotency-key feedback-setup-example \
  --format json
```

The first command does not mutate configuration. The second requires the exact
current plan id, claims a bounded durable idempotency receipt using only a hash
of the caller key, updates only the global YAML feedback section through an
atomic private-permission replacement, preserves unrelated YAML and comments,
and verifies the effective policy. It never writes a credential or accepts an
endpoint. Reusing a key for a different sink intent is rejected; retrying the
same intent returns the original receipt without another config write while
that receipt is retained. Terminal receipts have a 90-day retention window and
the journal holds at most 256 total claims. At capacity the oldest terminal
receipt is compacted; pending crash-recovery claims are reconciled against the
current config digest before retention runs and are never discarded blindly.
After a terminal receipt leaves that bounded window, its caller key may be used
as a new operation key, so automation should retry promptly with the same key.

The embedded Admin UI exposes the same readiness contract in **Maintenance →
Feedback delivery**. It can render and confirm the setup plan only for a
repository already bound in the effective cache. This is a trusted local
configuration write, not a feedback submission: it never creates an issue,
accepts an arbitrary destination, or exposes a credential, provider endpoint,
or cache path. If receipt delivery is interrupted, retry the still-open
confirmation to reuse the exact plan and idempotency key.

The same configuration can also be supplied by a trusted installer or bundle:

Add this to the global config:

```yaml
feedback:
  enabled: true
  sink: gitcode_issues
  repo_id: example-owner/feedback-repo
  labels: feedback|dogfood
  duplicate_policy: suggest
```

The sink repository is configuration-owned. `prepare_feedback`, `submit_feedback`, and the CLI do not accept a destination URL or repository override. The token still comes from the normal environment/keyring credential flow.

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
