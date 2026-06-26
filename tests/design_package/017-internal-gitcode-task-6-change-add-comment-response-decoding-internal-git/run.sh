#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

go test ./internal/gitcode -run 'TestConfirmedWriteOperations/SCN-GITCODE-ADD-COMMENT-LIVE-SHAPE-01|TestScenario017AddCommentMalformedBodySchemaDecode' -count=1
go test ./internal/service -run 'TestScenario017AddCommentLiveShapeCachesComment|TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted' -count=1
