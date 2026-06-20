# Scenarios: 021-docs-dogfood-task-3-dogfood-checklist-add

## 021-docs-dogfood-task-3-dogfood-checklist-add-scenario-1
- **Trigger:** A follow-up implementer runs `project/dogfood/the run.sh --slice day1` through `--slice day7` against sanitized fixtures.
- **Expected:** Each slice command executes against the binary (`bin/gitcode-mcp`), produces a redacted transcript, and the runner exits 0 only when expected product checks pass for all dependent slices. The `classify_outcome` function detects `unknown command` as `product_gap` rather than masking it as `documented_diagnostic`.
- **Validation:** Scenario 8 (runner executes day1) verifies the binary is exercised with the runner, checking that 7 success outcomes are produced and no `unknown command` errors appear or are misclassified. Scenario 9 verifies unknown slice handling (exit code 2). Scenario 10 verifies missing fixture config fallback.

## 021-docs-dogfood-task-3-dogfood-checklist-add-scenario-2
- **Target Surface:** The ordered dogfood checklist (`docs/dogfood-checklist.md`), append-only evidence log template (`project/dogfood/checklist.md`), executable runner (`project/dogfood/the run.sh`), and evidence directory placeholder (`project/dogfood/evidence/.gitignore`).
- **Expected:** All four product surfaces exist, checklist maps each day to commands that are invocable on the built binary, and the runner delegates transcript redaction to `project/dogfood/lib/safety.sh`.
- **Validation:** Pre-flight (product surface existence), Scenario 1 (bash -n syntax), Scenario 2 (checklist doc structure with all 7 day slices and replacement-command rules), Scenario 2b (evidence log template has all 9 required fields plus replacement metadata), Scenario 3 (evidence directory and .gitignore preserving itself), Scenario 4 (runner --help output), Scenario 5 (checklist commands map to binary subcommands), Scenario 6 (runner-required flags present in binary), Scenario 7 (mandatory architecture subcommands present), Scenario 12 (runner delegates to safety.sh), Scenario 13 (replacement command history). Missing commands/flags owned by other components are reported as informational but do not fail validation.

## 021-docs-dogfood-task-3-dogfood-checklist-add-scenario-3
- **Visible Outcome:** Per-slice pass/fail diagnostics with redacted transcripts, preserved replacement-command history when a command changes, and final evidence showing offline CLI and MCP reads for one fixture issue and one fixture wiki page.
- **Expected:** Day 1 (config/repo) passes. Day 2 (fixture sync/index) passes. Day 3 (CLI reads) passes with both `ISSUE-42` and `wiki:Home` readable. Day 4 (MCP parity/transport) produces a documented diagnostic or success. Day 5 (concurrency/write safety) passes dry-run checks. Day 6 (snapshot integrity) passes. Day 7 (docs/live validation/feedback) produces final evidence with offline CLI reads for issue and wiki. Replacement commands are recorded as append-only entries; prior evidence is preserved.
- **Validation:** Scenario 15 (ISSUE-42 and wiki:Home referenced in checklist doc at least twice each), Scenario 16 (day 7 final evidence requirement documented), Scenario 17 (MCP parity coverage for both issue and wiki in runner day4), Scenario 18 (append-only evidence log structure). Scenarios 5-7 provide informational gap analysis of checklist commands against the binary surface.

## 021-docs-dogfood-task-3-dogfood-checklist-add-scenario-4
- **Executable Evidence:** The local checklist command sequence `project/dogfood/the\ run.sh --slice day1` through `project/dogfood/the\ run.sh --slice day7` runs deterministically without live credentials, producing redacted per-slice transcripts in `project/dogfood/evidence/`.
- **Expected:** All slices execute without requiring `GITCODE_TOKEN`, `GITCODE_LIVE_TEST`, or network access. Transcript passes `assert_public_safe_transcript`. No credentials, private paths, cookies, raw live responses, or non-allowlisted IDs appear.
- **Validation:** Scenario 8 (runner executes day1, transcript passes public-safety check), Scenario 14 (no credentials/secrets in any product surface), Scenario 19 (git diff --check whitespace on dogfood artifacts), Scenario 20 (runner never requires GITCODE_TOKEN for slice completion). Scenario 11 exercises binary core commands (repo-scoped, with --dry-run for writes) independently of the checklist runner, using skip classification for commands that depend on other component updates.

## Validation Summary
- 20 scenario checks across 4 required scenarios
- All checks produce PASS/SKIP outcomes; no FAIL outcomes
- Exit code 0 when all product checks pass
- Informational gap analysis for commands/flags owned by other components (scenarios 5-7) reported as yellow/skip, not failure
- Binary exercised via repo add, list, get, search, export, get-snippet, stale-index, recent, backlinks, link-check, and create-issue --dry-run
