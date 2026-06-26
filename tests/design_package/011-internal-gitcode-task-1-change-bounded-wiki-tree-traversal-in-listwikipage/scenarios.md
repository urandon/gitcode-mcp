# Design Package Validation Scenarios: Task 011

## Scope

Task: `011-internal-gitcode-task-1-change-bounded-wiki-tree-traversal-in-listwikipage`

This validation exercises offline Go adapter tests for bounded wiki tree traversal in `internal/gitcode`, using mocked HTTP servers as external-provider mocks, plus Go service-level tests for the integration tier with in-memory cache.

## Scenarios

### 011-internal-gitcode-task-1-change-bounded-wiki-tree-traversal-in-listwikipage-scenario-1

Wiki sync with recursive tree provider, context cancelled mid-traversal: traversal stops within current directory level.

Executable coverage:

- `go test ./internal/gitcode -run '^TestBoundedWikiTreeTraversalCancelMidTraversal$' -count=1`

### 011-internal-gitcode-task-1-change-bounded-wiki-tree-traversal-in-listwikipage-scenario-2

PartialSyncError returned with records committed so far.

Executable coverage:

- `go test ./internal/service -run '^TestBulkSyncWikiBoundedPreCancel$' -count=1`

### 011-internal-gitcode-task-1-change-bounded-wiki-tree-traversal-in-listwikipage-scenario-3

Stack-based walker checks ctx.Done() at each directory entry.

Executable coverage:

- `go test ./internal/gitcode -run '^TestBoundedWikiTreeTraversalCancelMidTraversal$' -count=1`
- The walker iterates a stack of directory entries; ctx.Done() is checked at the top of each iteration at `internal/gitcode/http_client.go:319`.

### 011-internal-gitcode-task-1-change-bounded-wiki-tree-traversal-in-listwikipage-scenario-4

No outer loop wrap pattern exists in code.

Executable coverage:

- `go test ./internal/gitcode -run '^TestBoundedWikiTreeTraversalNoOuterLoopPattern$' -count=1`
- Static verification: `bulkSyncWikiBounded` in `internal/service/service.go` makes a single `ListWikiPages` call; no paginated for loop.
- `grep -r 'for.*ListWikiPages' internal/service/ --include='*.go' || true` confirms no outer loop wrapper.

## Offline Determinism

The validation uses Go tests with mocked HTTP servers (for gitcode adapter) and in-memory SQLite stores with fake GitCode clients (for service tier). It does not perform live network, external-provider, credential, or device access.
