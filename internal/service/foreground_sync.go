package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

const DefaultForegroundSyncPerPage = 100
const DefaultForegroundSyncTimeout = 30 * time.Second

// ForegroundSyncRequest defines public CLI/MCP collection omission semantics.
// Explicit coverage bounds keep their historical meaning. A preview additionally
// limits actual list traffic so cached-prefix scans cannot turn it into backfill.
func ForegroundSyncRequest(repoID, key string, maxPages, maxRecords, perPage int) (BulkSyncRequest, error) {
	if maxPages < 0 || maxRecords < 0 || perPage < 0 || perPage > 100 {
		return BulkSyncRequest{}, ErrInvalidQuery{Field: "bounds", Message: "max_pages and max_records must be non-negative; per_page must be between 1 and 100 when supplied"}
	}
	if perPage == 0 {
		perPage = DefaultForegroundSyncPerPage
	}
	if maxPages > int(^uint(0)>>1)/perPage {
		return BulkSyncRequest{}, ErrInvalidQuery{Field: "max_pages", Message: "max_pages times per_page exceeds the supported work bound"}
	}
	bounds := &SyncBounds{MaxPages: maxPages, MaxRecords: maxRecords}
	if maxPages == 0 && maxRecords == 0 {
		bounds.MaxPages, bounds.MaxRecords, bounds.MaxListPages = 1, perPage, 1
	}
	return BulkSyncRequest{RepoID: repoID, IdempotencyKey: strings.TrimSpace(key), PerPage: perPage, Bounds: bounds}, nil
}

// ForegroundSyncContext preserves an explicit caller budget; bare stdio/API
// callers receive the same finite operation default as the CLI entrypoint.
func ForegroundSyncContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, DefaultForegroundSyncTimeout)
}

// MergeSyncTraversal retains bounded/partial evidence when a transport combines
// collections. A complete sibling must never erase an unfinished collection.
func MergeSyncTraversal(dst, src *SyncResourcesResult, operationErrors ...error) {
	if dst == nil {
		return
	}
	var operationErr error
	for _, err := range operationErrors {
		if err != nil {
			operationErr = err
			break
		}
	}
	if src == nil {
		if operationErr == nil {
			return
		}
		src = &SyncResourcesResult{}
	}
	// Coverage fields alone cannot prove success: early errors may return no
	// result, and older collection implementations may leave a complete marker
	// beside resource failures. Do not mutate the caller's resource receipt.
	coverage := *src
	if (coverage.TraversalStatus == "" || coverage.TraversalStatus == "complete") && (operationErr != nil || src.FailureCount > 0 || len(src.Failures) > 0) {
		coverage.TraversalStatus, coverage.StopReason = "partial", "sync_error"
		var partial *PartialSyncError
		if errors.As(operationErr, &partial) {
			switch partial.Diagnostic {
			case SyncDiagnosticCancelled:
				coverage.TraversalStatus, coverage.StopReason = "cancelled", "cancelled"
			case SyncDiagnosticTimeout:
				coverage.TraversalStatus, coverage.StopReason = "timeout", "timeout"
			}
		}
	}
	src = &coverage
	dst.PagesListed += src.PagesListed
	dst.RecordsListed += src.RecordsListed
	dst.SkippedByWatermark += src.SkippedByWatermark
	merge := func(dst *string, value string) {
		if value == "" {
			return
		}
		if *dst == "" {
			*dst = value
		} else if *dst != value {
			*dst = "mixed"
		}
	}
	merge(&dst.StopReason, src.StopReason)
	merge(&dst.Ordering, src.Ordering)
	merge(&dst.TraversalStatus, src.TraversalStatus)
	merge(&dst.WatermarkStatus, src.WatermarkStatus)
	merge(&dst.WatermarkReason, src.WatermarkReason)
}

func SyncContinuation(result *SyncResourcesResult) string {
	if result == nil || result.TraversalStatus == "" || result.TraversalStatus == "complete" {
		return ""
	}
	return "inspect_status_then_restart_from_head_with_explicit_bounds_or_use_maintenance"
}
