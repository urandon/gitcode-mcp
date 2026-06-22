# Dogfood Feedback

## Run Context

- Run ID: example-owner/example-repo
- Date: 2026-06-20
- Branch: feat/dogfood-iteration
- Fixture config: <REDACTED_PATH>
- Cache path: <REDACTED_PATH>

## Completed Checklist Slices

| Slice | Status | Transcript |
|-------|--------|------------|
| day1  | pass   | <REDACTED_PATH>/day1.md |
| day2  | pass   | <REDACTED_PATH>/day2.md |

## Referenced Commands / Transcripts

- docs-smoke transcript: <REDACTED_PATH>/docs-smoke.md
- fixture-validation transcript: <REDACTED_PATH>/fixture-validation.md

## Observed Friction

### Friction #1

- Command / surface: gitcode-mcp repo add --repo ISSUE-42
- Expected: Success with confirmation message
- Actual: Alias collision with existing repo; resolved via --repo-id flag
- Impact: Minor UX confusion, but documented diagnostic matched

## Missing Metadata

- Chunk source ranges sometimes omit line numbers for paragraph-boundary splits.

## MCP Client Setup Friction

- Transport: stdio
- Issue: Client disconnected on first startup due to missing cache
- Resolution / workaround: Run sync before MCP serve, as documented

## Prompt / Config Improvements

- Add cache path validation in init flow to avoid silent no-cache MCP serves.

## Follow-Up Owner

- Component(s): mcp-server
- Suggested task(s): Pre-flight cache check on MCP serve startup
- Priority: medium

## Redaction Checklist

- [x] No bearer tokens or API keys present
- [x] No `Authorization:` headers present
- [x] No cookie or set-cookie header values present
- [x] No token-like high-entropy strings present
- [x] No absolute user paths present
- [x] No private/internal hostnames present
- [x] No raw JSON response blocks lacking `<REDACTED_RESPONSE>` or fixture markers
- [x] No non-allowlisted repo/tracker/wiki identifiers present
