package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("cache: not found")

type ErrUnscopedAliasResolution struct {
	Alias string
}

func (e ErrUnscopedAliasResolution) Error() string {
	if e.Alias == "" {
		return "cache: unscoped alias resolution requires repo_id"
	}
	return fmt.Sprintf("cache: unscoped alias resolution requires repo_id for %s", e.Alias)
}

type ErrAliasConflict struct {
	Alias string
	Repos []string
}

func (e ErrAliasConflict) Error() string {
	if len(e.Repos) == 0 {
		return fmt.Sprintf("cache: alias conflict for %s", e.Alias)
	}
	return fmt.Sprintf("cache: alias conflict for %s in repositories %v", e.Alias, e.Repos)
}

type ErrLockContention struct {
	Path       string
	HolderHint string
	Operation  string
	RepoID     string
	StartedAt  time.Time
	PID        int
	CacheRef   string
	CachePath  string
}

func (e ErrLockContention) Error() string {
	details := make([]string, 0, 5)
	if ref := e.PublicCacheRef(); ref != "" {
		details = append(details, "cache_ref="+ref)
	}
	if operation := e.PublicOperation(); operation != "" {
		details = append(details, "operation="+operation)
	}
	if repoID := e.PublicRepoID(); repoID != "" {
		details = append(details, "repo_id="+repoID)
	}
	if !e.StartedAt.IsZero() {
		details = append(details, "started_at="+e.StartedAt.UTC().Format(time.RFC3339Nano))
	}
	if e.PID > 0 {
		details = append(details, fmt.Sprintf("pid=%d", e.PID))
	}
	if len(details) == 0 {
		return "cache: writer lock is held"
	}
	return "cache: writer lock is held (" + strings.Join(details, ", ") + ")"
}

func (e ErrLockContention) DiagnosticCode() string { return "cache_busy" }

// PublicCacheRef returns a deterministic opaque correlation id without
// exposing the lock path, cache path, DSN, query parameters, or fragments.
func (e ErrLockContention) PublicCacheRef() string {
	for _, value := range []string{e.CacheRef, e.CachePath, e.Path} {
		if ref := OpaqueCacheRef(value); ref != "" {
			return ref
		}
	}
	return ""
}

// PublicOperation returns a bounded identifier or omits hostile legacy owner data.
func (e ErrLockContention) PublicOperation() string {
	value := strings.TrimSpace(e.Operation)
	if !validPublicLockComponent(value, 64) {
		return ""
	}
	return value
}

// PublicRepoID returns a bounded local or owner/repository identifier.
func (e ErrLockContention) PublicRepoID() string {
	value := strings.TrimSpace(e.RepoID)
	if value == "" || len(value) > 255 || strings.Count(value, "/") > 1 {
		return ""
	}
	for _, component := range strings.Split(value, "/") {
		if !validPublicLockComponent(component, 128) || component == "." || component == ".." {
			return ""
		}
	}
	return value
}

func validPublicLockComponent(value string, maxLen int) bool {
	if value == "" || len(value) > maxLen {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		if i > 0 && (r == '-' || r == '_' || r == '.') {
			continue
		}
		return false
	}
	return true
}

// OpaqueCacheRef turns private cache identity material into a stable public correlation id.
func OpaqueCacheRef(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "cache-" + hex.EncodeToString(sum[:8])
}

type ErrCacheCorruption struct {
	Path   string
	Detail string
}

func (e ErrCacheCorruption) Error() string {
	return fmt.Sprintf("cache: integrity check failed at %s: %s", e.Path, e.Detail)
}
