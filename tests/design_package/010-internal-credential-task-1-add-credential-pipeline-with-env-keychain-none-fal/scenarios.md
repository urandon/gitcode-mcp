# Scenario 010: Credential pipeline env→keychain→none fallback

## 010-internal-credential-task-1-add-credential-pipeline-with-env-keychain-none-fal-scenario-1

GITCODE_TOKEN set → resolver returns env source with token.

- **Given** a credential pipeline built with the production default provider order and a controlled source where `GITCODE_TOKEN=test-token-env-010`
- **When** `Pipeline.Resolve(ctx)` is executed
- **Then** resolution succeeds, the resolved token value is `test-token-env-010`, and the selected source is `env:GITCODE_TOKEN`

## 010-internal-credential-task-1-add-credential-pipeline-with-env-keychain-none-fal-scenario-2

No GITCODE_TOKEN, keychain present on darwin → returns keychain source with token.

- **Given** no `GITCODE_TOKEN` value is present and the runtime is darwin
- **When** the production `KeychainProvider` is inspected through its runtime `Probe(ctx)` and `Token(ctx)` product path
- **Then** it must not be the unavailable stub, and a present keychain credential must be resolvable as source `keychain`
- **Current expected failure if unchanged**: the implementation runner left `KeychainProvider` as a stub returning `credential-store-unavailable`, so this scenario reports the decommission/product gap instead of masking it with a validation-only fake

## 010-internal-credential-task-1-add-credential-pipeline-with-env-keychain-none-fal-scenario-3

Neither available → returns none. go test ./... passes without keychain dependency.

- **Given** a credential pipeline built with the production default provider order and a controlled source with no `GITCODE_TOKEN`
- **When** `Pipeline.Resolve(ctx)` and `Pipeline.Status(ctx)` are executed
- **Then** no token is resolved, `none` appears as the terminal fallback source, and the pipeline reports token missing without requiring a keychain library
- **And** `go test ./...` passes offline without GitCode credentials, network, or real OS keychain access
