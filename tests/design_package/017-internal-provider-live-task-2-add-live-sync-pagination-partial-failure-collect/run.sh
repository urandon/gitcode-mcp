#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

export GITCODE_LIVE_TEST=""
export GITCODE_TOKEN=""

go test ./internal/service -run 'TestBulkSyncIssuesSyncsListedIssuesAndZeroDeltaOnResync|TestBulkSyncWikiPartialFailureCollectsSuccessAndFailure' -count=1
