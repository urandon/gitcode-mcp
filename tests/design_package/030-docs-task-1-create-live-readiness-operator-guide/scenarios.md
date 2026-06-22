# Validation Scenarios: 030 docs live-readiness operator guide

This package validates `docs/live-readiness.md` against the real local `gitcode-mcp` CLI. It is deterministic and offline: it builds the production CLI, uses isolated temporary config/cache directories, unsets `GITCODE_TOKEN` where required, and performs no live network or device checks.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-1

AC-1 trigger: an operator follows `docs/live-readiness.md` steps 2 and 3 in sequence by running:

1. `gitcode-mcp bind --help`
2. `gitcode-mcp auth status --help`

Expected product behavior:

- `bind --help` exits 0.
- `bind --help` documents the repository binding flags from guide section 2, including `--repo-owner` and `--repo`.
- `auth status --help` exits 0.
- `auth status --help` documents `--live`, `--owner`, `--repo`, and `--format` with descriptions matching guide section 3.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-2

Each help output from scenario 1 must match the documented flags and descriptions in `docs/live-readiness.md` sections 2 and 3. The validation compares concrete command text and flag/description substrings from the runtime help output to the guide text.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-3

AC-2 trigger: run `gitcode-mcp auth status` with `GITCODE_TOKEN` unset and isolated config/cache directories.

Expected product behavior:

- output includes a no-token state;
- output lists available token sources in the documented pipeline order: env, keychain, none;
- output does not contain a deterministic fake secret value.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-4

The no-token auth output lists available token sources without revealing secrets and matches the documented credential pipeline order in guide section 3.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-5

Evidence for AC-2 is local stdout from `auth status` without a token, checked for source ordering and no secret leakage.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-6

AC-3 trigger: run `gitcode-mcp sync --help`.

Expected product behavior:

- `sync --help` exits 0;
- `sync --help` shows a `--live` flag;
- the `--live` description matches guide section 4's live sync behavior.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-7

Evidence for AC-3 is local `sync --help` stdout checked for `--live` and its documented behavior.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-8

AC-4 trigger: run `gitcode-mcp create-issue --help`.

Expected product behavior:

- output exits 0;
- output documents `--live`, `--dry-run`, `--idempotency-key`, `--title`, and `--body`;
- descriptions match guide section 5, including live execution, dry-run validation without mutation, idempotency key behavior, issue title, and issue body.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-9

Evidence for AC-4 is local `create-issue --help` stdout checked for all documented flags and descriptions.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-10

AC-5 trigger: run `gitcode-mcp doctor` against an isolated fixture/default cache and config with no repository binding.

Expected product behavior:

- output exits 0;
- report includes a no-repo-bound diagnostic;
- report suggests the `bind` command, matching guide section 8.

## 030-docs-task-1-create-live-readiness-operator-guide-scenario-11

Evidence for AC-5 is local `doctor` stdout checked for the no-repo-bound diagnostic and `bind` command suggestion.
