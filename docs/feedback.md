# Structured Feedback

`gitcode-mcp` can turn reproducible agent or human dogfood friction into a consistent, public-safe issue. The feature is opt-in and deliberately separates preparation from submission.

Use feedback for reusable product observations: an MCP action required a CLI, browser, or human fallback; an error was generic or misleading; setup was hidden; retries were required; useful evidence was missing; or a workflow exposed a feature gap. Do not use it for raw prompts, conversation transcripts, credentials, cookies, private repository content, environment dumps, or full API payloads.

## Configure the trusted sink

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

First call `prepare_feedback`. It is read-only and is available even in read-only MCP sessions:

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

Preparation validates the shape, redacts secrets, strips URL credentials/query/fragment components and private paths, records sanitized runtime context, renders deterministic Markdown, computes a fingerprint, and checks cached open feedback issues. It returns one of:

- `prepared`: ready to submit;
- `configuration_required`: useful draft, but no sink is enabled;
- `duplicate`: exact fingerprint match, so no new issue is needed;
- `duplicate_candidates`: likely matches require review.

After external issue creation is authorized, call `submit_feedback` with the same fields plus:

```json
{
  "write_mode": "live",
  "idempotency_key": "dogfood-sync-partial-20260818"
}
```

The submission re-prepares the report, resolves only the configured sink, creates the issue through the normal audited write lifecycle, performs sanitized cache readback, and returns the issue receipt. Replaying the same idempotency key does not repeat the provider write. Pass `duplicate_override: "create"` only after reviewing likely candidates and confirming the report is distinct; an exact fingerprint match is never duplicated.

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
