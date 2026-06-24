# Design Package Validation Scenarios

Task: `010-doctor-readiness-task-1-change-livereadinesssnapshot`

## 010-doctor-readiness-task-1-change-livereadinesssnapshot-scenario-1

Operator runs `gitcode-mcp doctor --live --format json` through the Go CLI route with a temporary cache path, a selected repository binding containing a mock `api_base_url`, a second non-selected binding with a different `api_base_url`, and a mocked credential source. The CLI doctor JSON must report `provider_mode: "live-http"`, mocked credential source metadata, the temporary cache path, only the selected binding's API base URL, `api_base_url_source: "repository_binding.api_base_url"`, and readiness status `ready`, without a token value or authorization header and without contacting the mock HTTP server.

Executable coverage is provided by `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/(SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT|SCN-CRED-DOCTOR-LIVE-MOCK-KEYCHAIN|SCN-CLI-DOCTOR-LIVE-JSON-SELECTED-VS-NON-SELECTED)'`.

## 010-doctor-readiness-task-1-change-livereadinesssnapshot-scenario-2

Operator runs the same CLI route with `doctor --repo` selecting the other binding. The JSON must switch the effective `api_base_url` to that binding's mock server URL and must not contain the previously selected URL as the effective value. Neither selected nor non-selected mock HTTP server may receive a request from doctor.

Executable coverage is provided by `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-DOCTOR-LIVE-JSON-SELECTED-VS-NON-SELECTED'`.

## 010-doctor-readiness-task-1-change-livereadinesssnapshot-scenario-3

Operator runs `doctor --live --format json` with no usable credential. The JSON must still report selected repository/cache/API base URL values, return readiness status `missing_credential`, include diagnostic `missing_credential`, and leave the mock HTTP server request count at zero.

Executable coverage is provided by `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CLI-DOCTOR-LIVE-JSON-MISSING-CREDENTIAL-NO-HTTP'`.

## 010-doctor-readiness-task-1-change-livereadinesssnapshot-scenario-4

Executable evidence is an offline Go CLI-route test for selected-vs-non-selected repository/base URL effective-value selection plus the live missing-credential doctor case, and `go test ./...` passes without real credentials, external network, or OS Keychain access.

Executable coverage is provided by `tests/design_package/010-doctor-readiness-task-1-change-livereadinesssnapshot/run.sh`, which runs focused CLI-route tests, focused doctor package tests, `go test ./...`, and `git diff --check`.
