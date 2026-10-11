package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

const observationReadAttempts = 5

// The actual Admin Revision poller shares nonblocking observation locks.
// Retry reads only, and only their typed contention result. This never retries
// execution or converts an unavailable/invalid observation into success.
func retryObservationRead[T any](ctx context.Context, read func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; attempt < observationReadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := read()
		if err == nil {
			return value, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		var query observability.QueryError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			!errors.As(err, &query) || query.Code != "observation_busy" || attempt == observationReadAttempts-1 {
			return zero, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable observation read budget")
}

func cliObservationError(exit int, stdout, stderr []byte) error {
	if exit == 0 && len(stderr) == 0 {
		return nil
	}
	var failure struct {
		Class string `json:"failure_class"`
	}
	if exit != 0 && len(stdout) == 0 && json.Unmarshal(stderr, &failure) == nil && failure.Class == "observation_busy" {
		return observability.QueryError{Code: "observation_busy"}
	}
	return errors.New("actual CLI observation failed")
}

func mcpObservationResult(stdout, stderr []byte) (json.RawMessage, error) {
	var response struct {
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if len(stderr) == 0 && json.Unmarshal(stdout, &response) == nil {
		var failure struct {
			Data struct {
				Code string `json:"code"`
			} `json:"data"`
		}
		if len(response.Error) > 0 && len(response.Result) == 0 && json.Unmarshal(response.Error, &failure) == nil && failure.Data.Code == "observation_busy" {
			return nil, observability.QueryError{Code: "observation_busy"}
		}
		var result struct {
			Structured json.RawMessage `json:"structuredContent"`
		}
		if len(response.Error) == 0 && json.Unmarshal(response.Result, &result) == nil && len(result.Structured) > 0 {
			return result.Structured, nil
		}
	}
	return nil, errors.New("MCP observation returned failure")
}

func adminObservationError(status int, cacheControl string, data []byte) error {
	if cacheControl == "no-store" {
		if status == http.StatusOK {
			return nil
		}
		var failure struct {
			Error observability.QueryError `json:"error"`
		}
		if status == http.StatusServiceUnavailable && json.Unmarshal(data, &failure) == nil && failure.Error.Code == "observation_busy" {
			return observability.QueryError{Code: "observation_busy"}
		}
	}
	return errors.New("Admin observation failed its read contract")
}

func TestObservationReadRetryIsTypedAndBounded(t *testing.T) {
	busy := observability.QueryError{Code: "observation_busy", Reason: "metrics_busy"}
	for _, tc := range []struct {
		name      string
		failures  int
		err       error
		wantCalls int
	}{
		{"success", 0, nil, 1},
		{"typed_busy", 3, busy, 4},
		{"exhausted", 10, busy, observationReadAttempts},
		{"nonbusy", 10, observability.QueryError{Code: "observation_unavailable"}, 1},
		{"unknown", 10, observability.QueryError{Code: "future_busy"}, 1},
		{"raw_text", 10, errors.New("observation_busy: metrics_busy"), 1},
		{"cancelled", 10, errors.Join(context.Canceled, busy), 1},
		{"deadline", 10, errors.Join(context.DeadlineExceeded, busy), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := retryObservationRead(context.Background(), func() (int, error) {
				calls++
				if calls <= tc.failures {
					return 0, tc.err
				}
				return 42, nil
			})
			if calls != tc.wantCalls || (tc.failures < tc.wantCalls && (err != nil || got != 42)) ||
				(tc.failures >= tc.wantCalls && !errors.Is(err, tc.err)) {
				t.Fatal("typed read retry violated its result or exact attempt budget")
			}
		})
	}
	for _, duringRead := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if !duringRead {
			cancel()
		}
		calls := 0
		_, err := retryObservationRead(ctx, func() (int, error) {
			calls++
			cancel()
			return 0, busy
		})
		want := 0
		if duringRead {
			want = 1
		}
		if !errors.Is(err, context.Canceled) || calls != want {
			t.Fatal("cancelled observation read was retried")
		}
		cancel()
	}
}

func TestObservationTransportBusyRequiresStructuredEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func() error
		busy bool
	}{
		{"cli", func() error { return cliObservationError(4, nil, []byte(`{"failure_class":"observation_busy"}`)) }, true},
		{"cli_success_stderr", func() error { return cliObservationError(0, nil, []byte(`{"failure_class":"observation_busy"}`)) }, false},
		{"cli_output_and_error", func() error {
			return cliObservationError(4, []byte(`{}`), []byte(`{"failure_class":"observation_busy"}`))
		}, false},
		{"cli_raw", func() error { return cliObservationError(4, nil, []byte(`observation_busy`)) }, false},
		{"cli_other", func() error { return cliObservationError(4, nil, []byte(`{"failure_class":"observation_timeout"}`)) }, false},
		{"mcp", func() error {
			_, err := mcpObservationResult([]byte(`{"error":{"data":{"code":"observation_busy"}}}`), nil)
			return err
		}, true},
		{"mcp_both", func() error {
			_, err := mcpObservationResult([]byte(`{"error":{"data":{"code":"observation_busy"}},"result":{}}`), nil)
			return err
		}, false},
		{"mcp_null_result", func() error {
			_, err := mcpObservationResult([]byte(`{"error":{"data":{"code":"observation_busy"}},"result":null}`), nil)
			return err
		}, false},
		{"mcp_null_error", func() error {
			_, err := mcpObservationResult([]byte(`{"error":null,"result":{"structuredContent":{}}}`), nil)
			return err
		}, false},
		{"mcp_stderr", func() error {
			_, err := mcpObservationResult([]byte(`{"error":{"data":{"code":"observation_busy"}}}`), []byte(`failure`))
			return err
		}, false},
		{"mcp_message_only", func() error {
			_, err := mcpObservationResult([]byte(`{"error":{"message":"observation_busy"}}`), nil)
			return err
		}, false},
		{"mcp_other", func() error {
			_, err := mcpObservationResult([]byte(`{"error":{"data":{"code":"snapshot_required"}}}`), nil)
			return err
		}, false},
		{"admin", func() error {
			return adminObservationError(503, "no-store", []byte(`{"error":{"code":"observation_busy"}}`))
		}, true},
		{"admin_status", func() error {
			return adminObservationError(500, "no-store", []byte(`{"error":{"code":"observation_busy"}}`))
		}, false},
		{"admin_cache", func() error { return adminObservationError(503, "", []byte(`{"error":{"code":"observation_busy"}}`)) }, false},
		{"admin_raw", func() error { return adminObservationError(503, "no-store", []byte(`observation_busy`)) }, false},
		{"admin_other", func() error {
			return adminObservationError(503, "no-store", []byte(`{"error":{"code":"observation_unavailable"}}`))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			_, err := retryObservationRead(context.Background(), func() (int, error) { calls++; return 0, tc.read() })
			var query observability.QueryError
			gotBusy := errors.As(err, &query) && query.Code == "observation_busy"
			wantCalls := 1
			if tc.busy {
				wantCalls = observationReadAttempts
			}
			if err == nil || gotBusy != tc.busy || calls != wantCalls {
				t.Fatal("transport error escaped typed-busy-only handling")
			}
		})
	}
}
