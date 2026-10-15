package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestForegroundSyncPolicy(t *testing.T) {
	for _, tc := range []struct {
		pages, records, perPage int
		preview                 bool
	}{{0, 0, 0, true}, {0, 0, 25, true}, {2, 0, 0, false}, {0, 40, 25, false}, {2, 40, 25, false}} {
		req, err := ForegroundSyncRequest("fixture", " key ", tc.pages, tc.records, tc.perPage)
		if err != nil || req.Bounds == nil || req.IdempotencyKey != "key" {
			t.Fatal("shared request contract missing")
		}
		if tc.preview && (req.Bounds.MaxListPages != 1 || req.Bounds.MaxPages != 1 || req.Bounds.MaxRecords != req.PerPage) {
			t.Fatal("preview is not a finite slice")
		}
		if !tc.preview && (req.Bounds.MaxListPages != 0 || req.Bounds.MaxPages != tc.pages || req.Bounds.MaxRecords != tc.records) {
			t.Fatal("explicit coverage bounds changed")
		}
	}
	for _, tc := range [][3]int{{-1, 0, 0}, {0, -1, 0}, {0, 0, -1}, {0, 0, 101}, {int(^uint(0) >> 1), 0, 100}} {
		_, err := ForegroundSyncRequest("fixture", "", tc[0], tc[1], tc[2])
		var invalid ErrInvalidQuery
		if !errors.As(err, &invalid) {
			t.Fatal("missing typed invalid bound error")
		}
	}
}

func TestForegroundSyncContextBudget(t *testing.T) {
	start := time.Now()
	ctx, cancel := ForegroundSyncContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(start.Add(DefaultForegroundSyncTimeout)) || deadline.After(time.Now().Add(DefaultForegroundSyncTimeout)) {
		t.Fatal("omitted operation deadline is not bounded")
	}
	for _, duration := range []time.Duration{time.Second, time.Minute} {
		parent, stop := context.WithTimeout(context.Background(), duration)
		child, done := ForegroundSyncContext(parent)
		a, _ := parent.Deadline()
		b, _ := child.Deadline()
		if !a.Equal(b) {
			t.Fatal("explicit caller deadline changed")
		}
		stop()
		if !errors.Is(child.Err(), context.Canceled) {
			t.Fatal("caller cancellation was lost")
		}
		done()
	}
}

func TestSyncTraversalAggregationPreservesIncompleteSibling(t *testing.T) {
	result := &SyncResourcesResult{}
	MergeSyncTraversal(result, &SyncResourcesResult{TraversalStatus: "bounded", StopReason: "max_list_pages", PagesListed: 1, RecordsListed: 100})
	MergeSyncTraversal(result, &SyncResourcesResult{TraversalStatus: "complete", StopReason: "end_of_collection", PagesListed: 1, RecordsListed: 2})
	if result.TraversalStatus != "mixed" || result.StopReason != "mixed" || result.PagesListed != 2 || result.RecordsListed != 102 || SyncContinuation(result) == "" {
		t.Fatal("complete sibling hid bounded coverage")
	}
}

func TestSyncTraversalAggregationIncludesFailureAndNilInterruption(t *testing.T) {
	for _, tc := range []struct {
		name string
		part *SyncResourcesResult
		err  error
	}{
		{"failure without traversal", &SyncResourcesResult{FailureCount: 1}, nil},
		{"stale complete marker", &SyncResourcesResult{TraversalStatus: "complete", StopReason: "end_of_collection", FailureCount: 1}, nil},
		{"nil cancellation", nil, &PartialSyncError{Diagnostic: SyncDiagnosticCancelled}},
		{"nil timeout", nil, &PartialSyncError{Diagnostic: SyncDiagnosticTimeout}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := &SyncResourcesResult{TraversalStatus: "complete", StopReason: "end_of_collection"}
			MergeSyncTraversal(result, tc.part, tc.err)
			if result.TraversalStatus == "complete" || result.StopReason == "end_of_collection" || SyncContinuation(result) == "" {
				t.Fatal("failed sibling retained a complete traversal receipt")
			}
		})
	}
}
