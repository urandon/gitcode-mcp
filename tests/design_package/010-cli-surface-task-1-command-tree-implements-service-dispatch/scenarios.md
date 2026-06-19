# Validation Scenarios

## 010-cli-surface-task-1-command-tree-implements-service-dispatch-scenario-1
A developer trigger runs `gitcode-mcp search "backlog" --format json` against a populated cache through the CLI product surface and receives valid JSON containing at least one result with `id`, `path`, `title`, and `snippet`; executable evidence is `go test ./internal/cli/... -run TestSearchJSON`.

## 010-cli-surface-task-1-command-tree-implements-service-dispatch-scenario-2
A developer trigger runs `gitcode-mcp get DOC-123` through the CLI product surface and stdout contains the record `id`, `path`, `title`, `body`, and `status`; executable evidence is `go test ./internal/cli/... -run TestGetSource`.

## 010-cli-surface-task-1-command-tree-implements-service-dispatch-scenario-3
A developer trigger runs `gitcode-mcp --help` and sees all required command names and aliases registered, including read, sync/index/export, and explicit write commands; executable evidence is `go test ./internal/cli/... -run TestAllCommandsRegistered`.

## 010-cli-surface-task-1-command-tree-implements-service-dispatch-scenario-4
A developer trigger runs the offline shell-replacement walkthrough through CLI routes `ingest`, `search_sources`, `list_sources`, `get_source`, and `source_backlinks`; expected outcome is semantically equivalent cache output without network or shell subprocess use, with executable evidence `go test ./internal/cli/... -run TestMinimumReplacementBar`.
