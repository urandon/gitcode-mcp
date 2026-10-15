package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func backupSource(t *testing.T, remote string) localProjectSource {
	base := newTestSource(t)
	short, err := os.MkdirTemp(".", "g7-")
	if err != nil {
		t.Fatal("runtime fixture")
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	base.cacheDir = filepath.Join(short, "cache")
	base.env[config.EnvMCPConfigPath] = filepath.Join(base.configDir, "local.yaml")
	base.env[config.EnvAPIURL] = remote + "/api/v5"
	return localProjectSource{base}
}

func TestLocalProjectBundleCLIAndMCPRestoredAuthority(t *testing.T) {
	var networkCalls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { networkCalls.Add(1); w.WriteHeader(500) }))
	defer remote.Close()
	src := backupSource(t, remote.URL)
	localProjectCLI(t, src, "", "init", "--project", "lab", "--project-id", "lab", "--storage-ref", "data", "--confirm")
	_, stop := startLocalProjectDaemon(t, src)
	var status service.LocalProjectsStatus
	json.Unmarshal(localProjectCLI(t, src, "", "status"), &status)
	args := []string{"mutate", "--project", "lab", "--operation", "create_issue", "--title", "Question", "--body", "Original evidence", "--expected-head", status.Projects[0].Commit, "--write-mode", "local", "--idempotency-key", "question"}
	var created localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, src, "", args...), &created)
	update := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Answered", State: "closed", Body: "Later evidence", ExpectedRevision: created.Entity.Citation.Blob, WriteMode: "local", IdempotencyKey: "answer"}}
	var updated localgit.MutationResult
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_mutate", update), ""), &updated)
	var note localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, src, "", "mutate", "--project", "lab", "--operation", "add_comment", "--parent-id", "ISSUE-1", "--title", "Note", "--body", "Comment evidence", "--expected-head", updated.Entity.Citation.Commit, "--write-mode", "local", "--idempotency-key", "note"), &note)
	wiki := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_wiki", Title: "Page", Body: "Wiki evidence", ExpectedHead: note.Entity.Citation.Commit, WriteMode: "local", IdempotencyKey: "page"}}
	var page localgit.MutationResult
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_mutate", wiki), ""), &page)
	var before localgit.Entity
	json.Unmarshal(localProjectCLI(t, src, "", "get", "--project", "lab", "--kind", "issue", "--id", "ISSUE-1"), &before)
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	localProjectCLI(t, src, "local_git_invalid_argument", "export", "--project", "lab", "--bundle", bundle)
	var exported service.LocalProjectBackupResult
	raw := localProjectCLI(t, src, "", "export", "--project", "lab", "--bundle", bundle, "--confirm")
	json.Unmarshal(raw, &exported)
	if exported.Status != "exported" || exported.Commit != page.Entity.Citation.Commit || exported.Commits != 5 || bytes.Contains(raw, []byte(bundle)) {
		t.Fatal("private export DTO mismatch")
	}
	localProjectCLI(t, src, "local_git_write_conflict", "restore", "--project", "new", "--project-id", "lab", "--storage-ref", "new", "--bundle", bundle, "--expected-head", exported.Commit, "--confirm")
	stop()
	target := backupSource(t, remote.URL)
	localProjectCLI(t, target, "local_git_invalid_argument", "restore", "--project", "lab", "--project-id", "lab", "--storage-ref", "restored", "--bundle", bundle, "--confirm")
	var restored service.LocalProjectBackupResult
	raw = localProjectCLI(t, target, "", "restore", "--project", "lab", "--project-id", "lab", "--storage-ref", "restored", "--bundle", bundle, "--expected-head", exported.Commit, "--confirm")
	json.Unmarshal(raw, &restored)
	if restored.Status != "restored" || restored.Commit != exported.Commit || restored.ProjectID != "lab" || bytes.Contains(raw, []byte(bundle)) {
		t.Fatal("restored registration mismatch")
	}
	localProjectCLI(t, target, "local_git_write_conflict", "restore", "--project", "lab", "--project-id", "lab", "--storage-ref", "restored", "--bundle", bundle, "--expected-head", exported.Commit, "--confirm")
	_, stopTarget := startLocalProjectDaemon(t, target)
	defer stopTarget()
	var after localgit.Entity
	json.Unmarshal(localProjectMCP(t, target, "tools/call", projectTool("local_project_get", map[string]any{"project": "lab", "kind": "issue", "id": "ISSUE-1"}), ""), &after)
	x, _ := json.Marshal(before)
	y, _ := json.Marshal(after)
	if string(x) != string(y) {
		t.Fatal("restored issue bytes/citation mismatch")
	}
	for _, entity := range []struct{ kind, id, body string }{{"comment", "COMMENT-1", "Comment evidence"}, {"wiki", "DOC-1", "Wiki evidence"}} {
		json.Unmarshal(localProjectCLI(t, target, "", "get", "--project", "lab", "--kind", entity.kind, "--id", entity.id), &after)
		if after.Body != entity.body || after.Citation.Commit != exported.Commit {
			t.Fatal("restored entity lost")
		}
	}
	var history []localgit.Citation
	json.Unmarshal(localProjectMCP(t, target, "tools/call", projectTool("local_project_history", map[string]any{"project": "lab", "kind": "issue", "id": "ISSUE-1", "max_commits": 64}), ""), &history)
	if len(history) != 2 || history[1] != created.Entity.Citation {
		t.Fatal("restored history mismatch")
	}
	var replay localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, target, "", args...), &replay)
	if !replay.Replayed || replay.Entity.Citation != created.Entity.Citation || replay.Entity.Body != created.Entity.Body {
		t.Fatal("restored original receipt lost")
	}
	update.Mutation.Title = "Stale"
	update.Mutation.IdempotencyKey = "stale"
	localProjectMCP(t, target, "tools/call", projectTool("local_project_mutate", update), "local_git_write_conflict")
	var search service.LocalProjectSearchResult
	json.Unmarshal(localProjectCLI(t, target, "", "search", "--project", "lab", "--query", "Later"), &search)
	if search.EffectiveMode != "fulltext" || len(search.Hits) != 1 || search.Hits[0].Citation != before.Citation {
		t.Fatal("restored lexical citation mismatch")
	}
	localProjectMCP(t, target, "tools/call", projectTool("local_project_restore", map[string]any{"bundle": "forbidden-file-input"}), "local_git_unsupported_capability")
	if networkCalls.Load() != 0 {
		t.Fatal("backup/restore contacted remote provider")
	}
}
