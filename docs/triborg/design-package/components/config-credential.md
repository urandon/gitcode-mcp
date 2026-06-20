# Design Package Component: config-credential

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: Config & Credential

## Summary
The `config-credential` component is affected because the approved architecture requires explicit config discovery, canonical YAML config UX, redacted effective-config display, credential-source reporting, and config-owned runtime-audit diagnostics. Current component anchors support startup config loading, environment-token lookup, and diagnostic redaction, but the product command surfaces and credential-store/status split are not yet present.

## Top-Level Alignment
This component owns the Config & Credential subsystem: YAML/env/credential-store precedence, token sourcing without plaintext persistence, redacted diagnostics, cache-path resolution, and auth/config failure classes. It supplies resolved config and credential status to CLI startup, adapter construction, repo binding diagnostics, cache diagnostics, and doctor runtime audit without performing GitCode network calls.

## Tasks

### Task 1: ConfigCommands add redacted UX
Outcome IDs: outcome-3
Outcome Role: primary_product
Decommission IDs: decommission-7
Change Type: add
Description: Add the product-facing config and auth command surface owned by `config-credential`. The local role is to make active configuration, override precedence, cache path resolution, and token availability visible without exposing secret values. This replaces opaque startup-only config discovery with explicit CLI surfaces that work in temporary homes, CI/headless environments, and platform credential-store scenarios.
Existing Behavior / Reuse: Reuse the existing `config.Config`, `config.Overrides`, `config.Source`, `config.OSSource`, `config.Load`, `config.Token`, and `config.RedactDiagnostic` concepts. Current behavior supports JSON config loading from legacy `GITCODE_CONFIG`, default cache path derivation, environment token lookup through `GITCODE_TOKEN`, startup override merging, and redacted errors, but it does not expose `auth` routes, `config init/locate/show` routes, YAML-first config, effective-config source metadata, credential-store abstraction, or platform/headless remediation. Keep startup loading internal for normal command execution while moving user-visible discovery to the new commands for `decommission-7`.
Detailed Design: Add a component-local `ConfigLocator` returning `ConfigLocation{Path, Source, Explicit, Exists, Format}` with canonical precedence: `GITCODE_MCP_CONFIG` explicit path first; default YAML path `$XDG_CONFIG_HOME/gitcode-mcp/config.yaml` or `~/.config/gitcode-mcp/config.yaml` second; legacy `GITCODE_CONFIG` third as read-only compatibility for existing JSON configs; built-in defaults last. `config init` creates YAML only at the `GITCODE_MCP_CONFIG` path when set or the default YAML path otherwise, never writes JSON, and refuses to overwrite unless an explicit overwrite flag is supplied. Add `LoadEffective` returning `EffectiveConfig{Config, Location, FieldSources, CredentialPolicy, CachePathSource}`; field precedence is environment overrides (`GITCODE_MCP_CACHE_DIR`, `GITCODE_API_URL`, command overrides where already supported) over canonical YAML over legacy JSON over defaults, and duplicate env/config fields are resolved by env winning with source metadata recorded. Keep JSON decoding minimal and read-only: accept the existing legacy schema through `GITCODE_CONFIG`, mark its source as `legacy-json`, and do not add broad ambiguous YAML/JSON auto-detection beyond file extension or explicit legacy source.

Split credential reporting from secret retrieval with two interfaces. Add `CredentialStatusReporter.Status(ctx, EffectiveConfig) CredentialStatus`, which returns only `CredentialStatus{Source, Present, StoreMode, ErrorClass, Remediation}` and never token bytes. Add a separate secret-bearing `TokenResolver.ResolveToken(ctx, EffectiveConfig) (SecretString, CredentialStatus, error)` used only by startup/runtime adapter construction; display code, `config show`, `auth status`, and doctor config reporting may call `Status` but not inspect `SecretString`. Credential precedence is `GITCODE_TOKEN` first, then configured keyring/keychain provider when `credential.store` is `auto` or a platform store, then env-only/headless mode when configured, then missing-token status; keyring unavailable/denied states degrade to typed status and remediation without raw provider errors. Add `EnvCredentialProvider`, `KeyringCredentialProvider`, and `ChainCredentialProvider`; the chain stops at the first present token for `ResolveToken` but can report the selected source and skipped/unavailable providers through redacted status fields.

Add `RedactedEffectiveConfig` rendering for path/source, config format, field override sources, cache path, credential store mode, token presence, and remediation. The active config path is intentionally displayed for product commands; tests use temporary/sanitized paths and separately assert that token values, file-contained secrets, and raw credential-store errors never appear. Add CLI handlers for `gitcode-mcp config init`, `gitcode-mcp config locate`, `gitcode-mcp config show --redacted`, and `gitcode-mcp auth status` that call only the config/credential APIs. Enforce the negative invariant for `decommission-7` by making malformed or missing startup diagnostics point to `config locate`/`config show --redacted`, while no product command requires reading source code to determine active paths or overrides.
Acceptance Criteria: A developer runs `gitcode-mcp config init`, `gitcode-mcp config locate`, `gitcode-mcp config show --redacted`, and `gitcode-mcp auth status` against a temporary config home with mocked OS credential providers; the CLI displays the active config path/source, canonical format, legacy JSON compatibility source when applicable, field override sources, cache path resolution, credential source, token presence without value, and platform/headless remediation. Local CLI tests exercise the real command routes with `GITCODE_MCP_CONFIG`, default YAML path, legacy `GITCODE_CONFIG` JSON, env overrides, env-only mode, keyring-present, keyring-unavailable, and missing-token states; they verify exit codes and visible output, assert env overrides win duplicate fields, assert `config init` writes YAML only, and assert no token, config-file secret, or raw credential-store diagnostic appears in stdout/stderr while sanitized temporary paths are allowed and expected.
Workload: 1.7 MM

### Task 2: RuntimeAuditConfig emit report
Outcome IDs: outcome-1
Outcome Role: supporting_evidence
Decommission IDs: none
Change Type: add
Description: Add the config-owned section of `doctor --runtime-audit --repo <repo_id>`. The local role is to provide installed version, active config source, credential status, cache path resolution, and actionable config/auth failure classes to the larger doctor report. This task emits supporting evidence for runtime audit while leaving repo binding, cache row counts, MCP surfaces, and index readiness to their owning components.
Existing Behavior / Reuse: Reuse the existing startup version source, `config.Load`/new `LoadEffective`, redaction helper, credential status model, and cache-path resolution from Task 1. Current behavior can print version and redact config-load errors, but it has no runtime-audit config report object and no doctor integration for config/auth status. This task does not duplicate cache, MCP, repo, or index audit logic.
Detailed Design: Add `RuntimeAuditConfigReport` with fields such as `Version`, `ConfigPath`, `ConfigSource`, `ConfigFormat`, `ConfigExists`, `CachePath`, `CachePathSource`, `CredentialSource`, `TokenPresent`, `CredentialStoreMode`, `FailureClasses`, `Remediation`, and `HandoffFields`. Add `BuildRuntimeAuditConfigReport(src, overrides, credentialStatusReporter, version)` that calls `ConfigLocator`, `LoadEffective` where possible, `CredentialStatusReporter.Status`, and cache-path resolution without opening the cache, checking repo aliases, constructing adapters, or attempting network access. Owned failure classes are `no-config`, `config-malformed`, `config-unreadable`, `legacy-config`, `token-missing`, `credential-store-unavailable`, and `credential-store-denied`; all embedded diagnostics pass through redaction before entering the report.

Define the doctor integration contract narrowly: `config-credential` emits only `RuntimeAuditConfigReport` plus typed handoff fields such as resolved config path, resolved cache path, configured repo declarations, and credential status for other components to consume. The composed doctor command must not synthesize success, ready, or healthy values for cache, repo binding, MCP, or index sections when their owners have not supplied reports; missing owner reports remain absent or explicitly `not_reported_by_owner`, never `ok`. If config loading fails, `BuildRuntimeAuditConfigReport` still uses locator and credential status where possible so users receive actionable config/auth remediation instead of only a startup failure. Add CLI-route tests for `gitcode-mcp doctor --runtime-audit --repo <repo_id>` that assert the config subsection exists in stable text or JSON output and that non-config sections are not faked by this component.
Acceptance Criteria: A developer runs `gitcode-mcp doctor --runtime-audit --repo fixture-repo` with a temporary fixture config source; the target CLI surface includes the config-owned runtime audit fields: installed version, active config path/source/format, cache path/source, credential source state, token-present/token-missing status, and config/auth failure classes. A fixture-backed CLI runtime-audit test executes the product command with valid YAML, missing config, malformed config, legacy JSON config, missing token, and keyring-unavailable states; it verifies the config subsection and typed handoff fields are present, verifies cache/repo/MCP/index success is not synthesized by this component, and verifies no token value, config-file secret, or raw credential-store diagnostic is emitted while sanitized temporary paths are displayed.
Workload: 0.7 MM

## Cross-Cutting Constraints
- Token material is never stored in config structs, logs, cache, fixtures, redacted config output, or doctor output — credential isolation is required across CLI startup, adapter construction, docs, and diagnostics.
- Config discovery and auth status must be public-safe under temporary homes and CI/headless mode — product commands display active paths, while tests use sanitized temporary paths and prohibit secret/raw-error leakage.
- Live network calls are not allowed from config/auth/doctor config sections — this component resolves only local config and credential presence, while adapter reachability belongs to other components.
- Credential-store support must degrade to environment-only or typed missing-token status when platform stores are unavailable — this follows the approved layered sourcing pattern from Git Credential Manager/keyring-style systems.

## Data And Control Flow
- User runs `config init` — CLI handler calls `ConfigLocator`, chooses `GITCODE_MCP_CONFIG` or the default YAML path, writes sample YAML only when allowed, and returns the active path without token data.
- User runs `config show --redacted` — CLI handler calls `LoadEffective`, `CredentialStatusReporter.Status`, and redacted rendering, then emits field sources, config format, cache path, and token presence only.
- User runs `auth status` — CLI handler queries credential status in env/keyring/env-only/missing order and emits source, present/missing state, error class, and remediation without exposing token bytes.
- Runtime startup or adapter construction needs a token — startup code calls `TokenResolver.ResolveToken`; display paths receive only `CredentialStatus` and never the `SecretString` value.
- User runs `doctor --runtime-audit` — doctor requests `RuntimeAuditConfigReport` first, then other component owners append their reports; config failures remain typed and redacted, and non-config readiness is not synthesized here.

## Component Interactions
- `config-credential` -> `CLI entrypoint` — provides `ConfigLocator`, `LoadEffective`, `CredentialStatusReporter`, `TokenResolver`, and command handlers for config/auth/doctor config sections.
- `config-credential` -> `gitcode_adapter` — provides secret-bearing token lookup only through `TokenResolver` during adapter construction; diagnostics use `CredentialStatus` and never serialize token values.
- `config-credential` -> `cache_sync` — provides resolved cache path and cache-path source for startup and doctor handoff; cache opening, WAL status, and row-count inspection remain cache-owned.
- `config-credential` -> `repo_binding` — provides effective config repository declarations and config-source diagnostics; repo validation and alias collision checks remain repo-binding-owned.
- `config-credential` -> `docs_dogfood` — exposes stable command outputs for install/config/secrets docs and doc smoke tests while preserving redaction invariants.

## Rationale
The approved architecture materially changes this component from internal startup config parsing into a user-visible configuration and credential subsystem. The current implementation already supplies anchors for loading, overrides, environment token lookup, and redaction, but the required product commands, canonical YAML precedence, credential status/token split, effective-config reporting, and runtime-audit config report are missing and must be added here.

## Skip Rationale
Not skipped.

## Runner Evidence
- Final message: `runa/calls/call-0207-run_attempt-1/final_message.txt`
