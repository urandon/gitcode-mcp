# Sanitizer Script Add Validation Scenarios

## 017-fixtures-tests-task-1-sanitizer-script-add-scenario-1

A developer triggers the fixture-publication path by running `scripts/sanitize-fixtures.sh <raw_dir> fixtures/ --owner raw-owner --repo raw-repo --project raw-project --host gitcode.example.invalid` against synthetic raw captures containing JSON and HTTP transcript files, then running `go test ./internal/gitcode/... -run TestSanitizedFixtures -count=1` against the sanitized fixture corpus.

Validation steps:

1. Create raw captures under a temporary directory, including endpoint-shaped paths below `api/v5/repos/raw-owner/raw-repo/issues/42`.
2. Include private identifiers, a raw hostname, an `Authorization:` transcript header, and a JSON key named `Authorization` in the raw inputs.
3. Run the production sanitizer command with the exact required flag shape.
4. Temporarily install the sanitized output as `fixtures/` and execute the production Go fixture-safety test.

## 017-fixtures-tests-task-1-sanitizer-script-add-scenario-2

The target surface is the fixture sanitizer plus the sanitized fixture corpus; the expected state is that output files preserve endpoint-shaped paths while containing only `api.example.com`, `example-owner`, `example-repo`, and `example-project` placeholders and no `Authorization` string, raw credentials, raw hostnames, or private identifiers.

Validation checks:

1. Assert generated files exist at `api/v5/repos/example-owner/example-repo/issues/42/response.json` and `api/v5/repos/example-owner/example-repo/issues/42/transcript.http`.
2. Inspect every generated file path and body for forbidden raw tokens: `Authorization`, `raw-owner`, `raw-repo`, `raw-project`, `gitcode.example.invalid`, and `raw-secret-token`.
3. Inspect host-like tokens in generated bodies and allow only `api.example.com`.
4. Assert the combined sanitized corpus still contains `api.example.com`, `example-owner`, `example-repo`, and `example-project`.

## 017-fixtures-tests-task-1-sanitizer-script-add-scenario-3

Executable evidence: the sanitizer command exits 0 and `go test ./internal/gitcode/... -run TestSanitizedFixtures -count=1` passes; if a raw `Authorization` header survives in any output file, the test fails.

Validation checks:

1. The harness exits non-zero if the sanitizer command fails.
2. The harness exits non-zero if required sanitized paths are missing.
3. The harness exits non-zero if any forbidden token survives in sanitized output.
4. The harness exits non-zero if `TestSanitizedFixtures` rejects the temporarily installed sanitized corpus.
5. The harness restores any pre-existing `fixtures/` directory and leaves no product-path modifications behind.
