# Validation Scenarios: 029 internal diagnostics redaction filter

## 029-internal-diagnostics-task-1-add-redaction-filter-for-log-print-output-intercep-scenario-1

All diagnostics (auth status, doctor, error messages, e2e test output) contain no raw tokens, private URLs, Authorization headers, or raw API response bodies.

Concrete offline validation:
- Run the production diagnostics package tests that exercise text, byte, header, URL, JSON body, and writer interception redaction.
- Run production CLI tests for `auth status`, `doctor`, runtime audit, and error redaction surfaces.
- Run the e2e package test binary with `-tags=e2e` and no live environment so the real e2e test path compiles and skips cleanly without network access while preserving the shared test logger redaction path.
- Build the real `gitcode-mcp` binary and run `auth status`, `doctor --format json`, and a failing live `sync --live` with local fake token/owner/repo/base URL values; validate combined stdout/stderr excludes raw token, private coordinates, Authorization header values, cookies, private URL host, and raw API response body text.

## 029-internal-diagnostics-task-1-add-redaction-filter-for-log-print-output-intercep-scenario-2

Token values appear only as [REDACTED].

Concrete offline validation:
- Use deterministic fake secrets only: a fake GitCode token, private owner, private repo, private base URL, Authorization header value, Cookie value, and raw API response sample text.
- Validate real command output and redaction test output contain `[REDACTED]` where sensitive token/header/body values would otherwise appear.
- Fail if the fake token appears anywhere in runtime outputs or if redaction removes the marker entirely.
