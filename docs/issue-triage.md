# Evidence-First Issue Triage

Use this maintainer protocol for incoming issues and feedback before taking work.
[Feedback intake](feedback.md) prepares a reporter's worksheet; submission is not
validation, diagnosis, acceptance, or a release commitment. The
[delivery protocol](../AGENTS.md#autonomous-delivery-protocol) governs subsequent
implementation, independent exact-SHA review, CI, merge and release.

## Intake before implementation

1. Read the original title/description and all correction/design comments. Keep
   the original input unchanged; publish triage and later corrections as comments.
2. Refresh the affected issue/PR state explicitly; routine reads remain cached.
   Search **open and closed** duplicates and read likely matches, not just titles.
   Distinguish shared symptoms from the same underlying defect; a closed match
   does not establish that the reporter's installed version contains its fix.
3. Inspect the affected version/SHA and current intended base. Classify the signal:
   bug (contract violated), feature gap, UX friction, configuration/environment,
   or support/question. Reporter category, urgency and proposed fix are inputs,
   not proof of a bug or priority.
4. Reproduce with the smallest deterministic, sanitized fixture when possible.
   Record version/SHA, necessary mode/state, expected vs actual behavior, exact
   commands and outcomes: **passed**, **skipped**, **not run**, or **blocked**.
   An offline fixture is not a live-provider compatibility test; unavailable
   credentials, hardware or a service are prerequisites, not successful tests.
5. Separate five things: observed symptom, impact, confirmed signal, suspected
   cause, and proposed solution. State the confidence/limits of each hypothesis.
   Do not broaden a reproduced defect into an adjacent helper without evidence.
6. Post a verdict and next action using the template below **before implementation**.
   An agreed feature gap may enter design; it need not be called a reproduced bug.

Never copy credentials, cookies, private coordinates, local filesystem paths,
raw logs, provider bodies or conversations into public evidence. Prefer a finite
error/state, synthetic fixture and bounded sanitized excerpt. If public safety
cannot be established, stop publication and request an approved evidence route.
Do not perform an external write, destructive reproduction or production probe
without task-specific authority; use an authorized disposable target when needed.

## Verdicts and next actions

| Verdict | Next action | Closure prerequisite |
| --- | --- | --- |
| **Confirmed / reproduced** | State the demonstrated contract violation and regression oracle; prioritize a bounded fix. A confirmed feature gap goes to design instead. | For an implemented change, satisfy all delivery gates and link final evidence. Triage alone is not a fix. |
| **Relevant, reproduction blocked** | Name the missing prerequisite, safe checks already done, owner of the next step if known, and exact unblock condition. | Keep open while actionable work is blocked. Close only after evidence supports another verdict or an explicit disposition, not because the prerequisite is unavailable. |
| **Needs information** | Ask targeted questions that change the diagnosis: version, mode, smallest sequence, expected result or sanitized failure class. | No automatic closure for silence. If later closed by explicit maintainer disposition, record what remains unknown and how to reopen. |
| **Not reproduced** | State tested SHA/environment, exact attempted sequence and coverage limits; ask about the distinguishing circumstance. | A passing attempt is not proof of absence. Keep open if a credible signal remains; close only with a justified alternate resolution or explicit non-actionable disposition. |
| **Duplicate** | Link the canonical issue, compare the actual signal, and transfer any distinct evidence/dependency without rewriting intake. | Canonical coverage must match. Close with a comment/link and existing `duplicate` label; keep a distinct uncovered case open. |
| **Already fixed** | Link the fixing PR/commit and verified behavior; distinguish merged, released and installed versions. | Evidence must cover the reported case and required delivery gates. State the first containing release if verified, or explicitly say the fix is not yet released. |
| **Out of scope / not a product defect** | Explain the boundary with evidence; offer a safe configuration/support handoff or separate feature issue if justified. | Record the resolution and remaining uncertainty. A confusing product diagnostic may still warrant its own UX issue. Do not blame the reporter. |
| **Declined / deferred** | Give rationale, tradeoff, decision authority and dependency/revisit trigger. Deferral keeps justified work visible without promising a release. | Close a deliberate decline only with a recorded authorized disposition. Deferred work normally stays open; closing it needs an explicit archival disposition and reopening condition. |

Update the verdict when new evidence changes it; link the earlier assessment and
explain what changed. A configuration problem and a product UX defect can coexist.

## First response and triage comment

Use a short comment; omit genuinely inapplicable optional fields with a reason,
not invented facts. Keep one current acceptance checklist and link superseded
assessments rather than leaving contradictory progress claims.

```text
Classification / verdict:
Rationale and confirmed signal:
Observed symptom / impact:
Evidence: affected version; tested SHA/mode; minimal sanitized steps;
          expected vs actual; commands and passed/skipped/not-run/blocked results.
Cause: confirmed or hypothesis (with limits); proposed solution is separate.
Missing context / targeted questions (or none):
Severity / scheduling priority and rationale:
Workaround (or none verified):
Duplicate / fix / dependency links (or none found):
Next action / blocker owner and unblock or revisit condition:
Acceptance signal / closure or reopening evidence:
```

## Labels and priority

Reuse the repository's actual vocabulary; inspect it before applying labels and
preserve unrelated existing labels. These recommendations were checked against
the live repository vocabulary for this change. Do not create synonyms or new
priority/status labels merely to encode this protocol: verdict, severity and
priority can be explicit comment fields.

| Purpose | Existing labels and rule |
| --- | --- |
| Kind | `bug` for a demonstrated defect; `enhancement` for a capability gap; `documentation` for docs work; `question` for support or targeted missing information. Reassess after triage; intake labels are not evidence. |
| Provenance | Preserve `feedback` / `dogfood` for origin, not severity or acceptance. |
| Affected area | Use relevant existing labels such as `cli`, `mcp`, `service`, `cache`, `provider`, `sync`, `rag`, `setup`, `diagnostics`, `ci`, `release`, `admin-ui`, `ai-ux`. Do not add every surface when only one is affected. |
| Resolution | `duplicate` after canonical coverage is established; `wontfix` for a deliberate authorized decline, not ordinary deferral; `invalid` only when the report is demonstrably inapplicable/malformed and the comment explains why, never as shorthand for not reproduced. |
| Coordination | `epic` for an actual decomposed umbrella; `research` for a bounded investigation; `help wanted` only when a concrete contribution/unblock action is described. |

Severity is **demonstrated impact**, independently of scheduling: S0 means
critical data loss, credential exposure or broad unrecoverable failure; S1 means
a core workflow is blocked without a viable workaround; S2 means a degraded
workflow has a verified workaround; S3 means minor friction. State affected
cohort/scope and uncertainty; a feature's value is not defect severity. Suspected
critical impact warrants urgent safe investigation, not an unsupported claim.

Scheduling priority is an explicit decision: P0 contains an active critical
incident or broken required delivery gate; P1 is the next blocking correction;
P2 is planned ordinary work; P3 is deferred/low-urgency work with a revisit
condition. Explain dependencies, workaround, reach and cost; severity does not
automatically assign priority or a milestone. Do not repurpose independent code
review finding priorities as an incoming report's severity rating.

## Taking work, milestones and decomposition

- Take work only after a recorded actionable verdict, scope, acceptance oracle,
  non-goals, risk and verification plan. If evidence is missing, investigate or
  ask for it instead of silently implementing the reporter's preferred fix.
- Put confirmed blockers ahead of dependent features; normally drain the oldest
  actionable items within that order. Record why urgent impact or dependencies
  change the order. A release wave must not hide unresolved acceptance work.
- Assign a milestone only when the work is accepted for that scope and its
  dependencies/gates are explicit. An untriaged report, deferral or feature wish
  does not promise a version. Release tags require the separate delivery rules.
- If one issue controls several implementation tasks, publish a bounded design,
  contracts/diagrams as useful, then an epic with child issues, dependency order
  and checklists. Assess CLI, MCP, private service and Admin together; create an
  explicit UI task/cohort or state why none is needed. Do not silently omit a
  recovery/migration/observation surface. Small coherent fixes stay one issue/PR.

## Blocked follow-up, closure and reopening

For blocked/deferred work, name the blocker, evidence limits, next safe action
and observable unblock/revisit trigger. Distinguish lack of authority from a
technical dependency. When the prerequisite, implementation/version or reporter
evidence changes, repeat the relevant check and update the verdict. Do not
invent response-time commitments, periodic jobs or automatic age-based closure.

For **fixed**, post regression/acceptance evidence, reviewed head, PR/merge SHA,
required exact-head and resulting-main CI results, and any genuinely remaining
dependency. Follow AGENTS delivery gates before closure; never call skipped or
unavailable live qualification passed. Say whether the fix is released and do
not imply that merging upgrades an installed binary/config/cache.

For **duplicate**, link the canonical disposition and preserve new evidence;
for **already fixed**, verify the reported case and its containing version;
for **non-actionable/declined**, record the boundary/decision and uncertainty.
Documentation-only triage or administrative closure must not claim a product fix.
Reopen when new evidence contradicts the closure, the same acceptance oracle
fails on a containing version, or the canonical issue does not cover the case.
Link the prior resolution and new evidence; preserve the original task input.

## Public-safe examples (hypothetical)

1. **Confirmed bug:** An offline synthetic fixture on a named SHA requests
   `limit=2, offset=0` for open issues labelled `bug`; a closed issue is returned.
   The finite result and regression test confirm a status-predicate violation;
   filter ordering remains a hypothesis until code inspection establishes it.
   Record S2/P2 if a verified explicit-filter workaround exists; implement the
   narrow filter-order fix and close only after reviewed/green delivery. This
   example is not a claim that the current product has this defect.
2. **Blocked / not reproduced / needs information:** A reporter says “hybrid
   search fails.” Full-text succeeds on the tested SHA, but the embedding provider
   is unavailable. Verdict: relevant, hybrid reproduction blocked, not “fixed” or
   “no bug.” Ask for the affected version, requested/effective modes and sanitized
   failure class; name provider readiness as the unblock condition. An available
   provider with the exact sequence passing would justify a bounded
   not-reproduced assessment, not automatic closure. No workaround is verified
   unless full-text actually satisfies that reporter's goal.
3. **Duplicate / already fixed:** A label-query report matches a canonical issue
   by reproduction and acceptance signal, not just title. Link it and carry over
   new evidence before duplicate closure. If the canonical fix is merged but the
   reporter runs an earlier version, verify the case on the containing SHA and
   state “already fixed in main, not yet released” when that is the evidence;
   do not claim the reporter's installation is repaired. Reopen if the same
   sequence fails on the containing version.
