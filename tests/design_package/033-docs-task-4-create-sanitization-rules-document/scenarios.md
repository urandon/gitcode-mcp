# Validation Scenarios: 033-docs-task-4-create-sanitization-rules-document

## 033-docs-task-4-create-sanitization-rules-document-scenario-1

AC-1 documentation/runtime scenario: read `docs/sanitization.md` section 2 (`## 2. Redacted Surface Types`) and execute `gitcode-mcp auth status` with `GITCODE_TOKEN` set in an isolated environment. The scenario verifies the document describes token redaction and the runtime output reports a redacted token field without printing the raw token.

## 033-docs-task-4-create-sanitization-rules-document-scenario-2

AC-1 redaction scenario: the configured token value must appear as `[REDACTED]` or an allowed partial preview format, never as the full token string. The scenario uses a deterministic fake token value and fails if stdout contains that full value.

## 033-docs-task-4-create-sanitization-rules-document-scenario-3

AC-1/AC-2 evidence scenario: execute `auth status` with a token set and compare stdout against the section 2 token rule; then prepare an offline bound repository and execute `doctor` to validate the documented private-coordinate redaction rule.

## 033-docs-task-4-create-sanitization-rules-document-scenario-4

AC-2 doctor/runtime scenario: read section 2's private repository coordinates entry, bind an offline repository using sanitized placeholder coordinates, and run `gitcode-mcp doctor --repo <id>`. The scenario verifies stdout contains `[REDACTED]` for owner/repo fields and does not contain the underlying owner or repo values.

## 033-docs-task-4-create-sanitization-rules-document-scenario-5

AC-3 fixture scenario: read section 4 (`## 4. Surface-Specific Rules`) and scan fixture-like project files for real token/header patterns. The scenario fails on token-like strings such as `glpat-`, `GITCODE-PAT-`, raw `Authorization: Bearer`, or raw cookie headers outside known validation/test code surfaces.

## 033-docs-task-4-create-sanitization-rules-document-scenario-6

AC-4 placeholder consistency scenario: read section 3 (`## 3. Safe Replacement Patterns`) and verify documented placeholders (`YOUR_OWNER`, `YOUR_REPO`, `$GITCODE_TOKEN`, `[REDACTED]`) are present in `docs/sanitization.md` and are used consistently in `docs/live-readiness.md` and related documentation without substituting real private coordinates or tokens.
