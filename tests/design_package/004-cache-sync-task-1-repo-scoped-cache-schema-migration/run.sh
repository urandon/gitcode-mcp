#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/004-cache-sync-task-1-repo-scoped-cache-schema-migration"
WORK="$SCENARIO_DIR/.tmp-run"
LOCK_ARTIFACT="$ROOT/internal/service/gitcode-mcp-sync.lock"
rm -rf "$WORK"
mkdir -p "$WORK/helper" "$WORK/go-build-cache" "$WORK/go-tmp" "$WORK/tmp"
cleanup() {
  rm -f "$LOCK_ARTIFACT"
  rm -rf "$WORK"
}
trap cleanup EXIT

export GOCACHE="$WORK/go-build-cache"
export GOTMPDIR="$WORK/go-tmp"
export TMPDIR="$WORK/tmp"
export GITCODE_LIVE_TEST=0
rm -f "$LOCK_ARTIFACT"

initial_non_scenario_status="$(git -C "$ROOT" status --short --untracked-files=all | grep -Fv ' tests/design_package/004-cache-sync-task-1-repo-scoped-cache-schema-migration/' || true)"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

run_capture() {
  local name="$1"
  shift
  set +e
  "$@" >"$WORK/$name.out" 2>"$WORK/$name.err"
  local code=$?
  set -e
  if [[ "$code" != "0" ]]; then
    printf '%s\n' "--- $name stdout ---" >&2
    cat "$WORK/$name.out" >&2
    printf '%s\n' "--- $name stderr ---" >&2
    cat "$WORK/$name.err" >&2
    fail "$name exited $code"
  fi
}

assert_output_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$WORK/$name.out"; then
    fail "$name did not run expected evidence marker: $needle"
  fi
}

cat >"$WORK/helper/go.mod" <<EOF_GO_MOD
module scenario004

go 1.22

require gitcode-mcp v0.0.0

replace gitcode-mcp => $ROOT
EOF_GO_MOD

cat >"$WORK/helper/main.go" <<'EOF_GO'
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
)

type cacheStatus struct {
	RepoID          string `json:"repo_id"`
	WALCapable      bool   `json:"wal_capable"`
	JournalMode     string `json:"journal_mode"`
	Records         int    `json:"records"`
	Comments        int    `json:"comments"`
	IdentityAliases int    `json:"identity_aliases"`
	SyncEvents      int    `json:"sync_events"`
	AuditRows       int    `json:"audit_rows"`
	Snapshots       int    `json:"snapshots"`
	SnapshotChunks  int    `json:"snapshot_chunks"`
	Chunks          int    `json:"chunks"`
	RemoteRevisions int    `json:"remote_revisions"`
}

func main() {
	if len(os.Args) != 3 {
		fatalf("usage: helper <repo-root> <cache-path>")
	}
	root := os.Args[1]
	cachePath := os.Args[2]
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		fatalf("open migrated cache: %v", err)
	}
	if err := seed(ctx, store); err != nil {
		fatalf("seed fixture cache: %v", err)
	}
	if err := validateStore(ctx, store); err != nil {
		fatalf("validate store: %v", err)
	}
	if err := store.Close(); err != nil {
		fatalf("close seeded cache: %v", err)
	}
	if err := validateCLI(root, cachePath); err != nil {
		fatalf("validate cli cache-status: %v", err)
	}
	fmt.Println("scenario-004 helper passed")
}

func seed(ctx context.Context, store *cache.SQLiteStore) error {
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	repos := []cache.RepositoryBinding{
		{RepoID: "fixture-a", Owner: "public-owner-a", Name: "public-repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, DisplayName: "Fixture A", CreatedAt: now, UpdatedAt: now},
		{RepoID: "fixture-b", Owner: "public-owner-b", Name: "public-repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}, DisplayName: "Fixture B", CreatedAt: now, UpdatedAt: now},
	}
	for _, repo := range repos {
		if err := store.UpsertRepo(ctx, repo); err != nil {
			return err
		}
	}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{
		Record: cache.Record{RepoID: "fixture-a", ID: "ISSUE-42", Type: "issue", Path: "issues/42.md", Title: "Fixture issue A", Body: "sanitized issue body A", Status: "open", Labels: []string{"sanitized"}, ContentHash: "hash-record-a", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev-a", CreatedAt: now, UpdatedAt: now},
		Comments: []cache.RecordComment{{CommentID: "comment-a-1", Author: "agent-a", Body: "sanitized comment", ContentHash: "hash-comment-a", CreatedAt: now, UpdatedAt: now}},
		Identities: []cache.Identity{{AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}},
		RemoteRevisions: []cache.RemoteRevision{{RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev-a", Status: "synced", LastFetchedAt: now}},
		SyncEvents: []cache.SyncEvent{{ID: "sync-a-1", RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev-a", Status: "success", IdempotencyKey: "fixture-a-issue-42-rev-a", Message: "fixture sync", CreatedAt: now}},
		AuditTrail: []cache.AuditTrailEntry{{ID: "audit-a-1", Operation: "fixture-seed", RemoteType: "issue", RemoteID: "42", IdempotencyKey: "audit-a", Status: "success", Message: "fixture audit", PayloadHash: "hash-audit-a", CreatedAt: now}},
	}); err != nil {
		return err
	}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{
		Record: cache.Record{RepoID: "fixture-a", ID: "WIKI-HOME", Type: "wiki", Path: "wiki/Home.md", Title: "Fixture wiki", Body: "sanitized wiki body", Status: "current", Labels: []string{}, ContentHash: "hash-record-wiki", Provenance: cache.ProvenanceRemote, RemoteType: "wiki", RemoteID: "Home", RemoteRevision: "rev-wiki", CreatedAt: now, UpdatedAt: now},
		Identities: []cache.Identity{{AliasType: "wiki", Alias: "Home", Remote: cache.RemoteAlias{Type: "wiki", ID: "Home"}}},
	}); err != nil {
		return err
	}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{
		Record: cache.Record{RepoID: "fixture-b", ID: "ISSUE-42", Type: "issue", Path: "issues/42.md", Title: "Fixture issue B", Body: "sanitized issue body B", Status: "open", Labels: []string{"sanitized"}, ContentHash: "hash-record-b", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev-b", CreatedAt: now, UpdatedAt: now},
		Identities: []cache.Identity{{AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}},
	}); err != nil {
		return err
	}
	chunk, err := store.UpsertChunk(ctx, cache.Chunk{RepoID: "fixture-a", SourceID: "ISSUE-42", ContentHash: "hash-chunk-a", ByteStart: 0, ByteEnd: 24, LineStart: 1, LineEnd: 3, HeadingPath: []string{"Fixture"}, Text: "sanitized issue body A", NormalizedText: "sanitized issue body a", InheritedMetadata: map[string]string{"repo_id": "fixture-a"}, OutboundLinks: []string{}, ResolvedAliases: map[string]string{}})
	if err != nil {
		return err
	}
	return store.UpsertSnapshot(ctx, cache.Snapshot{RepoID: "fixture-a", ID: "snapshot-a", Format: "json", ContentHash: "hash-snapshot-a", RecordCount: 2, CreatedAt: now, Metadata: map[string]string{"fixture": "true"}, Chunks: []cache.SnapshotChunk{{ChunkID: chunk.ID, RecordID: "ISSUE-42", ByteStart: 0, ByteEnd: 24, LineStart: 1, LineEnd: 3, Citation: "issues/42.md:1-3", ContentHash: "hash-chunk-a"}}})
}

func validateStore(ctx context.Context, store *cache.SQLiteStore) error {
	a, err := store.ResolveRepoAlias(ctx, "fixture-a", cache.RemoteAlias{Type: "issue", ID: "42"})
	if err != nil {
		return fmt.Errorf("resolve fixture-a issue:42: %w", err)
	}
	if a.RepoID != "fixture-a" || a.SourceID != "ISSUE-42" {
		return fmt.Errorf("fixture-a scoped alias resolved to %#v", a)
	}
	b, err := store.ResolveRepoAlias(ctx, "fixture-b", cache.RemoteAlias{Type: "issue", ID: "42"})
	if err != nil {
		return fmt.Errorf("resolve fixture-b issue:42: %w", err)
	}
	if b.RepoID != "fixture-b" || b.SourceID != "ISSUE-42" {
		return fmt.Errorf("fixture-b scoped alias resolved to %#v", b)
	}
	_, err = store.ResolveAlias(ctx, cache.RemoteAlias{Type: "issue", ID: "42"})
	if err == nil {
		return fmt.Errorf("unscoped issue:42 resolved successfully; expected typed conflict or repo_id-required error")
	}
	var aliasConflict cache.ErrAliasConflict
	var unscoped cache.ErrUnscopedAliasResolution
	if !errors.As(err, &aliasConflict) && !errors.As(err, &unscoped) {
		return fmt.Errorf("unscoped issue:42 returned %T %v, expected typed alias conflict or unscoped alias error", err, err)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		return err
	}
	if counts.Records != 2 || counts.Comments != 1 || counts.IdentityAliases != 2 || counts.SyncEvents != 1 || counts.AuditRows != 1 || counts.Snapshots != 1 || counts.SnapshotChunks != 1 || counts.Chunks != 1 || counts.RemoteRevisions != 1 {
		return fmt.Errorf("unexpected fixture-a store counts: %#v", counts)
	}
	wal, mode, err := store.WALCapable(ctx)
	if err != nil {
		return err
	}
	if !wal || (mode != "wal" && mode != "memory") {
		return fmt.Errorf("cache is not WAL-capable: wal=%t mode=%q", wal, mode)
	}
	return nil
}

func validateCLI(root, cachePath string) error {
	outA, err := runCLI(root, cachePath, "fixture-a")
	if err != nil {
		return err
	}
	if outA.RepoID != "fixture-a" || !outA.WALCapable || (outA.JournalMode != "wal" && outA.JournalMode != "memory") {
		return fmt.Errorf("fixture-a CLI WAL metadata invalid: %#v", outA)
	}
	if outA.Records != 2 || outA.Comments != 1 || outA.IdentityAliases != 2 || outA.SyncEvents != 1 || outA.AuditRows != 1 || outA.Snapshots != 1 || outA.SnapshotChunks != 1 || outA.Chunks != 1 || outA.RemoteRevisions != 1 {
		return fmt.Errorf("fixture-a CLI counts invalid: %#v", outA)
	}
	outB, err := runCLI(root, cachePath, "fixture-b")
	if err != nil {
		return err
	}
	if outB.RepoID != "fixture-b" || outB.Records != 1 || outB.Comments != 0 || outB.IdentityAliases != 1 || outB.SyncEvents != 0 || outB.AuditRows != 0 || outB.Snapshots != 0 || outB.SnapshotChunks != 0 || outB.Chunks != 0 || outB.RemoteRevisions != 0 {
		return fmt.Errorf("fixture-b CLI counts are not repo-scoped: %#v", outB)
	}
	return nil
}

func runCLI(root, cachePath, repoID string) (cacheStatus, error) {
	cmd := exec.Command("go", "run", "./cmd/gitcode-mcp", "--cache-path", cachePath, "cache-status", "--repo", repoID, "--format", "json")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GITCODE_LIVE_TEST=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cacheStatus{}, fmt.Errorf("cache-status %s failed: %w\n%s", repoID, err, string(out))
	}
	var status cacheStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return cacheStatus{}, fmt.Errorf("decode cache-status %s JSON: %w\n%s", repoID, err, string(out))
	}
	return status, nil
}

func fatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	msg = strings.ReplaceAll(msg, filepath.Clean(os.TempDir()), "<tmp>")
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
EOF_GO

cd "$ROOT"

run_capture scenario-helper go run "$WORK/helper/main.go" "$ROOT" "$WORK/cache.db"
assert_output_contains scenario-helper 'scenario-004 helper passed'

run_capture targeted-cache-tests go test ./internal/cache -run 'TestSchemaVersion|TestRepoScopedCacheMigrationConstraints|TestRepoScopedRecordGraphCountsSnapshotsAndAliases|TestRecordProvenanceRemoteCanonical|TestIdentityResolution' -count=1 -v
assert_output_contains targeted-cache-tests 'TestSchemaVersion'
assert_output_contains targeted-cache-tests 'TestRepoScopedCacheMigrationConstraints'
assert_output_contains targeted-cache-tests 'TestRepoScopedRecordGraphCountsSnapshotsAndAliases'
assert_output_contains targeted-cache-tests 'TestRecordProvenanceRemoteCanonical'
assert_output_contains targeted-cache-tests 'TestIdentityResolution'

run_capture targeted-cli-tests go test ./internal/cli -run 'TestCacheStatusJSON' -count=1 -v
assert_output_contains targeted-cli-tests 'TestCacheStatusJSON'

run_capture required-offline-suite go test ./...
run_capture diff-check git diff --check
rm -f "$LOCK_ARTIFACT"
rm -rf "$WORK"

status_output="$(git status --short --untracked-files=all)"
non_scenario_status="$(printf '%s\n' "$status_output" | grep -Fv ' tests/design_package/004-cache-sync-task-1-repo-scoped-cache-schema-migration/' || true)"
if [[ "$non_scenario_status" != "$initial_non_scenario_status" ]]; then
  printf '%s\n' "$status_output" >&2
  fail 'validation modified files outside scenario directory'
fi

printf 'PASS: repo-scoped cache schema migration validation scenarios passed offline\n'
