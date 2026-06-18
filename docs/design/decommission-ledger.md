# Decommission Ledger

Schema Version: `triborg.decommission-ledger.v1`

## decommission-1
- Request Task: 10
- Target: Shell-based agent knowledge layer: find, rg --files, rg -n, sed -n, hand-maintained markdown indexes for source discovery
- Category: surface
- Action: `replace`
- Verification: Run gitcode-mcp search_sources, list_sources, get_source, and get_snippet commands offline on ingested fixture data; confirm each produces output semantically equivalent to the replaced shell workflow without invoking find/rg/sed.
- Allowlist: none
- Keep Reason: n/a


## decommission-2
- Request Task: 10
- Target: Hand-maintained markdown indexes: source ledger, track index, task backlog, acceptance ledger, open questions index, backlink graph, broken-link report (plaintext files)
- Category: state_contract
- Action: `replace`
- Verification: Run gitcode-mcp index --full on ingested fixture sources; verify that gitcode-mcp tasks, gitcode-mcp tracks, gitcode-mcp stale-index, and gitcode-mcp link-check produce JSON output with equivalent data to the replaced plaintext indexes without requiring a human to edit a markdown index file.
- Allowlist: none
- Keep Reason: n/a


## decommission-3
- Request Task: 10
- Target: Agent shell workflow for coordinator intake: manually reading AGENTS.md, README, backlog, track indexes, and handoff files by path
- Category: surface
- Action: `replace`
- Verification: Run the minimum-replacement-bar walkthrough: ingest, search_sources, get_source, source_backlinks, sync_status all complete offline and produce correct output for a coordinator intake, task lookup, and handoff review scenario.
- Allowlist: none
- Keep Reason: n/a


## decommission-4
- Request Task: 10
- Target: Clickable local markdown links (relative file paths in markdown) as the primary cross-reference mechanism; replaced by bidirectional alias maps with GitCode issue/wiki link generation
- Category: surface
- Action: `replace`
- Verification: Run gitcode-mcp get_source and verify the output includes both a local cache path and a resolved remote alias; run gitcode-mcp link-check and confirm that broken local-path links are flagged with suggested alias resolutions.
- Allowlist: none
- Keep Reason: n/a


## decommission-14
- Request Task: 14
- Target: legacy surface referenced by request task 14: Define one-week implementation plan
- Category: unspecified
- Action: `replace`
- Verification: At the end of each simulated day, the documented verification command passes. The Day 7 walkthrough exercises the minimum replacement bar: `ingest` → `search_sources` → `get_source` → `source_backlinks` → `sync_status` all complete offline and produce correct output for a coordinator intake, task lookup, and handoff review scenario described in the plan.
- Allowlist: none
- Keep Reason: n/a
