//go:build darwin || linux

package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func executionFixtureEvent(code Code, details ExecutionDetails) Event {
	t := catalog[code]
	return Event{Schema: 1, EventID: strings.Repeat("a", 32), BootID: strings.Repeat("b", 32), Sequence: 1, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Stream: t.stream, Severity: t.severity, Component: t.component, Code: code, Message: t.message,
		JobRef: KnownRef("job", "fixture-job").value, CorrelationRef: KnownRef("correlation", "fixture-job").value, ExecutionDetails: details}
}

func TestExecutionCatalogFiniteCompatibility(t *testing.T) {
	cases := []struct {
		code    Code
		details ExecutionDetails
	}{
		{JobAdmitted, ExecutionDetails{Phase: "admission", Outcome: "queued"}},
		{JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "succeeded"}},
		{JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "failed", FailureClass: "operation_failed"}},
		{JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "cancelled", FailureClass: "cancelled"}},
		{JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "interrupted", FailureClass: "interrupted"}},
		{JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "superseded", FailureClass: "superseded"}},
		{ProviderAttempt, ExecutionDetails{Phase: "fetch", Outcome: "succeeded"}},
		{ProviderAttempt, ExecutionDetails{Phase: "embedding", Outcome: "succeeded", Attempt: MaxExecutionAttempt}},
		{ProviderAttempt, ExecutionDetails{Phase: "fetch", Collection: "wiki", Outcome: "failed", FailureClass: "provider_failed", Attempt: 2}},
		{SyncCommitted, ExecutionDetails{Phase: "commit", Collection: "issues", Outcome: "succeeded"}},
		{RetryAdmitted, ExecutionDetails{Phase: "retry", Collection: "wiki", Outcome: "admitted", Attempt: 2}},
		{RetryAdmitted, ExecutionDetails{Phase: "retry", Collection: "issues", Outcome: "admitted", Attempt: MaxExecutionAttempt}},
		{WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "sync", Outcome: "denied", FailureClass: "writer_busy"}},
		{WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "rag", Outcome: "denied", FailureClass: "writer_busy"}},
		{WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "repository_docs", Outcome: "denied", FailureClass: "writer_busy"}},
		{WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "other", Outcome: "denied", FailureClass: "writer_busy"}},
	}
	for _, outcome := range []string{"cancelled", "timeout", "rate_limited", "forbidden", "invalid"} {
		cases = append(cases, struct {
			code    Code
			details ExecutionDetails
		}{ProviderAttempt, ExecutionDetails{Phase: "fetch", Outcome: outcome, FailureClass: outcome}})
	}
	for _, failure := range []string{"timeout", "rate_limited", "forbidden", "invalid", "provider_failed"} {
		cases = append(cases, struct {
			code    Code
			details ExecutionDetails
		}{JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "failed", FailureClass: failure}})
	}
	for _, tc := range cases {
		t.Run(string(tc.code)+"/"+tc.details.Phase+"/"+tc.details.Outcome, func(t *testing.T) {
			e := executionFixtureEvent(tc.code, tc.details)
			if tc.code == WriterContended && tc.details.WorkKind == "other" {
				e.JobRef = "" // direct/pre-admission denial must not invent a job
			}
			b, err := marshalEvent(e)
			var decoded Event
			if err != nil || decodeStrict(b, &decoded) != nil || !decoded.valid() || decoded != e {
				t.Fatal("finite execution event did not round-trip")
			}
			for _, mutate := range []func(*Event){
				func(e *Event) { e.Phase = "synthetic-secret" },
				func(e *Event) { e.Collection = "synthetic-secret" },
				func(e *Event) { e.WorkKind = "synthetic-secret" },
				func(e *Event) { e.Outcome = "synthetic-secret" },
				func(e *Event) { e.FailureClass = "synthetic-secret" },
				func(e *Event) { e.Attempt = MaxExecutionAttempt + 1 },
				func(e *Event) { e.CorrelationRef = "" },
				func(e *Event) { e.CorrelationRef = "synthetic-secret" },
				func(e *Event) { e.Legacy = true },
				func(e *Event) { e.Schema = 99 },
			} {
				invalid := e
				mutate(&invalid)
				if b, err := marshalEvent(invalid); err == nil || len(b) != 0 || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatal("invalid execution content escaped the catalog")
				}
			}
		})
	}
}

func TestExecutionCatalogRejectsCrossCodeCombinations(t *testing.T) {
	invalid := []Event{
		executionFixtureEvent(JobAdmitted, ExecutionDetails{Phase: "admission", Outcome: "succeeded"}),
		executionFixtureEvent(JobAdmitted, ExecutionDetails{Phase: "admission", Outcome: "queued", Attempt: 1}),
		executionFixtureEvent(JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "succeeded", FailureClass: "operation_failed"}),
		executionFixtureEvent(JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "cancelled", FailureClass: "timeout"}),
		executionFixtureEvent(JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "failed", FailureClass: "interrupted"}),
		executionFixtureEvent(JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "failed", FailureClass: "superseded"}),
		executionFixtureEvent(JobTerminal, ExecutionDetails{Phase: "completion", Outcome: "superseded", FailureClass: "interrupted"}),
		executionFixtureEvent(ProviderAttempt, ExecutionDetails{Phase: "completion", Outcome: "succeeded"}),
		executionFixtureEvent(ProviderAttempt, ExecutionDetails{Phase: "embedding", Collection: "issues", Outcome: "succeeded"}),
		executionFixtureEvent(ProviderAttempt, ExecutionDetails{Phase: "fetch", Outcome: "timeout", FailureClass: "provider_failed"}),
		executionFixtureEvent(SyncCommitted, ExecutionDetails{Phase: "commit", Outcome: "succeeded"}),
		executionFixtureEvent(SyncCommitted, ExecutionDetails{Phase: "commit", Collection: "wiki", Outcome: "succeeded", Attempt: 1}),
		executionFixtureEvent(RetryAdmitted, ExecutionDetails{Phase: "retry", Collection: "wiki", Outcome: "admitted", Attempt: 1}),
		executionFixtureEvent(RetryAdmitted, ExecutionDetails{Phase: "retry", Collection: "wiki", Outcome: "admitted"}),
		executionFixtureEvent(RetryAdmitted, ExecutionDetails{Phase: "retry", Collection: "wiki", Outcome: "succeeded", Attempt: 2}),
		executionFixtureEvent(RetryAdmitted, ExecutionDetails{Phase: "fetch", Collection: "wiki", Outcome: "admitted", Attempt: 2}),
		executionFixtureEvent(RetryAdmitted, ExecutionDetails{Phase: "retry", Outcome: "admitted", Attempt: 2}),
		executionFixtureEvent(WriterContended, ExecutionDetails{Phase: "writer", Outcome: "denied", FailureClass: "writer_busy"}),
		executionFixtureEvent(WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "sync", Outcome: "denied", FailureClass: "writer_busy", Attempt: 1}),
		executionFixtureEvent(WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "sync", Outcome: "denied", FailureClass: "writer_busy", Collection: "issues"}),
		executionFixtureEvent(WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "sync", Outcome: "failed", FailureClass: "writer_busy"}),
		executionFixtureEvent(WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "sync", Outcome: "denied", FailureClass: "operation_failed"}),
		executionFixtureEvent(WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "sync", Outcome: "denied", FailureClass: "writer_busy", Attempt: MaxExecutionAttempt}),
		executionFixtureEvent(JobAdmitted, ExecutionDetails{Phase: "admission", WorkKind: "sync", Outcome: "queued"}),
		executionFixtureEvent(Boot, ExecutionDetails{WorkKind: "sync"}),
		executionFixtureEvent(Boot, ExecutionDetails{Phase: "fetch", Outcome: "succeeded"}),
	}
	retry := executionFixtureEvent(RetryAdmitted, ExecutionDetails{Phase: "retry", Collection: "wiki", Outcome: "admitted", Attempt: 2})
	retry.JobRef = ""
	if retry.valid() {
		t.Fatal("retry event accepted without resolved job")
	}
	for _, e := range invalid {
		if e.valid() {
			t.Fatal("cross-code execution combination accepted")
		}
	}
	for _, code := range []Code{JobAdmitted, JobTerminal, SyncCommitted} {
		details := ExecutionDetails{Phase: "admission", Outcome: "queued"}
		if code == JobTerminal {
			details = ExecutionDetails{Phase: "completion", Outcome: "succeeded"}
		} else if code == SyncCommitted {
			details = ExecutionDetails{Phase: "commit", Collection: "wiki", Outcome: "succeeded"}
		}
		e := executionFixtureEvent(code, details)
		e.JobRef = ""
		if e.valid() {
			t.Fatal("job-bound event accepted without resolved job")
		}
	}
	old := executionFixtureEvent(Boot, ExecutionDetails{})
	b, err := marshalEvent(old)
	if err != nil || strings.Contains(string(b), "phase") || strings.Contains(string(b), "attempt") {
		t.Fatal("old lifecycle representation changed")
	}
	var decoded Event
	b = append(append([]byte(nil), b[:len(b)-2]...), []byte(",\"unknown_execution_field\":\"synthetic-secret\"}\n")...)
	if decodeStrict(b, &decoded) == nil {
		t.Fatal("unknown field accepted during replay")
	}
}

func TestExecutionContextIsolationAndNonblockingAdmission(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	t.Cleanup(func() { flush(t, c, true) })
	refs := Context{Job: KnownRef("job", "fixture-job"), Correlation: KnownRef("correlation", "fixture-job"),
		ExecutionDetails: ExecutionDetails{Phase: "synthetic-secret"}}
	deadline := time.Now().Add(time.Minute)
	parent, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	ctx := WithObservation(parent, c, refs)
	refs.Job = Ref{"synthetic-secret"} // carrier must contain a value copy
	if WithObservation(parent, nil, refs) != parent {
		t.Fatal("nil collector changed execution context")
	}
	if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatal("observation changed execution deadline")
	}
	details := ExecutionDetails{Phase: "admission", Outcome: "queued"}
	if EmitFromContext(context.Background(), JobAdmitted, details) {
		t.Fatal("unrelated context acquired an observation binding")
	}
	// A cancelled execution still reports its confirmed terminal boundary;
	// instrumentation neither clears cancellation nor suppresses that evidence.
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("observation disconnected parent cancellation")
	}
	await(t, func() bool { return EmitFromContext(ctx, JobAdmitted, details) })
	before := c.Coverage()
	c.mu.Lock()
	done := make(chan bool, 1)
	go func() {
		done <- EmitFromContext(ctx, ProviderAttempt, ExecutionDetails{Phase: "fetch", Outcome: "succeeded"})
	}()
	select {
	case accepted := <-done:
		c.mu.Unlock()
		if accepted {
			t.Fatal("held collector admitted an event")
		}
	case <-time.After(time.Second):
		c.mu.Unlock()
		t.Fatal("observation waited for a telemetry reader")
	}
	if got := c.Coverage(); got.Dropped != before.Dropped+1 || got.LossEpoch != before.LossEpoch+1 {
		t.Fatal("context rejection did not expose its loss boundary")
	}
	flush(t, c, true)
	p, err := c.Query(Filter{Code: JobAdmitted, Component: "jobs"}, "", 10)
	if err != nil || len(p.Events) != 1 || p.Events[0].JobRef != KnownRef("job", "fixture-job").value || p.Events[0].ExecutionDetails != details {
		t.Fatal("copied correlation or execution details changed")
	}
}

func TestExecutionLedgerRestartPrivacyAndLegacyBoundary(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	t.Cleanup(func() { flush(t, c, true) })
	sentinel := "synthetic-private-coordinate-cookie-body"
	refs := Context{Job: KnownRef("job", sentinel), Registration: KnownRef("registration", sentinel), Cache: KnownRef("cache", sentinel), Repo: KnownRef("repo", sentinel), Correlation: KnownRef("correlation", sentinel),
		ExecutionDetails: ExecutionDetails{Phase: "fetch", Collection: "wiki", Outcome: "rate_limited", FailureClass: "rate_limited", Attempt: 7}}
	if c.Emit(ProviderAttempt, Context{Correlation: refs.Correlation, ExecutionDetails: ExecutionDetails{Phase: sentinel}}) {
		t.Fatal("raw execution details admitted")
	}
	admitFixture(t, c, ProviderAttempt, refs)
	writer := refs
	writer.ExecutionDetails = ExecutionDetails{Phase: "writer", WorkKind: "repository_docs", Outcome: "denied", FailureClass: "writer_busy"}
	admitFixture(t, c, WriterContended, writer)
	admitFixture(t, c, RecoveryFinished, Context{})
	flush(t, c, true)
	restarted := New(Config{Directory: path, ManagedOutput: true})
	ready(t, restarted)
	t.Cleanup(func() { flush(t, restarted, true) })
	flush(t, restarted, true)
	p, err := restarted.Query(Filter{Component: "provider", Code: ProviderAttempt}, "", 10)
	if err != nil || len(p.Events) != 1 || p.Events[0].ExecutionDetails != refs.ExecutionDetails || p.Events[0].CorrelationRef != refs.Correlation.value {
		t.Fatal("execution fields did not survive clean restart")
	}
	if lifecycle, err := restarted.Query(Filter{Code: RecoveryFinished}, "", 10); err != nil || len(lifecycle.Events) != 1 {
		t.Fatal("old lifecycle record did not survive restart")
	}
	if denial, err := restarted.Query(Filter{Component: "writer", Code: WriterContended}, "", 10); err != nil || len(denial.Events) != 1 || denial.Events[0].ExecutionDetails != writer.ExecutionDetails || denial.Events[0].CorrelationRef != writer.Correlation.value {
		t.Fatal("writer event dimensions did not survive clean restart")
	}
	for _, component := range []string{"jobs", "provider", "sync", "writer"} {
		if _, _, err := normalizeFilter(Filter{Component: component}); err != nil {
			t.Fatal("catalog component rejected by query")
		}
	}
	if _, _, err := normalizeFilter(Filter{Component: sentinel}); err == nil {
		t.Fatal("unknown component accepted")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), sentinel) {
		t.Fatal("private coordinate reached observation DTO")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal("read observation fixture inventory")
	}
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil || strings.Contains(string(b), sentinel) {
			t.Fatal("unsafe retained observation fixture")
		}
	}
	legacyRoot := filepath.Join(t.TempDir(), "legacy")
	if os.Mkdir(legacyRoot, 0700) != nil || os.WriteFile(filepath.Join(legacyRoot, "service.out.log"), []byte(catalog[ProviderAttempt].message+"\n"), 0600) != nil {
		t.Fatal("prepare legacy template fixture")
	}
	legacy, err := ReadLegacy(legacyRoot, "stdout")
	if err != nil || !legacy.Partial || len(legacy.Codes) != 1 || legacy.Codes[0] != LegacyOmitted {
		t.Fatal("legacy text manufactured typed execution evidence")
	}
}

func TestWriterExecutionContextRejectsWithoutWaitingForCollector(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	t.Cleanup(func() { flush(t, c, true) })
	ctx := WithObservation(context.Background(), c, Context{Cache: KnownRef("cache", "fixture-cache"), Correlation: KnownRef("correlation", "fixture-writer-scope")})
	before := c.Coverage()
	c.mu.Lock()
	done := make(chan bool, 1)
	go func() {
		done <- EmitFromContext(ctx, WriterContended, ExecutionDetails{Phase: "writer", WorkKind: "other", Outcome: "denied", FailureClass: "writer_busy"})
	}()
	select {
	case admitted := <-done:
		c.mu.Unlock()
		if admitted {
			t.Fatal("held collector admitted writer evidence")
		}
	case <-time.After(time.Second):
		c.mu.Unlock()
		t.Fatal("writer evidence waited for a collector reader")
	}
	if got := c.Coverage(); got.Dropped != before.Dropped+1 || got.LossEpoch != before.LossEpoch+1 || got.State != "partial" {
		t.Fatal("writer evidence rejection lost its exact loss boundary")
	}
}
