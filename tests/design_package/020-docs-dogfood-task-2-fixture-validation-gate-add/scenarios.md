# Scenarios: 020-docs-dogfood-task-2-fixture-validation-gate-add

## 020-docs-dogfood-task-2-fixture-validation-gate-add-scenario-1
- **Trigger:** A maintainer runs `project/dogfood/validate-fixtures.sh --fixtures testdata/fixtures --transcript <tmp>/fixture-validation.md` without credentials.
- **Expected:** Script exits 0, transcript produced with offline validation pass, live validation skipped.

## 020-docs-dogfood-task-2-fixture-validation-gate-add-scenario-2
- **Target Surface:** The `validate-fixtures.sh` script, `docs/fixture-capture.md` documentation referencing validation commands, and the `docs-smoke.commands` manifest entry for `fixture-validation-gate`.
- **Expected:** Local validation command exists and is executable, docs-smoke.commands line for `fixture-validation-gate` includes `success` in allowed outcomes.

## 020-docs-dogfood-task-2-fixture-validation-gate-add-scenario-3
- **Visible Outcome:** Offline fixture validation passing or producing documented diagnostics, live validation reported as skipped, and a redacted transcript with no secrets/private paths/raw responses.
- **Expected:** Transcript passes `assert_public_safe_transcript`, contains `offline_pass` or `offline_fail` classification, live outcome is `live_skipped_no_flag` or `live_skipped_no_token`.

## 020-docs-dogfood-task-2-fixture-validation-gate-add-scenario-4
- **Executable Evidence:** `project/dogfood/validate-fixtures.sh --fixtures <path> --transcript <path>` runs successfully; optional `GITCODE_LIVE_TEST=1 GITCODE_TOKEN=<token> validate-fixtures.sh --live` produces redacted live output.
- **Expected:** With a mock token, `live_pass_redacted` classification appears, and the raw token value does NOT appear in the transcript. Transcript passes `assert_public_safe_transcript`.
