# Admin UI release gates

The embedded admin UI ships as part of the Go binary. These gates keep that convenience from quietly adding an unbounded web runtime, dependency surface, or daemon footprint.

## Automated gates

Run from the repository root:

```sh
go test ./...
go vet ./...
go test -race ./internal/adminhttp ./internal/servicectl ./internal/cache ./internal/rag ./internal/service
cd web
npm run licenses
npm run check
npm test
npm run test:e2e
cd ..
./scripts/check-admin-ui-assets.sh
git diff --check
```

`npm run licenses` inspects every package locked in `web/package-lock.json`, including build/test-only dependencies. The accepted set is MIT, ISC, Apache-2.0, and BSD-3-Clause. A missing or newly introduced license fails closed. Human-readable scope notes live in `web/THIRD_PARTY_NOTICES.md`.

The Playwright matrix covers authenticated loading, URL-preserved navigation and filters, Light/Dark/System themes, narrow layout, keyboard focus, reduced motion, observation-only Search Lab comparison, repository-document exact-generation policy/revision/coverage, typed exclusions and plan preview, pathless registered-source reconciliation, scoped index-job navigation, exact-revision Git search and CLI handoffs, typed fallback, citations and score explanation, provider smoke, bounded repair confirmation, and idempotent interrupted retry. Repository documentation has committed visual-regression baselines for disabled, unavailable, empty, partial, building, blocked, superseded, stale, registered-without-ready-set, and ready states. The registered-without-ready-set fixture also submits an Admin full-text request to prove that lexical Git search is enabled independently of semantic readiness. One live-environment smoke remains opt-in because it requires a running daemon and local browser session.

## Resource budgets

The comparison baseline is the stripped macOS arm64 `v0.1.9` binary, the last tagged release before the admin epic. Build both binaries with the same toolchain and flags:

```sh
go build -trimpath -ldflags="-s -w" -o /tmp/gitcode-mcp-current ./cmd/gitcode-mcp
```

| Gate | Budget | Epic completion measurement |
| --- | ---: | ---: |
| Stripped binary increase | at most 1 MiB | 654,880 bytes (13,610,802 → 14,265,682; 4.81%) |
| Embedded asset directory | at most 512 KiB | 280 KiB |
| Warm `--help` median regression | at most 2 ms | 0.259 ms (6.257 → 6.516 ms) |
| Admin-enabled idle RSS increase | at most 6 MiB | 2,640 KiB (17,632 → 20,272 KiB) |

Numbers are evidence for this toolchain and host, not cross-platform guarantees. A release fails the gate when the comparable local delta exceeds its budget; update a budget only with an issue comment explaining the changed architecture and measurement.

Startup uses 50 measured invocations after one warm-up for each stripped binary, sorted by elapsed wall time; the middle sample is the median. RSS uses separate, clean temporary `HOME` and `GITCODE_MCP_SERVICE_RUNTIME_DIR` directories, waits at least 20 seconds, then compares `ps -o rss` for baseline `service run` and current `service run --admin`. The clean home is essential: otherwise current maintenance registrations and caches become part of the measurement.

## Manual release checklist

- Compare implementation and selected visual reference at the same desktop viewport; repeat at 390 px width.
- Exercise Light, Dark, and System (default) and verify readable contrast in status, score, error, warning, and confirmation states.
- Traverse primary navigation, repository tabs, Search Lab inputs/results, provider actions, and confirmation dialogs by keyboard; focus must remain visible and return to the trigger.
- Verify reduced-motion mode removes non-essential transitions and animations.
- Inspect a copied Search Lab report and deep link for credentials, cache paths, provider endpoints, session material, and raw source bodies.
- Verify an ordinary comparison creates no jobs or RAG runs; verify repair cannot exceed its confirmed chunk bound.
- Rebuild assets twice and require no second diff.
- Restart without `--admin` to verify the disable path, then upgrade the binary and reload to verify embedded assets and API version converge together.

Release cadence is intentionally coarse: do not tag or run an installation step for every admin increment. Publish after a reviewed batch of fixes or an epic-sized feature, using the repository release-notes workflow.
