package service

import (
	"context"
	"errors"
	"time"

	"gitcode-mcp/internal/cache"
)

const targetedPRCommentWriterWait = 5 * time.Second

// Exact readback waits for local admission before fetching. The lease remains
// held through publication, so contention cannot discard bytes or repeat HTTP.
// Collection and daemon policies are deliberately unchanged.
func (s *Service) acquireTargetedPRCommentWriter(ctx context.Context, repoID string, limit time.Duration) (context.Context, func(), error) {
	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	delay := 25 * time.Millisecond
	var last cache.ErrLockContention
	contended := false
	for {
		admitted, release, err := s.acquireBulkWriter(waitCtx, repoID, "bulk-sync-pr-comments")
		if err == nil {
			// Only admission is deadline-limited; canceling waitCtx on return must
			// not cancel the subsequent provider read or cache publication.
			return context.WithValue(ctx, bulkWriterAdmissionKey{}, admitted.Value(bulkWriterAdmissionKey{})), release, nil
		}
		if ctx.Err() != nil {
			return ctx, nil, ctx.Err()
		}
		if waitCtx.Err() != nil {
			if !contended {
				return ctx, nil, err
			}
			last.WaitExhausted = true
			return ctx, nil, last
		}
		if !errors.As(err, &last) {
			return ctx, nil, err
		}
		contended = true
		timer := time.NewTimer(delay)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			if ctx.Err() != nil {
				return ctx, nil, ctx.Err()
			}
			last.WaitExhausted = true
			return ctx, nil, last
		case <-timer.C:
		}
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
}
