# Validation Scenarios: 019-docs-dogfood-task-1-docs-smoke-package-add

## Scenario Inventory

### 019-docs-dogfood-task-1-docs-smoke-package-add-scenario-1
**Description:** A new developer runs `project/dogfood/docs-smoke.sh --fixture-config testdata/configs/dogfood.yaml --cache-path <tmp>/gitcode-mcp.db --transcript <tmp>/docs-smoke.md` against sanitized fixtures.

**Expected:** The smoke runner exits 0, produces a redacted transcript at the specified path, and no undocumented failures occur. Every command in the manifest either succeeds, returns a documented diagnostic, or is skipped as live-gated.

**Validation:** Execute the smoke runner with a temporary cache and verify exit code 0, transcript file existence and non-empty content, and absence of `undocumented_failure` outcomes in the transcript.

---

### 019-docs-dogfood-task-1-docs-smoke-package-add-scenario-2
**Description:** The target product surface is the documented CLI/MCP workflow from install through config, repo binding, fixture adapter sync/index, offline cache-only CLI read, MCP HTTP/SSE `/message` or documented stdio `tools/call` read, fixture validation, dry-run or documented-diagnostic write walkthrough behavior, and documented live-skip behavior.

**Expected:** The smoke command manifest covers every documented workflow surface. Steps for install, config, secrets, repo binding, fixture sync/index, CLI reads (list, get-issue, get-wiki, snippet, list-chunks, cache-status), MCP read, write dry-run, fixture validation gate, and live write skip are all present and produce their documented outcomes.

**Validation:** Parse the command manifest (`project/dogfood/docs-smoke.commands`) and verify entries exist for install, config, secrets, repo, sync, read, MCP, write, fixture validation, and live skip. Each entry must have a valid `doc_path` referencing an existing docs file.

---

### 019-docs-dogfood-task-1-docs-smoke-package-add-scenario-3
**Description:** The visible outcome is a redacted transcript where every curated documented command succeeds, returns the documented diagnostic, or is skipped as credential-gated live validation; fixture-only steps fail if they attempt live access; the MCP read step returns the documented fixture snippet or documented read diagnostic for the same cache.

**Expected:** The redacted transcript shows no `undocumented_failure` outcomes. The MCP step transcript contains the fixture snippet (`remote issue body`). The redacted transcript passes public-safety checks (no tokens, private paths, cookies).

**Validation:** Parse the redacted transcript: verify no `undocumented_failure` outcomes; verify MCP step output contains expected fixture content; run `assert_public_safe_transcript` on the redacted output.

---

### 019-docs-dogfood-task-1-docs-smoke-package-add-scenario-4
**Description:** Executable evidence is the local `project/dogfood/docs-smoke.sh` command; `go test ./...` is not claimed to exercise this shell smoke unless a later explicit Go wrapper is added by an owning task.

**Expected:** `project/dogfood/docs-smoke.sh --help` exits 0. The script is a valid `bash` script. `go test ./...` passes (no regressions from new files).

**Validation:** Run `bash -n project/dogfood/docs-smoke.sh`; run `project/dogfood/docs-smoke.sh --help` and verify exit 0; run `go test ./...` and verify all packages pass.
