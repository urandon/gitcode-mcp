# Materialized Validation Scenarios

Task: `016-internal-gitcode-task-5-change-issue-label-omission-serialization-interna`

## 016-internal-gitcode-task-5-change-issue-label-omission-serialization-interna-scenario-1

Issue create without labels: HTTP body lacks labels key entirely.

Executable validation: run the mocked HTTP client product-path test `TestScenario016CreateIssueLabelsOmitted` in `internal/gitcode`. The test calls `HTTPClient.CreateIssue`, captures the raw POST body received by a local `httptest` server, unmarshals the serialized JSON, and fails if the `labels` key is present.

## 016-internal-gitcode-task-5-change-issue-label-omission-serialization-interna-scenario-2

Issue update changing only title: HTTP body lacks labels key entirely.

Executable validation: run the mocked HTTP client product-path test `TestScenario016UpdateIssueTitleOnlyLabelsOmitted` in `internal/gitcode`. The test calls `HTTPClient.UpdateIssue`, captures the raw PATCH body received by a local `httptest` server, unmarshals the serialized JSON, and fails if the `labels` key is present.

## 016-internal-gitcode-task-5-change-issue-label-omission-serialization-interna-scenario-3

Serialized JSON inspected; labels property absent from serialized body.

Executable validation: the two omission tests above inspect serialized request JSON bodies produced by the production write path. The companion test `TestScenario016ExplicitLabelsPreserved` proves explicit label mutation still serializes the `labels` field, preventing a false pass caused by dropping labels unconditionally.
