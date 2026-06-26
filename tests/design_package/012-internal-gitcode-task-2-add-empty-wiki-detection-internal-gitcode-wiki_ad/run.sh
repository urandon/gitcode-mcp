#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

echo "=== Scenario 1: GET wiki/contents returns 400 or 404 -> empty_wiki diagnostic ==="

echo "--- 1a: 400 with wiki not found body -> ErrEmptyWiki ---"
go test -run "^TestEmptyWikiDetection400$" -count=1 "$REPO_ROOT/internal/gitcode/" -v 2>&1 | grep -E "PASS|FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: TestEmptyWikiDetection400 failed"
  exit 1
fi

echo "--- 1b: 404 with wiki is empty body -> ErrEmptyWiki ---"
go test -run "^TestEmptyWikiDetection404$" -count=1 "$REPO_ROOT/internal/gitcode/" -v 2>&1 | grep -E "PASS|FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: TestEmptyWikiDetection404 failed"
  exit 1
fi

echo "--- 1c: All 5 empty-wiki message variants map to ErrEmptyWiki ---"
go test -run "^TestEmptyWikiDetection404UninitializedMessage$" -count=1 "$REPO_ROOT/internal/gitcode/" -v 2>&1 | grep -E "PASS|FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: TestEmptyWikiDetection404UninitializedMessage failed"
  exit 1
fi

echo "=== Scenario 2: Create-page against empty wiki -> empty_wiki diagnostic ==="

echo "--- 2a: CreateWikiPage POST 400 wiki not found -> ErrEmptyWiki ---"
go test -run "^TestCreateWikiPageEmptyWikiDiagnostic$" -count=1 "$REPO_ROOT/internal/gitcode/" -v 2>&1 | grep -E "PASS|FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: TestCreateWikiPageEmptyWikiDiagnostic failed"
  exit 1
fi

echo "=== Scenario 3: Empty wiki response not classified as api_validation ==="

echo "--- 3a: 400 with non-empty-wiki body -> ErrAPIValidation ---"
go test -run "^TestEmptyWikiDetection400NonEmptyWiki$" -count=1 "$REPO_ROOT/internal/gitcode/" -v 2>&1 | grep -E "PASS|FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: TestEmptyWikiDetection400NonEmptyWiki failed"
  exit 1
fi

echo "--- 3b: 200 empty array [] is not empty-wiki ---"
go test -run "^TestEmptyWikiDetectionEmptyArray200IsOK$" -count=1 "$REPO_ROOT/internal/gitcode/" -v 2>&1 | grep -E "PASS|FAIL"
if [ "${PIPESTATUS[0]}" -ne 0 ]; then
  echo "FAIL: TestEmptyWikiDetectionEmptyArray200IsOK failed"
  exit 1
fi

echo "=== All 3 scenarios passed ==="
exit 0
