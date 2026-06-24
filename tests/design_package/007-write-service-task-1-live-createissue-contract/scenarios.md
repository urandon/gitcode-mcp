# 007-write-service-task-1-live-createissue-contract Scenarios

## 007-write-service-task-1-live-createissue-contract-scenario-1

Operator runs `gitcode-mcp create-issue --live --repo <repo> --title <title> --idempotency-key <key>` with `GITCODE_TOKEN` unset and a mocked Keychain-equivalent credential already resolved by CLI startup. The validation exercises the real CLI startup and write-service route through `cmd/gitcode-mcp` Go integration coverage. The mocked external GitCode HTTP provider must receive one authenticated create request with the expected idempotency key, and CLI output must report live create success without leaking token material or `fixture client is read-only`.

Executable evidence: `go test ./cmd/gitcode-mcp -run 'TestCLIStartupPlanSelectsLiveProvider/SCN-CRED-LIVE-WRITE-MOCK-KEYCHAIN$' -count=1 -v`.

## 007-write-service-task-1-live-createissue-contract-scenario-2

Operator repeats the same create command with the same idempotency key and same payload. The write-service route must return deterministic replay or already-applied state without a second create side effect, while audit and cache state remain inspectable through runtime APIs.

Executable evidence: `go test ./internal/service -run 'TestScenario007WriteLiveCreateIdempotentReplay$' -count=1 -v`.

## 007-write-service-task-1-live-createissue-contract-scenario-3

Operator repeats the same idempotency key with a different payload. The write-service route must reject the request with `write_idempotency_conflict` before another provider write, leaving mock/client request count unchanged after the conflict.

Executable evidence: `go test ./internal/service -run 'TestScenario007WriteLiveCreateIdempotencyConflict$' -count=1 -v`.

## 007-write-service-task-1-live-createissue-contract-scenario-4

Operator runs `create-issue --live` when the service is accidentally backed by the fixture read-only client. The route must return the fixture-fallback diagnostic code and user-visible output/error state must not contain `fixture client is read-only`.

Executable evidence: `go test ./internal/service -run 'TestScenario007WriteLiveFixtureFallbackDetected$' -count=1 -v`.

## Cross-scenario audit/cache confirmation

Successful live create must record non-secret audit success and cache issue confirmation with a remote id.

Executable evidence: `go test ./internal/service -run 'TestScenario007WriteLiveCreateAuditCacheConfirmation$' -count=1 -v`.

## Repository acceptance gate

The offline validation script runs `go test ./...` with live credential and API environment variables unset to ensure the evidence remains executable under the repository-wide acceptance gate without real GitCode credentials, external network, or OS Keychain access.
