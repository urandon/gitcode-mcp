package gitcode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func TestProviderExecutionEventsActualAttemptsWithoutMetrics(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, class string
		status               int
		err                  error
	}{
		{"success", "succeeded", "", 200, nil},
		{"forbidden", "forbidden", "forbidden", 403, nil},
		{"rate_limit", "rate_limited", "rate_limited", 429, nil},
		{"invalid", "invalid", "invalid", 422, nil},
		{"server", "failed", "provider_failed", 503, nil},
		{"cancel", "cancelled", "cancelled", 0, context.Canceled},
		{"timeout", "timeout", "timeout", 0, context.DeadlineExceeded},
		{"transport", "failed", "provider_failed", 0, errors.New("synthetic-private-error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := observability.New(observability.Config{Directory: filepath.Join(t.TempDir(), "observation"), ManagedOutput: true})
			deadline := time.Now().Add(5 * time.Second)
			for c.Coverage().State == "initializing" {
				if time.Now().After(deadline) {
					t.Fatal("collector fixture unavailable")
				}
				time.Sleep(time.Millisecond)
			}
			if c.Coverage().State != "ready" {
				t.Fatal("collector fixture unavailable")
			}
			calls := 0
			client, _, _ := observedProviderFixture(t, Config{}, func(*http.Request) (*http.Response, error) {
				calls++
				if tc.err != nil {
					return nil, tc.err
				}
				return providerFixtureResponse(tc.status, "[]"), nil
			})
			ctx := observability.WithObservation(context.Background(), c, observability.Context{Job: observability.KnownRef("job", "fixture"), Correlation: observability.KnownRef("correlation", "fixture")})
			_, _, _ = client.getBytes(ctx, "/synthetic-private-route", nil)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, _, _ = client.getBytes(cancelled, "/synthetic-private-route", nil)
			if calls != 1 {
				t.Fatal("pre-transport rejection or telemetry added traffic")
			}
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			if !c.Close(closeCtx, true) {
				t.Fatal("collector fixture did not flush")
			}
			page, err := c.Query(observability.Filter{Code: observability.ProviderAttempt}, "", 20)
			if err != nil || uint64(len(page.Events))+page.Coverage.Dropped != 1 {
				t.Fatal("provider boundary missing or duplicated")
			}
			for _, e := range page.Events {
				if e.Phase != "fetch" || e.Outcome != tc.outcome || e.FailureClass != tc.class || e.Attempt != 1 || e.JobRef == "" || e.CorrelationRef == "" {
					t.Fatal("provider finite execution dimensions changed")
				}
			}
			data, _ := json.Marshal(page)
			if strings.Contains(string(data), "synthetic-private") || strings.Contains(string(data), "example.invalid") {
				t.Fatal("provider execution captured private request data")
			}
		})
	}
}
