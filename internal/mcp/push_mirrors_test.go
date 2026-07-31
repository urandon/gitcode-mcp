package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/service"
)

type pushMirrorSpyService struct {
	serviceInterface
	request        service.PushMirrorListRequest
	triggerRequest service.WriteCommandRequest
	waitRequest    service.PushMirrorWaitRequest
}

func (s *pushMirrorSpyService) ListPushRemoteMirrors(_ context.Context, req service.PushMirrorListRequest) (service.PushMirrorListResult, error) {
	s.request = req
	return service.PushMirrorListResult{
		RepoID: "fixture-a",
		Count:  1,
		Mirrors: []service.PushMirrorRecord{{
			ID:               "PUSHMIRROR-17",
			RemoteID:         "17",
			Destination:      "https://mirror.example.invalid/example/repo.git",
			UpdateStatus:     "finished",
			NumberOfFailures: 0,
		}},
		Evidence:    "adapter-confirmed read with sanitized cache refresh",
		GeneratedAt: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (s *pushMirrorSpyService) TriggerPushRemoteMirror(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.triggerRequest = req
	return service.WriteCommandResult{
		Command:        "trigger-push-mirror",
		Status:         "succeeded",
		RepoID:         req.RepoID,
		RemoteID:       "17",
		IdempotencyKey: req.IdempotencyKey,
		PushMirror:     &service.WritePushMirrorReceipt{MirrorID: "17", Status: "triggered", PreviousStatus: "finished", ReadbackStatus: "processing", TriggeredAt: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)},
		Evidence:       "adapter-confirmed write with audit and cache refresh",
		GeneratedAt:    time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (s *pushMirrorSpyService) WaitPushRemoteMirror(_ context.Context, req service.PushMirrorWaitRequest) (service.PushMirrorWaitResult, error) {
	s.waitRequest = req
	return service.PushMirrorWaitResult{RepoID: req.RepoID, MirrorID: "17", Status: "finished", UpdateStatus: "finished", LastSuccessfulUpdateAt: time.Date(2026, 7, 30, 12, 0, 5, 0, time.UTC), Evidence: "terminal status confirmed by sanitized live readback", GeneratedAt: time.Date(2026, 7, 30, 12, 0, 5, 0, time.UTC)}, nil
}

func TestMCPListPushRemoteMirrorsAvailableInReadOnlyMode(t *testing.T) {
	spy := &pushMirrorSpyService{}
	srv, reader, writer, stderr := newPipeServerWithToolAccess(spy, ToolAccessRead)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()

	listRequest, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "list", "method": "tools/list"})
	_, _ = reader.Write(append(listRequest, '\n'))
	listLine, err := readLine(writer)
	if err != nil {
		t.Fatalf("read tools/list: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(string(listLine), `"list_push_remote_mirrors"`) {
		t.Fatalf("read-only tools/list omitted push mirror tool: %s", listLine)
	}

	callRequest, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "mirrors",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_push_remote_mirrors",
			"arguments": map[string]any{"repo_id": "fixture-a"},
		},
	})
	_, _ = reader.Write(append(callRequest, '\n'))
	callLine, err := readLine(writer)
	if err != nil {
		t.Fatalf("read tool response: %v (stderr: %s)", err, stderr.String())
	}
	callResult := decodeToolCallResult(t, callLine)
	var result service.PushMirrorListResult
	decodeStructured(t, callResult, &result)
	if spy.request.RepoID != "fixture-a" || result.Count != 1 || result.Mirrors[0].Destination != "https://mirror.example.invalid/example/repo.git" {
		t.Fatalf("request=%#v result=%#v", spy.request, result)
	}
	if strings.Contains(string(callLine), "token") || strings.Contains(string(callLine), "@") || strings.Contains(string(callLine), "?") {
		t.Fatalf("tool response contains credential-shaped data: %s", callLine)
	}

	_ = reader.Close()
	wg.Wait()
}

func TestMCPPushMirrorTriggerRequiresWriteAccessAndWaitIsReadOnly(t *testing.T) {
	spy := &pushMirrorSpyService{}
	readServer, readIn, readOut, readErr := newPipeServerWithToolAccess(spy, ToolAccessRead)
	var readWG sync.WaitGroup
	readWG.Add(1)
	go func() {
		defer readWG.Done()
		_ = readServer.Serve()
	}()
	listRequest, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "list", "method": "tools/list"})
	_, _ = readIn.Write(append(listRequest, '\n'))
	listLine, err := readLine(readOut)
	if err != nil {
		t.Fatalf("read tools/list: %v (stderr: %s)", err, readErr.String())
	}
	if strings.Contains(string(listLine), `"trigger_push_remote_mirror"`) {
		t.Fatalf("read-only tools/list exposed trigger: %s", listLine)
	}
	if !strings.Contains(string(listLine), `"wait_push_remote_mirror"`) {
		t.Fatalf("read-only tools/list omitted wait: %s", listLine)
	}
	_ = readIn.Close()
	readWG.Wait()

	writeServer, writeIn, writeOut, writeErr := newPipeServerWithToolAccess(spy, ToolAccessWrite)
	var writeWG sync.WaitGroup
	writeWG.Add(1)
	go func() {
		defer writeWG.Done()
		_ = writeServer.Serve()
	}()
	_, _ = writeIn.Write(append(listRequest, '\n'))
	writeList, err := readLine(writeOut)
	if err != nil {
		t.Fatalf("write tools/list: %v (stderr: %s)", err, writeErr.String())
	}
	if !strings.Contains(string(writeList), `"trigger_push_remote_mirror"`) || !strings.Contains(string(writeList), `"idempotency_key"`) {
		t.Fatalf("write tools/list omitted trigger contract: %s", writeList)
	}

	triggerRequest, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "trigger", "method": "tools/call",
		"params": map[string]any{
			"name":      "trigger_push_remote_mirror",
			"arguments": map[string]any{"repo_id": "fixture-a", "mirror_id": "17", "write_mode": "live", "idempotency_key": "trigger-key"},
		},
	})
	_, _ = writeIn.Write(append(triggerRequest, '\n'))
	triggerLine, err := readLine(writeOut)
	if err != nil {
		t.Fatalf("trigger response: %v (stderr: %s)", err, writeErr.String())
	}
	var triggerResult service.WriteCommandResult
	decodeStructured(t, decodeToolCallResult(t, triggerLine), &triggerResult)
	if spy.triggerRequest.ID != "17" || spy.triggerRequest.IdempotencyKey != "trigger-key" || triggerResult.PushMirror == nil {
		t.Fatalf("request=%#v result=%#v", spy.triggerRequest, triggerResult)
	}

	waitRequest, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "wait", "method": "tools/call",
		"params": map[string]any{
			"name":      "wait_push_remote_mirror",
			"arguments": map[string]any{"repo_id": "fixture-a", "mirror_id": "17", "after": "2026-07-30T12:00:00Z", "timeout_seconds": 120},
		},
	})
	_, _ = writeIn.Write(append(waitRequest, '\n'))
	waitLine, err := readLine(writeOut)
	if err != nil {
		t.Fatalf("wait response: %v (stderr: %s)", err, writeErr.String())
	}
	var waitResult service.PushMirrorWaitResult
	decodeStructured(t, decodeToolCallResult(t, waitLine), &waitResult)
	if waitResult.Status != "finished" || spy.waitRequest.MirrorID != "17" || spy.waitRequest.TimeoutSeconds != 120 {
		t.Fatalf("request=%#v result=%#v", spy.waitRequest, waitResult)
	}
	if strings.Contains(string(triggerLine), "destination") || strings.Contains(string(waitLine), "destination") {
		t.Fatalf("credential-bearing field leaked: trigger=%s wait=%s", triggerLine, waitLine)
	}
	_ = writeIn.Close()
	writeWG.Wait()
}
