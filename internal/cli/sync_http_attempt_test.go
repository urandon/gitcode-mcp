package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestCLISyncAggregateKeepsKnownHTTPAndInterruption(t *testing.T) {
	negative := diagnostics.RetainHTTPAttempt(&service.PartialSyncError{Diagnostic: service.SyncDiagnosticCancelled}, false)
	positive := diagnostics.RetainHTTPAttempt(&service.PartialSyncError{SuccessCount: 1}, true)
	result := &service.SyncResourcesResult{SuccessCount: 1}
	merged := mergeSyncError(negative, result, positive)
	partial, ok := merged.(*service.PartialSyncError)
	if !ok || partial.Diagnostic != service.SyncDiagnosticCancelled {
		t.Fatal("aggregate lost partial pointer or interruption")
	}
	summary := syncResourcesSummary(result, partial, time.Now())
	if summary.HTTPAttempted == nil || !*summary.HTTPAttempted {
		t.Fatal("partial success summary lost positive HTTP evidence")
	}
	d := diagnostics.Classify(merged, diagnosticContext(startupPlan{ProviderMode: "live-http"}, merged))
	if d.Code != diagnostics.CodeOperationCancelled || !d.HTTPAttempted {
		t.Fatal("late cancellation must retain prior HTTP without inventing a transport failure")
	}
}

func TestCLISyncLaterPreDispatchInterruptionKeepsPriorHTTP(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, entered := range []int{0, 1} {
			network := gitcode.ErrNetworkUnavailable{Attempts: entered, Cause: cause}
			failure := service.ErrSyncFailure{Mode: "network_timeout", Cause: network}
			partial := &service.PartialSyncError{Errors: []service.ResourceError{{Err: failure, FailureClass: "network_timeout"}}, SuccessCount: 1, FailureCount: 1}
			partial.Errors = append(partial.Errors, service.ResourceError{Err: diagnostics.RetainPreDispatchInterruption(cause)})
			partial.FailureCount++
			partial.PreserveHTTPAttemptEvidence(diagnostics.RetainHTTPAttempt(failure, true))
			d := diagnostics.Classify(partial, diagnosticContext(startupPlan{ProviderMode: "live-http"}, partial))
			want := diagnostics.CodeOperationCancelled
			if cause == context.DeadlineExceeded {
				want = diagnostics.CodeOperationTimeout
			}
			if entered != 0 {
				want = diagnostics.CodeLiveTransportFailure
			}
			if d.Code != want || !d.HTTPAttempted {
				t.Fatalf("partial interruption mismatch: entered=%d class=%s attempted=%t", entered, d.Code, d.HTTPAttempted)
			}
		}
	}
}

func TestCLISyncKnownNoHTTPJSONDiagnostic(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		var out bytes.Buffer
		err := diagnostics.RetainHTTPAttempt(cause, false)
		if writeCommandError(&out, "json", startupPlan{ProviderMode: "live-http"}, err) != 1 {
			t.Fatal("interruption must fail")
		}
		var d struct {
			FailureClass  string `json:"failure_class"`
			HTTPAttempted *bool  `json:"http_attempted"`
		}
		if json.Unmarshal(out.Bytes(), &d) != nil || d.HTTPAttempted == nil || *d.HTTPAttempted {
			t.Fatal("JSON diagnostic must explicitly report no HTTP attempt")
		}
		want := "operation_cancelled"
		if cause == context.DeadlineExceeded {
			want = "operation_timeout"
		}
		if d.FailureClass != want {
			t.Fatal("JSON diagnostic mislabeled pre-provider interruption")
		}
	}
}

func TestCLIContentionSanitizationKeepsHTTPReceipt(t *testing.T) {
	for _, attempted := range []bool{false, true} {
		var out bytes.Buffer
		err := diagnostics.RetainHTTPAttempt(cache.ErrLockContention{}, attempted)
		writeCommandError(&out, "json", startupPlan{ProviderMode: "live-http"}, err)
		var d struct {
			FailureClass  string `json:"failure_class"`
			HTTPAttempted *bool  `json:"http_attempted"`
		}
		if json.Unmarshal(out.Bytes(), &d) != nil || d.FailureClass != "cache_busy" || d.HTTPAttempted == nil || *d.HTTPAttempted != attempted {
			t.Fatal("public-safe contention projection discarded actual attempt evidence")
		}
	}
}
