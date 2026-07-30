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
	request service.PushMirrorListRequest
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
