# Design Package Component: doctor-readiness

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Doctor Readiness

## Summary
`doctor-readiness` is affected because `doctor --live --format json` must expose the same effective live configuration that explicit live commands use. The component-local change is a focused live-readiness snapshot for provider mode, credential source metadata, cache path, selected API base URL, readiness status, and non-secret diagnostics.

## Top-Level Alignment
This component implements the architecture’s doctor lifecycle for Task 9 and supports Task 3 and Task 8 by rendering shared credential-resolution metadata and repository-binding API base URL selection. It remains local and zero-network: doctor reports effective readiness state without constructing or probing the live HTTP provider.

## Tasks

### Task 1: Change LiveReadinessSnapshot
Outcome IDs: outcome-3, outcome-8, outcome-9
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: change
Description: Extend the doctor live JSON report so `doctor --live --format json` renders an effective live startup snapshot instead of incomplete doctor-local live state. The component-local entities are `doctor.Request`, `doctor.Build`, `doctor.Report`, `LiveProviderSection`, `CredentialSection`, readiness diagnostics, and JSON/text renderers. The report must show `provider_mode: "live-http"`, selected credential source metadata, cache path, selected API base URL, readiness status, and stable non-secret diagnostic codes.
Existing Behavior / Reuse: Existing doctor behavior already builds reports through `doctor.Build`, uses a configured `CredentialStatusReporter`, accepts cache path inputs, opens cache state, lists repository bindings, redacts diagnostics, and renders JSON through the CLI doctor route. The required functionality does not already exist because current live doctor output is incomplete: live mode is not the iteration-4 `live-http` effective provider mode, the selected repository `api_base_url` is not surfaced as a live-client authority, selected-vs-non-selected repository binding behavior is not proven, and readiness diagnostics are not ordered into a stable local status contract. Reuse the existing credential reporter, config loading, repository binding lookup, cache path selection, redaction, and renderer paths; do not add a second credential resolver or perform a live HTTP probe.
Detailed Design: Add or extend a component-local `LiveReadinessSnapshot` used by `doctor.Report` with fields equivalent to `provider_mode`, `credential_source`, `credential_present`, `cache_path`, `api_base_url`, `api_base_url_source`, `readiness_status`, and `diagnostics`. `doctor.Build` populates this snapshot when `doctor.Request.Live` is true; non-live doctor behavior continues to use the existing offline/fixture report shape with compatibility-preserving fields.

Repository selection must use the selected/effective repository binding, not store order. If `doctor.Request` includes an explicit repository selector from `doctor --repo`, that binding controls the reported repository identity and `api_base_url`; if CLI startup supplies an already-selected binding or startup resolution snapshot, that snapshot is authoritative and doctor only renders/redacts it; only when no repo is selected may doctor use an existing deterministic fallback defined by current doctor behavior. If two bindings exist, one selected and one not selected, the live readiness snapshot must contain only the selected binding’s API base URL and must not report or prefer the non-selected binding.

API base URL reporting follows the architecture’s authority rule. When the selected repository binding contains `api_base_url`, doctor reports that value with source metadata such as `repository_binding.api_base_url`, and it is authoritative. When the selected binding omits `api_base_url`, doctor reports the same documented fallback source that live startup would use, with source metadata such as `fallback_env` or `default_config`, if that fallback is valid; if no valid fallback exists, doctor reports an empty API base URL or omitted effective value according to existing JSON conventions plus readiness status `configuration_error` and diagnostic `missing_api_base_url`. If a candidate API base URL is syntactically invalid, doctor reports readiness status `configuration_error` and diagnostic `invalid_api_base_url`.

Credential handling remains read-only and non-secret. `doctor-readiness` consumes `CredentialStatusReporter.Status` from `credential-resolution` and renders source metadata such as `env:GITCODE_TOKEN`, `mock_keychain`, or another non-secret source name plus `credential_present`; it never renders token values, authorization headers, cookies, or raw credential material. If no usable credential is present for live mode, doctor renders readiness status `missing_credential` and diagnostic `missing_credential` without contacting any HTTP server.

Readiness status is computed locally with deterministic precedence: first `configuration_error` with diagnostic `missing_repository_binding` when no selected binding or allowed fallback repository exists; second `configuration_error` with `missing_api_base_url` when no effective base URL source exists; third `configuration_error` with `invalid_api_base_url` when the effective base URL is invalid; fourth `missing_credential` with `missing_credential` when live credentials are absent; fifth `configuration_warning` with `cache_unavailable` or `cache_path_unavailable` when existing doctor cache inspection detects a non-fatal local cache/config issue; otherwise `ready` with no error diagnostic. The `diagnostics` component owns stable diagnostic code definitions and classification names; `doctor-readiness` only renders those codes in its snapshot and JSON/text output.

Update JSON rendering to expose the live readiness fields in the existing live provider section or a nested `live_readiness` object while preserving existing compatible JSON fields. Update text rendering only for parity with the same non-secret values. Invariants: live doctor never constructs or calls `live-provider`; effective fields match live startup selection; repository binding `api_base_url` wins when present; fallback API base URL reporting matches live startup fallback metadata when binding omits it; diagnostics are stable and non-secret; redaction runs over the final report before rendering.
Acceptance Criteria: Operator runs `gitcode-mcp doctor --live --format json` with a temporary cache path, a selected repository binding containing a mock `api_base_url`, a second non-selected binding with a different `api_base_url`, and a mocked credential source; the CLI doctor JSON reports `provider_mode: "live-http"`, the mocked credential source metadata, the temporary cache path, only the selected binding’s API base URL, `api_base_url_source: "repository_binding.api_base_url"` or equivalent, and readiness status `ready`, with no token value or authorization header. Operator runs the same CLI route with `doctor --repo` selecting the other binding; the JSON switches to that binding’s API base URL and does not contain the previously selected URL as the effective value. Operator runs `doctor --live --format json` with no usable credential; the JSON still reports selected repository/cache/API base URL values but returns readiness status and diagnostic `missing_credential`, and the mock HTTP server request count remains zero. Executable evidence is an offline Go CLI-route test for selected-vs-non-selected repository/base URL effective-value selection plus the live missing-credential doctor case, and `go test ./...` passes without real credentials, external network, or OS Keychain access.
Workload: 0.6 MM

## Cross-Cutting Constraints
- Secret redaction is mandatory — doctor-readiness consumes credential metadata and must never expose token values, authorization headers, cookies, raw credential errors, or private credential material in JSON or text output.
- Effective-value reporting is mandatory — doctor output must reflect the same provider mode, credential source, cache path, selected repository binding, and API base URL authority used by live startup.
- Network isolation is mandatory — primary live doctor readiness output is local configuration/readiness reporting and must not require real network, real credentials, or OS Keychain access.
- Shared credential source semantics are mandatory — doctor-readiness reports source metadata from the shared credential pipeline, including injectable mocked Keychain-equivalent sources, without implementing a separate credential resolver.

## Data And Control Flow
- CLI invokes live doctor JSON — `cli-startup` to `doctor-readiness` — `doctor.Request.Live` selects the live readiness snapshot and `doctor.Build` returns a redacted report for JSON rendering.
- Selected repository binding is resolved — `cli-startup` or `doctor.Request` to `doctor-readiness` — explicit `doctor --repo` or startup-selected binding controls the reported API base URL; deterministic fallback is used only when no repo is selected and existing doctor behavior defines one.
- API base URL source is reported — `repository-binding` to `doctor-readiness` — binding `api_base_url` is authoritative when present; otherwise doctor reports the same documented live-startup fallback source metadata or a configuration diagnostic.
- Credential metadata is resolved — `credential-resolution` to `doctor-readiness` — doctor consumes `CredentialStatusReporter.Status` and renders source/presence metadata only.
- Readiness diagnostics are rendered — `diagnostics` to `doctor-readiness` — doctor renders stable non-secret codes such as `missing_credential`, `missing_repository_binding`, `missing_api_base_url`, `invalid_api_base_url`, and `cache_unavailable` without owning global diagnostic classification.
- Cache path is selected — `cli-startup` and config to `doctor-readiness` — explicit command cache path wins, then effective config, then existing default cache path fallback for report consistency.

## Component Interactions
- `credential-resolution` -> `doctor-readiness` — provides credential status metadata used for `credential.source`, `credential_present`, available source names, and non-secret readiness reporting.
- `repository-binding` -> `doctor-readiness` — provides selected repository identity, authoritative binding `api_base_url`, and fallback source metadata when the binding omits `api_base_url`.
- `cli-startup` -> `doctor-readiness` — passes live flag, selected repository or startup snapshot, cache path, config source, and credential reporter into the doctor build/render flow.
- `diagnostics` -> `doctor-readiness` — supplies stable typed non-secret diagnostic codes for doctor to render, including `missing_credential`, `invalid_api_base_url`, and configuration/cache readiness diagnostics.
- `cli-integration-tests` -> `doctor-readiness` — verifies CLI JSON fields using temporary cache/config, selected and non-selected repository bindings, mocked credentials, and zero-network execution under `go test ./...`.

## Rationale
Component Impact marks `doctor-readiness` as detailed for exactly one delta: live doctor JSON readiness reporting. Existing doctor reporting is reusable but does not yet expose the iteration-4 effective live-readiness contract, especially selected repository API base URL authority, fallback source metadata, and ordered readiness diagnostics.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0375-run_attempt-1/final_message.txt`
