# Scenarios: Feedback Validator Add

## 022-docs-dogfood-task-4-feedback-validator-add-scenario-1

A coordinator fills `project/dogfood/feedback.md` after running the dogfood checklist and runs `project/dogfood/check-dogfood-feedback project/dogfood/feedback.md`.

## 022-docs-dogfood-task-4-feedback-validator-add-scenario-2

The target product surface is the public-safe feedback artifact and stable validator command.

## 022-docs-dogfood-task-4-feedback-validator-add-scenario-3

The visible outcome is zero fatal findings for sanitized feedback, warnings only for bounded unknown-identifier cases, and categorized line-referenced failures for seeded token, cookie, private path, private host, raw-response, and non-allowlisted identifier examples.

## 022-docs-dogfood-task-4-feedback-validator-add-scenario-4

Executable evidence is `project/dogfood/check-dogfood-feedback` over both pass and fail fixtures.
