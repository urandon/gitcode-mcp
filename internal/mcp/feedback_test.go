package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/feedback"
	"gitcode-mcp/internal/service"
)

type feedbackSpyService struct {
	serviceInterface
	prepared     []feedback.Draft
	submits      []service.SubmitFeedbackRequest
	readiness    feedback.Readiness
	readinessErr error
}

func (s *feedbackSpyService) FeedbackReadiness(context.Context) (feedback.Readiness, error) {
	if s.readinessErr != nil {
		return feedback.Readiness{}, s.readinessErr
	}
	if s.readiness.State != "" {
		return s.readiness, nil
	}
	return feedback.Readiness{State: feedback.ReadinessReady, PrepareAvailable: true, SubmitAvailable: true, Sink: feedback.SinkGitCodeIssues, RepoID: "example/feedback"}, nil
}

func TestFeedbackToolsListFailsClosedWhenReadinessCannotBeEvaluated(t *testing.T) {
	spy := &feedbackSpyService{readinessErr: service.ErrInvalidQuery{Field: "feedback", Message: "sensitive adapter detail"}}
	srv := NewWithToolAccess(strings.NewReader(""), io.Discard, io.Discard, spy, nil, ToolAccessWrite)
	var out bytes.Buffer
	srv.writer = &out
	srv.toolsList(context.Background(), request{JSONRPC: "2.0"})
	var response response
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	var listed toolsListResult
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "submit_feedback" {
			continue
		}
		if !strings.Contains(tool.Description, feedback.ReadinessProviderUnavailable) || strings.Contains(tool.Description, "sensitive adapter detail") || tool.Meta == nil {
			t.Fatalf("submit definition=%#v", tool)
		}
		return
	}
	t.Fatal("submit_feedback not listed in write mode")
}

func (s *feedbackSpyService) PrepareFeedback(_ context.Context, draft feedback.Draft) (feedback.PreparedReport, error) {
	s.prepared = append(s.prepared, draft)
	return feedback.PreparedReport{Status: "prepared", Configured: true, Sink: feedback.SinkGitCodeIssues, RepoID: "example/feedback", Title: "feedback", Body: "body", Fingerprint: "fingerprint", DedupeDecision: "none"}, nil
}

func (s *feedbackSpyService) SubmitFeedback(_ context.Context, req service.SubmitFeedbackRequest) (feedback.SubmissionResult, error) {
	s.submits = append(s.submits, req)
	return feedback.SubmissionResult{Status: "submitted", Sink: feedback.SinkGitCodeIssues, RepoID: "example/feedback", TicketNumber: 42, Fingerprint: "fingerprint", DedupeDecision: "none", IdempotencyKey: req.IdempotencyKey, GeneratedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}, nil
}

func TestFeedbackToolsExposeSafePolicyAndDelegate(t *testing.T) {
	readNames := expectedToolNamesForAccess(ToolAccessRead)
	writeNames := expectedToolNamesForAccess(ToolAccessWrite)
	if !containsString(readNames, "feedback_status") || !containsString(readNames, "prepare_feedback") || containsString(readNames, "submit_feedback") {
		t.Fatalf("read tools=%v", readNames)
	}
	if !containsString(writeNames, "feedback_status") || !containsString(writeNames, "prepare_feedback") || !containsString(writeNames, "submit_feedback") {
		t.Fatalf("write tools=%v", writeNames)
	}
	prepareSchema := writeToolInputSchema("prepare_feedback")
	if _, ok := prepareSchema.Properties["repo_id"]; ok {
		t.Fatal("feedback schema must not accept an arbitrary sink repository")
	}
	if !containsString(prepareSchema.Required, "impact") {
		t.Fatalf("required=%v", prepareSchema.Required)
	}

	spy := &feedbackSpyService{}
	srv, r, w, stderr := newPipeServerWithToolAccess(spy, ToolAccessWrite)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	call := func(id, name string, args map[string]any) toolCallResult {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		_, _ = r.Write(append(payload, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read %s: %v stderr=%s", name, err, stderr.String())
		}
		return decodeToolCallResult(t, line)
	}
	args := map[string]any{"summary": "fallback required", "category": "ux_friction", "surface": "mcp", "reporter_type": "agent", "observed": "MCP failed", "expected": "MCP succeeds", "impact": "CLI fallback was required", "fallback_used": "CLI"}
	status := call("status", "feedback_status", map[string]any{})
	var statusResult feedback.Readiness
	decodeStructured(t, status, &statusResult)
	if statusResult.State != feedback.ReadinessReady || !statusResult.SubmitAvailable {
		t.Fatalf("status=%#v", statusResult)
	}
	prepared := call("prepare", "prepare_feedback", args)
	var preparedResult feedback.PreparedReport
	decodeStructured(t, prepared, &preparedResult)
	if preparedResult.Fingerprint != "fingerprint" || len(spy.prepared) != 1 || spy.prepared[0].FallbackUsed != "CLI" {
		t.Fatalf("prepared=%#v calls=%#v", preparedResult, spy.prepared)
	}
	args["write_mode"] = "live"
	args["idempotency_key"] = "feedback-42"
	submitted := call("submit", "submit_feedback", args)
	var submittedResult feedback.SubmissionResult
	decodeStructured(t, submitted, &submittedResult)
	if submittedResult.TicketNumber != 42 || len(spy.submits) != 1 || spy.submits[0].Mode != service.WriteModeLive || spy.submits[0].IdempotencyKey != "feedback-42" {
		t.Fatalf("submitted=%#v calls=%#v", submittedResult, spy.submits)
	}
	_ = r.Close()
	wg.Wait()
}

func TestFeedbackToolsListAdvertisesRuntimeSubmissionReadiness(t *testing.T) {
	spy := &feedbackSpyService{readiness: feedback.Readiness{State: feedback.ReadinessCredentialMissing, PrepareAvailable: true, Remediation: "configure a credential", Handoff: "gitcode-mcp auth status"}}
	srv := NewWithToolAccess(strings.NewReader(""), io.Discard, io.Discard, spy, nil, ToolAccessWrite)
	var out bytes.Buffer
	srv.writer = &out
	srv.toolsList(context.Background(), request{JSONRPC: "2.0"})
	var response response
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	var listed toolsListResult
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "submit_feedback" {
			continue
		}
		if !strings.Contains(tool.Description, feedback.ReadinessCredentialMissing) || tool.Meta == nil {
			t.Fatalf("submit definition=%#v", tool)
		}
		return
	}
	t.Fatal("submit_feedback not listed in write mode")
}

func TestFeedbackToolDescriptionsEncourageUsefulBoundedReports(t *testing.T) {
	for _, name := range []string{"prepare_feedback", "submit_feedback"} {
		capabilityDefinition, ok := capability.LookupByMCPName(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		definition := writeToolDefinition(capabilityDefinition)
		lower := strings.ToLower(definition.Description)
		if !strings.Contains(lower, "feedback") || !strings.Contains(lower, "transcript") {
			t.Fatalf("description %s=%q", name, definition.Description)
		}
	}
}
