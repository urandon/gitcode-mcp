package cache

import (
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("cache: not found")

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
