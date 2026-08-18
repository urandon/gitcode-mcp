package cache

import (
	"strings"
	"testing"
	"time"
)

func TestLockContentionPublicProjectionNeverFormatsPathsOrHints(t *testing.T) {
	secretPath := "/Users/private-user/workspace/cache.db.lock"
	secretDSN := "file:/Users/private-user/cache.db?token=secret#fragment"
	err := ErrLockContention{
		Path:       secretPath,
		CachePath:  secretDSN,
		HolderHint: "writer at " + secretPath + " uses token=secret",
		Operation:  "bulk-sync-issues",
		RepoID:     "owner/repo",
		StartedAt:  time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		PID:        42,
	}

	message := err.Error()
	for _, forbidden := range []string{secretPath, secretDSN, "private-user", "token=secret", "#fragment", err.HolderHint} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("public message leaked %q: %q", forbidden, message)
		}
	}
	for _, want := range []string{"cache_ref=cache-", "operation=bulk-sync-issues", "repo_id=owner/repo", "started_at=2026-08-18T12:00:00Z", "pid=42"} {
		if !strings.Contains(message, want) {
			t.Fatalf("public message %q missing %q", message, want)
		}
	}
	if got, again := err.PublicCacheRef(), err.PublicCacheRef(); got == "" || got != again || strings.Contains(got, "private-user") {
		t.Fatalf("PublicCacheRef() = %q, again %q", got, again)
	}
}
