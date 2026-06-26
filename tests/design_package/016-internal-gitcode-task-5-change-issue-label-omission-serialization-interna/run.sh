#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

export GONOSUMDB="*"
export GOPRIVATE="*"

go test ./internal/gitcode -run 'TestScenario016(CreateIssueLabelsOmitted|UpdateIssueTitleOnlyLabelsOmitted|ExplicitLabelsPreserved)$' -count=1
