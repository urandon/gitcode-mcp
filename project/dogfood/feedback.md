# Dogfood Feedback

## Run Context

- Run ID: `<FIXTURE_REPO>`
- Date: `<REDACTED_RESPONSE>`
- Branch: `<FIXTURE_REPO>`
- Fixture config: `<REDACTED_PATH>`
- Cache path: `<REDACTED_PATH>`

## Completed Checklist Slices

| Slice | Status | Transcript |
|-------|--------|------------|
| day1  | `<REDACTED_RESPONSE>` | `<REDACTED_PATH>` |
| day2  | `<REDACTED_RESPONSE>` | `<REDACTED_PATH>` |
| day3  | `<REDACTED_RESPONSE>` | `<REDACTED_PATH>` |
| day4  | `<REDACTED_RESPONSE>` | `<REDACTED_PATH>` |
| day5  | `<REDACTED_RESPONSE>` | `<REDACTED_PATH>` |
| day6  | `<REDACTED_RESPONSE>` | `<REDACTED_PATH>` |
| day7  | `<REDACTED_RESPONSE>` | `<REDACTED_PATH>` |

## Referenced Commands / Transcripts

- docs-smoke transcript: `<REDACTED_PATH>`
- fixture-validation transcript: `<REDACTED_PATH>`
- checklist evidence: `<REDACTED_PATH>`

## Observed Friction

Describe any friction encountered during the dogfood run, e.g. confusing command output, missing documentation, unexpected errors, or CLI/MCP behavior mismatches.

### Friction #1

- Command / surface: `<REDACTED_RESPONSE>`
- Expected: `<REDACTED_RESPONSE>`
- Actual: `<REDACTED_RESPONSE>`
- Impact: `<REDACTED_RESPONSE>`

## Missing Metadata

Note any metadata that would have been useful but was absent. Examples: missing provenance tags, missing sync timestamps, absent chunk references, empty identity_map entries.

- `<REDACTED_RESPONSE>`

## MCP Client Setup Friction

Describe issues encountered while configuring or connecting an MCP client (e.g. Claude Desktop, Codex, etc.) to the stdio or HTTP/SSE server.

- Transport: `<REDACTED_RESPONSE>`
- Issue: `<REDACTED_RESPONSE>`
- Resolution / workaround: `<REDACTED_RESPONSE>`

## Prompt / Config Improvements

Suggestions for improving design-agent prompts, coordinator configs, or task-generation templates. Focus on patterns that would reduce friction in follow-up runs.

- `<REDACTED_RESPONSE>`

## Follow-Up Owner

- Component(s): `<REDACTED_RESPONSE>`
- Suggested task(s): `<REDACTED_RESPONSE>`
- Priority: `<REDACTED_RESPONSE>`

## Redaction Checklist

- [ ] No bearer tokens or API keys present
- [ ] No `Authorization:` headers present
- [ ] No `Cookie:` / `Set-Cookie:` headers present
- [ ] No token-like high-entropy strings present
- [ ] No absolute user paths present (e.g. `/Users/<name>/`, `/home/<name>/`, Windows profile paths)
- [ ] No private/internal hostnames present
- [ ] No raw JSON response blocks lacking `<REDACTED_RESPONSE>` or fixture markers
- [ ] No non-allowlisted repo/tracker/wiki identifiers present
