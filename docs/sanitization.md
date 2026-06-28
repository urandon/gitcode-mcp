# Sanitization Rules

Canonical public-safety reference for `gitcode-mcp`. Every output surface, fixture, document, log, issue comment, pull request report, and wiki page must comply.

## 1. Purpose

The public-safety contract: no raw tokens, private URLs, owner/repo names, Authorization headers, or raw API response bodies appear in any output surface.

Affected surfaces:
- CLI stdout and stderr
- MCP tool JSON responses
- Test output (unit, integration, e2e)
- Fixture files
- Logs and diagnostic streams
- Repository documents, issue comments, pull request reports, wiki pages, and commit messages

## 2. Redacted Surface Types

| Surface | What is redacted | Replacement |
|---|---|---|
| Token value (from `GITCODE_TOKEN` env or keychain) | The full raw token string | Token preview: first 3 characters + `***` + last 3 characters (e.g. `glp***xyz`). None if `RedactToken` returns empty. |
| Token value in text bodies | Any raw token string in logs, error messages, or formatted text | `[REDACTED]` via `Filter.RedactText` or `RedactText(...)` |
| Authorization header | `Bearer <token>` and similar auth header values | `[REDACTED]` via `Filter.RedactHeaders` |
| Cookie / Set-Cookie header | Full header values | `[REDACTED]` via `Filter.RedactHeaders` |
| Private repo coordinates in JSON | `owner`, `repo`, `repository` keys in JSON output | `[REDACTED]` via `isJSONSecretKey` in `redactJSONValue` |
| Private repo coordinates in text | Owner and repo names passed as sensitive terms | `[REDACTED]` via string replacement in `Filter.RedactText` |
| Raw API response bodies | Unstructured or structured API response payloads | Summarized as `api_response: status=<text> body=[REDACTED]` via `RawAPIResponseSummary`; or redacted in place via `RedactJSONBody` |
| Internal/unapproved URLs | URLs whose host is not in the approved-host allowlist | Host replaced with `redacted.example.com` via `Filter.RedactURL` |
| URL query secrets | `token`, `key`, `secret`, `password`, `credential` query parameters | Value replaced with `[REDACTED]` via `isSecretKey` |
| JSON secret keys | `authorization`, `cookie`, `set-cookie`, `token`, `secret`, `key`, `password`, `credential`, `api_key`, `access_key`, `private_key` | Value replaced with `[REDACTED]` via `isJSONSecretKey` |

## 3. Safe Replacement Patterns

These placeholder values are used throughout this repository's documentation, fixtures, tests, and example output. Any new artifact must use only these patterns.

| Placeholder | When to use |
|---|---|
| `$GITCODE_TOKEN` | Environment variable reference in documentation — never a real value |
| `YOUR_OWNER` | Owner/namespace placeholder in documented example commands |
| `YOUR_REPO` | Repository name placeholder in documented example commands |
| `[REDACTED]` | Full redaction token used in actual output when a sensitive value is stripped (the `diagnostics.Redacted` constant) |
| `ik-001` (idempotency keys) | Example idempotency key in documentation (not a secret) |
| `/path/to/gitcode-mcp-cache` and `/path/to/gitcode-mcp-config` | Documentation-only filesystem path placeholders |

Token preview format (produced by `ResolvedToken.RedactToken()` in `internal/credential/credential.go`):
- Tokens of length >= 8 runes are displayed as first 3 characters + `***` + last 3 characters (e.g. `glp***xyz`).
- Tokens shorter than 8 runes are displayed as `***`.
- Empty tokens produce an empty string.
- If a custom salt is set, the salt value is used instead of the derived preview.

## 4. Surface-Specific Rules

### CLI output
CLI stdout and stderr that may contain sensitive values must be written through `Filter.RedactedWriter`, `Filter.RedactText`, or equivalent redaction before display. The `doctor` command redacts owner/repo, token values, and raw API responses. The `auth status` command reports token presence, credential source, store mode, available sources, and a `redacted_token` preview when a token is configured — never the raw token string.

### MCP tool responses
JSON response bodies from MCP tools are sanitized: `owner`, `repo`, and secret-key fields are replaced with `[REDACTED]`. No raw API response bodies are embedded.

### E2e test output
Two-cache parity assertions compare digest values without exposing raw content. In mismatch error messages, `title` and `body` fields are replaced with `[REDACTED]`. Redaction filters applied to all test output.

### Fixture files
Fixture files under `internal/`, `testdata/`, and `tests/` must contain no real tokens, no private repository coordinates, no Authorization headers, no cookies, and no raw unsanitized API response bodies. Fixtures captured from live APIs must pass through the redaction filter before being committed.

### Logs and diagnostics
All log output from `internal/diagnostics/` flows through the redaction filter. Audit trail records in `internal/audit/` must not store raw tokens. Error messages must not embed bearer tokens, owner/repo names, or raw API payloads.

### Documentation
All documentation files (`docs/*.md`, `README.md`) use only sanitized placeholders (section 3). Example commands reference `YOUR_OWNER`, `YOUR_REPO`, `$GITCODE_TOKEN`, and `[REDACTED]`. No real private coordinates, tokens, or internal URLs appear in documentation.

### Issues, pull requests, wiki pages, and commit messages
Issue comments, pull request reports, wiki pages, and commit messages must not contain raw tokens, private repository coordinates, Authorization headers, cookies, or internal URLs.

## 5. Verification

Deterministic offline checks:
- `go test ./...` passes with fixture-only providers and no live environment variables.
- Unit tests in `internal/credential/credential_test.go:TestRedactedTokenPreview` verify the token preview format for various input lengths.
- Unit tests in `internal/diagnostics/redaction_test.go` verify that `RedactText`, `RedactHeaders`, `RedactURL`, `RedactJSONBody`, and `RedactedWriter` strip all sensitive terms.
- Unit tests in `internal/doctor/doctor_test.go` verify `[REDACTED]` presence in doctor output.
- Unit tests in `internal/cli/config_commands_test.go` verify `[REDACTED]` in config display output.

Manual verification:
- Run `gitcode-mcp auth status` with a real `GITCODE_TOKEN` environment variable; the output must contain a preview (e.g. `glp***xyz`) or `[REDACTED]` — never the full raw token.
- Run `gitcode-mcp doctor` with a bound repository; the owner and repo fields must show `[REDACTED]`, never the real private coordinates.
- Grep fixture directories for token-like patterns (`glpat-`, `GITCODE-PAT-`, `Bearer `) — expect zero matches.
- Check all `docs/*.md` files for consistent use of `YOUR_OWNER`, `YOUR_REPO`, `$GITCODE_TOKEN`, and `[REDACTED]` placeholders; no real private coordinates or tokens.
