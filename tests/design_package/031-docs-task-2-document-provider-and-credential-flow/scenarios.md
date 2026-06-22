# Validation Scenarios: 031 docs provider and credential flow

This package validates `docs/architecture.md` against the real local `gitcode-mcp` CLI and offline test behavior. It is deterministic and offline: it builds the production CLI, uses isolated temporary paths, unsets live credential variables, and performs no live network or device checks.

## 031-docs-task-2-document-provider-and-credential-flow-scenario-1

AC-1 trigger: a reviewer reads `docs/architecture.md` Provider Selection section and traces the three documented provider modes to local CLI behavior by running `gitcode-mcp sync --help`.

Expected product behavior:

- `docs/architecture.md` contains a `## Provider Selection` section.
- The section documents all three provider modes: `fixture`, `live`, and `unavailable`.
- The section documents that provider mode is resolved once at command start.
- The section documents that `fixture` is the default when `--live` is absent.

## 031-docs-task-2-document-provider-and-credential-flow-scenario-2

The `sync --help` text references the `--live` flag matching the documented predicate.

Expected product behavior:

- `gitcode-mcp sync --help` exits 0.
- Help output includes a `--live` flag.
- The help text describes `--live` as selecting live GitCode/provider sync rather than fixture/default sync.

## 031-docs-task-2-document-provider-and-credential-flow-scenario-3

Evidence: local command trigger is `sync --help` compared to `docs/architecture.md`; target surfaces are doc text and help output.

Expected product behavior:

- Architecture predicate says `--live` plus credentials selects `live`.
- Architecture predicate says `--live` plus no credentials selects `unavailable`.
- Architecture predicate says no `--live` selects `fixture`.
- Runtime sync help exposes the same `--live` switch needed to reach the documented predicate.

## 031-docs-task-2-document-provider-and-credential-flow-scenario-4

AC-2 trigger: a reviewer reads the Credential Pipeline section's priority chain and compares it to `gitcode-mcp auth status --help`.

Expected product behavior:

- `docs/architecture.md` contains a `## Credential Pipeline` section.
- The section documents priority order: `GITCODE_TOKEN` environment variable, keychain source, none.
- `gitcode-mcp auth status --help` exits 0.

## 031-docs-task-2-document-provider-and-credential-flow-scenario-5

The `auth status --help` text lists env and keychain as available sources and omits secrets.

Expected product behavior:

- `auth status --help` includes `env` / `GITCODE_TOKEN` as a credential source.
- `auth status --help` includes `keychain` as a credential source.
- The source order in help matches the documented chain: env before keychain.
- Help output does not contain a deterministic fake token value.

## 031-docs-task-2-document-provider-and-credential-flow-scenario-6

AC-3 trigger: an operator runs `go test ./...` with no `GITCODE_TOKEN` set.

Expected product behavior:

- All tests pass.
- The test command succeeds with live credential variables unset.

## 031-docs-task-2-document-provider-and-credential-flow-scenario-7

Evidence: local command trigger is `go test ./...` without live environment variables; target surface is test output.

Expected product behavior:

- `go test ./...` exits 0 offline.
- The architecture documentation's statement that fixture mode is the default for tests is supported by passing tests without credentials.
