# Dogfood Checklist Evidence Log

Append-only evidence log for the one-week dogfood checklist.
Each slice run appends a new evidence entry. Prior entries are never edited or deleted.
Replacement commands append new entries with `replaces_command_id`, `reason`, and `supersedes_transcript`.

## Evidence Entry Template

<!--
### slice_id: <dayN-name>
- **slice_id**: day1-config-repo
- **required_prior_slices**: (none)
- **command**: project/dogfood/the\ run.sh --slice day1 --cache-path <cache-dir>/gitcode-mcp.db --transcript project/dogfood/evidence/day1.md
- **expected_fixture_result**: All config/repo commands succeed or return documented diagnostics; repository is bound and visible in repo status.
- **actual_redacted_result**: <PASS | FAIL | BLOCKED> — <summary>
- **transcript_path**: project/dogfood/evidence/day1.md
- **status**: <pass | fail | blocked>
- **blocker**: <empty | description>
- **next_action**: <empty | description>

Replacement metadata (optional):
- **replaces_command_id**: <prior slice or command id>
- **reason**: <why the replacement was needed>
- **supersedes_transcript**: <prior transcript path>
-->

---

## Evidence Entries

(No evidence entries yet. Slices append entries here as they are executed.)


### slice_id: day1

- **timestamp**: 2026-06-20T05:56:42Z
- **required_prior_slices**: (none)
- **command**: project/dogfood/the\\ run.sh --slice day1 --cache-path /var/folders/41/71vc01p17316mft_g37ppjb00000gn/T/tmp.bVe4PaOv4s/nocfg.db --transcript /var/folders/41/71vc01p17316mft_g37ppjb00000gn/T/tmp.bVe4PaOv4s/nocfg.md
- **expected_fixture_result**: All config/repo commands succeed or return documented diagnostics; repository is bound and visible in repo status.
- **actual_redacted_result**: **FAIL** — 5 failure(s)
- **transcript_path**: /var/folders/41/71vc01p17316mft_g37ppjb00000gn/T/tmp.bVe4PaOv4s/nocfg.md
- **status**: fail
- **blocker**: 5 step(s) produced unexpected outcomes
- **next_action**: review transcript at /var/folders/41/71vc01p17316mft_g37ppjb00000gn/T/tmp.bVe4PaOv4s/nocfg.md for undocumented failures

