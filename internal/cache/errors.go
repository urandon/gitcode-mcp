package cache

import (
	"errors"
	"fmt"
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
}

func (e ErrLockContention) Error() string {
	if e.HolderHint == "" {
		return fmt.Sprintf("cache: lock contention at %s", e.Path)
	}
	return fmt.Sprintf("cache: lock contention at %s: %s", e.Path, e.HolderHint)
}

type ErrCacheCorruption struct {
	Path   string
	Detail string
}

func (e ErrCacheCorruption) Error() string {
	return fmt.Sprintf("cache: integrity check failed at %s: %s", e.Path, e.Detail)
}
