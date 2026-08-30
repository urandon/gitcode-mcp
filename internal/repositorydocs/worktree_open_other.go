//go:build plan9

package repositorydocs

import (
	"context"
	"os"
)

func readWorktreeFileNoFollow(ctx context.Context, root, repoPath string, maxBytes int64) ([]byte, int64, error) {
	return nil, 0, os.ErrPermission
}

func readWorktreeFileNoFollowAfterOpen(ctx context.Context, root, repoPath string, maxBytes int64, afterOpen func()) ([]byte, int64, error) {
	return nil, 0, os.ErrPermission
}
