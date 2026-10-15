package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
)

type localProjectSource struct{ *testSource }

func (s localProjectSource) Env(key string) string {
	if key == config.EnvToken || key == "GITCODE_MCP_TEST_KEYCHAIN_TOKEN" {
		panic("local project discovered credentials")
	}
	return s.testSource.Env(key)
}
func (s localProjectSource) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (s localProjectSource) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

type localReadOnlySource struct{ config.Source }

func (s localReadOnlySource) Env(key string) string {
	if key == config.EnvMCPToolAccess {
		return "read"
	}
	return s.Source.Env(key)
}

func localProjectCLI(t *testing.T, src config.Source, wantCode string, args ...string) json.RawMessage {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(append([]string{"project"}, args...), strings.NewReader(""), &out, &errs, src)
	if wantCode != "" {
		if code == 0 || !bytes.Contains(errs.Bytes(), []byte(wantCode)) {
			t.Fatal("missing typed CLI refusal")
		}
		return nil
	}
	if code != 0 {
		t.Fatal("local project CLI failed: " + errs.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal("invalid CLI JSON")
	}
	return out.Bytes()
}
func localProjectMCP(t *testing.T, src config.Source, method string, params any, wantCode string) json.RawMessage {
	t.Helper()
	input, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	var out, errs bytes.Buffer
	if run([]string{"--mcp"}, strings.NewReader(string(input)+"\n"), &out, &errs, src) != 0 {
		t.Fatal("local MCP entrypoint failed")
	}
	var resp struct {
		Error *struct {
			Data struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(out.Bytes(), &resp) != nil {
		t.Fatal("invalid MCP JSON")
	}
	if wantCode != "" {
		if resp.Error == nil || resp.Error.Data.Code != wantCode {
			t.Fatal("missing typed MCP refusal")
		}
		return nil
	}
	if resp.Error != nil {
		t.Fatal("local MCP call failed with " + resp.Error.Data.Code)
	}
	if method == "tools/list" {
		return resp.Result
	}
	var result struct {
		Structured json.RawMessage `json:"structuredContent"`
	}
	if json.Unmarshal(resp.Result, &result) != nil || len(result.Structured) == 0 {
		t.Fatal("missing structured local result")
	}
	return result.Structured
}
func projectTool(name string, args any) map[string]any {
	return map[string]any{"name": name, "arguments": args}
}

func startLocalProjectDaemon(t *testing.T, src config.Source) (*servicectl.RPCClient, func()) {
	t.Helper()
	eff, err := config.LoadEffective(src, config.Overrides{})
	if err != nil {
		t.Fatal("local fixture config failed")
	}
	m := servicectl.Manager{Source: src, EffectiveConfig: &eff.Config, Version: "fixture"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Error("local daemon failed")
				}
			case <-time.After(3 * time.Second):
				t.Error("local daemon did not join")
			}
		})
	}
	t.Cleanup(stop)
	client, err := m.Client()
	if err != nil {
		stop()
		t.Fatal("local client unavailable")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status service.LocalProjectsStatus
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := client.Call(ctx, "LocalProjects.Status", struct{}{}, &status)
		cancel()
		if err == nil {
			return client, stop
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	t.Fatal("local daemon did not publish readiness")
	return nil, nil
}

func TestLocalProjectCLIAndMCPRealGitVerticalSlice(t *testing.T) {
	base := newTestSource(t)
	short, err := os.MkdirTemp(".", "g4-")
	if err != nil {
		t.Fatal("short runtime fixture unavailable")
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	base.cacheDir = filepath.Join(short, "cache")
	base.env[config.EnvMCPConfigPath] = filepath.Join(base.configDir, "local.yaml")
	var networkCalls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { networkCalls.Add(1); w.WriteHeader(500) }))
	defer remote.Close()
	base.env[config.EnvAPIURL] = remote.URL + "/api/v5"
	base.env[config.EnvRAGProviderEndpoint] = remote.URL
	src := localProjectSource{base}
	localProjectCLI(t, src, "", "init", "--project", "lab", "--project-id", "lab", "--storage-ref", "data", "--confirm")
	localProjectCLI(t, src, "", "register", "--project", "lab", "--project-id", "lab", "--storage-ref", "data", "--confirm")
	_, stop := startLocalProjectDaemon(t, src)
	var status service.LocalProjectsStatus
	json.Unmarshal(localProjectCLI(t, src, "", "status"), &status)
	if len(status.Projects) != 1 || !status.Projects[0].Readable {
		t.Fatal("local status unavailable")
	}
	createArgs := []string{"mutate", "--project", "lab", "--operation", "create_issue", "--title", "--body", "--body", "Original evidence", "--labels", "research,ready", "--milestone", "Phase one", "--expected-head", status.Projects[0].Commit, "--write-mode", "local", "--idempotency-key", "create-question"}
	var created localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, src, "", createArgs...), &created)
	if created.Entity.Metadata.ID != "ISSUE-1" || created.Entity.Metadata.Title != "--body" || created.Entity.Metadata.Milestone != "Phase one" || len(created.Entity.Metadata.Labels) != 2 {
		t.Fatal("issue metadata missing")
	}
	var read localgit.Entity
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_get", map[string]any{"project": "lab", "kind": "issue", "id": "ISSUE-1"}), ""), &read)
	if read.Citation != created.Entity.Citation || read.Body != "Original evidence" {
		t.Fatal("MCP did not read exact CLI result")
	}
	update := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Answered", State: "closed", Body: "Later evidence", ExpectedRevision: read.Citation.Blob, WriteMode: "local", IdempotencyKey: "answer"}}
	var updated localgit.MutationResult
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_mutate", update), ""), &updated)
	stale := update
	stale.Mutation.Title = "Stale"
	stale.Mutation.IdempotencyKey = "stale"
	localProjectMCP(t, src, "tools/call", projectTool("local_project_mutate", stale), "local_git_write_conflict")
	var comment localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, src, "", "mutate", "--project", "lab", "--operation", "add_comment", "--parent-id", "ISSUE-1", "--title", "Note", "--body", "Comment evidence", "--expected-head", updated.Entity.Citation.Commit, "--write-mode", "local", "--idempotency-key", "note"), &comment)
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_get", map[string]any{"project": "lab", "kind": "comment", "id": "COMMENT-1"}), ""), &read)
	if read.Metadata.ParentID != "ISSUE-1" || read.Body != "Comment evidence" {
		t.Fatal("comment routing mismatch")
	}
	wiki := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_wiki", Title: "Research", Body: "Wiki v1", Links: []localgit.Reference{{Kind: localgit.Issue, ID: "ISSUE-1"}}, ExpectedHead: comment.Entity.Citation.Commit, WriteMode: "local", IdempotencyKey: "wiki"}}
	var page localgit.MutationResult
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_mutate", wiki), ""), &page)
	if len(page.Entity.Metadata.Links) != 1 || page.Entity.Metadata.Links[0].ID != "ISSUE-1" {
		t.Fatal("typed link metadata lost")
	}
	json.Unmarshal(localProjectCLI(t, src, "", "mutate", "--project", "lab", "--operation", "update_wiki", "--id", "DOC-1", "--title", "Research", "--state", "active", "--body", "Wiki v2", "--expected-revision", page.Entity.Citation.Blob, "--write-mode", "local", "--idempotency-key", "wiki-later"), &page)
	var pages []localgit.Entity
	json.Unmarshal(localProjectCLI(t, src, "", "list", "--project", "lab", "--kind", "wiki"), &pages)
	if len(pages) != 1 || pages[0].Body != "Wiki v2" {
		t.Fatal("wiki list mismatch")
	}
	localProjectMCP(t, src, "tools/call", projectTool("local_project_list", map[string]any{"project": "lab", "kind": "comment", "limit": 8}), "")
	stop()
	localProjectCLI(t, src, "local_git_unavailable", "status")
	_, stop = startLocalProjectDaemon(t, src)
	defer stop()
	var replay localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, src, "", createArgs...), &replay)
	if !replay.Replayed || replay.Entity.Citation != created.Entity.Citation || replay.Entity.Body != created.Entity.Body {
		t.Fatal("restart replay lost original result")
	}
	deleteReq := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "delete_issue", ID: "ISSUE-1", ExpectedRevision: updated.Entity.Citation.Blob, WriteMode: "local", IdempotencyKey: "delete"}}
	var deleted localgit.MutationResult
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_mutate", deleteReq), ""), &deleted)
	if deleted.Entity.Metadata.State != "deleted" || deleted.Entity.Body != "" {
		t.Fatal("tombstone missing")
	}
	var history []localgit.Citation
	json.Unmarshal(localProjectCLI(t, src, "", "history", "--project", "lab", "--kind", "issue", "--id", "ISSUE-1", "--max-commits", "64"), &history)
	if len(history) != 3 || history[2] != created.Entity.Citation {
		t.Fatal("Git history/citations mismatch")
	}
	localProjectMCP(t, src, "tools/call", projectTool("local_project_history", map[string]any{"project": "lab", "kind": "issue", "id": "ISSUE-1", "max_commits": 1}), "local_git_history_unavailable")
	localProjectCLI(t, src, "local_git_write_conflict", "init", "--project", "lab", "--project-id", "lab", "--storage-ref", "data", "--confirm")
	localProjectCLI(t, src, "local_git_invalid_argument", "get", "--project", "lab", "--kind", "issue", "--id", "ISSUE-1", "--body", "ignored")
	localProjectMCP(t, src, "tools/call", projectTool("local_project_get", map[string]any{"project": "missing", "kind": "issue", "id": "ISSUE-1"}), "local_git_unknown_project")
	localProjectMCP(t, src, "tools/call", projectTool("create_issue", map[string]any{}), "local_git_unsupported_capability")
	localProjectMCP(t, localReadOnlySource{src}, "tools/call", projectTool("local_project_mutate", deleteReq), "tool_disabled_by_policy")
	badFile := filepath.Join(t.TempDir(), "invalid-body")
	if os.WriteFile(badFile, []byte{0xff}, 0600) != nil {
		t.Fatal("invalid UTF8 fixture unavailable")
	}
	localProjectCLI(t, src, "local_git_invalid_argument", "mutate", "--project", "lab", "--operation", "create_issue", "--title", "Invalid", "--body-file", badFile, "--expected-head", deleted.Entity.Citation.Commit, "--write-mode", "local", "--idempotency-key", "invalid-body")
	var rejected, diagnostic bytes.Buffer
	if run([]string{"create-issue", "--title", "Remote must not run"}, nil, &rejected, &diagnostic, src) == 0 || !bytes.Contains(diagnostic.Bytes(), []byte("local_git_unsupported_capability")) {
		t.Fatal("local mode accepted remote CLI routing")
	}
	if networkCalls.Load() != 0 {
		t.Fatal("local flow contacted configured provider")
	}
	eff, err := config.LoadEffective(src, config.Overrides{})
	if err != nil {
		t.Fatal("effective config unavailable")
	}
	if _, err = os.Stat(eff.Config.CachePath); !os.IsNotExist(err) {
		t.Fatal("local project created a cache authority")
	}
	// Explicit protected HTTP/SSE reuses the same local handler, not cache bootstrap.
	h, b := newMCPHTTPHandler(StartupDeps{Config: eff.Config, Source: src})
	defer b.Close()
	transport := mcp.NewHTTPSSETransport(h, mcp.ServerConfig{Logger: log.New(io.Discard, "", 0)})
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	transport.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || b.store != nil {
		t.Fatal("local HTTP readiness bootstrapped cache")
	}
	if localMCPLoopback("0.0.0.0:9020") || !localMCPLoopback("127.0.0.1:9020") {
		t.Fatal("local HTTP bind policy mismatch")
	}
	httpServer := httptest.NewServer(transport.Handler())
	defer httpServer.Close()
	streamCtx, streamCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer streamCancel()
	streamReq, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, httpServer.URL+"/sse", nil)
	stream, err := httpServer.Client().Do(streamReq)
	if err != nil {
		t.Fatal("local SSE unavailable")
	}
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	endpoint := readLocalSSEData(t, reader)
	if !strings.HasPrefix(endpoint, "/message?session_id=") {
		t.Fatal("local SSE endpoint missing")
	}
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": projectTool("local_project_get", map[string]any{"project": "lab", "kind": "issue", "id": "ISSUE-1"})})
	postReq, _ := http.NewRequestWithContext(streamCtx, http.MethodPost, httpServer.URL+endpoint, bytes.NewReader(payload))
	posted, err := httpServer.Client().Do(postReq)
	if err != nil {
		t.Fatal("local SSE call unavailable")
	}
	posted.Body.Close()
	if posted.StatusCode != http.StatusAccepted {
		t.Fatal("local SSE call not accepted")
	}
	var reply struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			Structured localgit.Entity `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(readLocalSSEData(t, reader)), &reply) != nil || len(reply.Error) > 0 || reply.Result.Structured.Citation != deleted.Entity.Citation {
		t.Fatal("HTTP/SSE did not preserve exact local entity result")
	}
	if b.store != nil || networkCalls.Load() != 0 {
		t.Fatal("local SSE entered cache or provider graph")
	}
}

func readLocalSSEData(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	for i := 0; i < 8; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal("local SSE data unavailable")
		}
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	t.Fatal("local SSE data missing")
	return ""
}
