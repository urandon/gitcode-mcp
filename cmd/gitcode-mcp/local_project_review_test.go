package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func TestLocalProjectCLIConfigRefusalsNeverDiscoverCredentials(t *testing.T) {
	for _, fixture := range []struct{ name, yaml, access string }{
		{"malformed", "service: {mode: local}\nlocal_projects: [\n", ""},
		{"missing", "", ""},
		{"merge", "service: {mode: local}\ncache_mode: invalid\n", ""},
		{"environment", "service: {mode: local}\nlocal_projects: {lab: {backend: local_git, project_id: lab, storage_ref: data, ref: refs/heads/main}}\n", "invalid"},
		{"credential-policy", "service: {mode: local}\nlocal_projects: {lab: {backend: local_git, project_id: lab, storage_ref: data, ref: refs/heads/main}}\ncredential: {store: invalid}\n", ""},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			base := newTestSource(t)
			path := filepath.Join(base.configDir, "local.yaml")
			base.env[config.EnvMCPConfigPath] = path
			base.env[config.EnvMCPToolAccess] = fixture.access
			if os.MkdirAll(base.configDir, 0700) != nil {
				t.Fatal("config directory unavailable")
			}
			if fixture.yaml != "" && os.WriteFile(path, []byte(fixture.yaml), 0600) != nil {
				t.Fatal("config fixture unavailable")
			}
			defer func() {
				if recover() != nil {
					t.Fatal("local config refusal consulted credentials")
				}
			}()
			for _, args := range [][]string{{"status"}, {"get", "--project", "lab", "--kind", "issue", "--id", "ISSUE-1"}} {
				var out, diagnostic bytes.Buffer
				code := run(append([]string{"project"}, args...), nil, &out, &diagnostic, localProjectSource{base})
				var result map[string]string
				if code == 0 || out.Len() != 0 || json.Unmarshal(diagnostic.Bytes(), &result) != nil || result["code"] != "local_git_invalid_configuration" {
					t.Fatal("missing finite local config refusal")
				}
				if bytes.Contains(diagnostic.Bytes(), []byte(path)) || bytes.Contains(diagnostic.Bytes(), []byte(fixture.yaml)) && fixture.yaml != "" {
					t.Fatal("configuration diagnostic leaked input")
				}
			}
			if _, err := os.Stat(filepath.Join(base.cacheDir, "gitcode-mcp", "cache.db")); !os.IsNotExist(err) {
				t.Fatal("refusal opened derived cache")
			}
		})
	}
}

func TestLocalProjectCLILinksCreateReplaceAndClear(t *testing.T) {
	base := newTestSource(t)
	short, err := os.MkdirTemp(".", "links-")
	if err != nil {
		t.Fatal("short runtime fixture unavailable")
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	base.cacheDir = filepath.Join(short, "cache")
	base.env[config.EnvMCPConfigPath] = filepath.Join(base.configDir, "local.yaml")
	src := localProjectSource{base}
	localProjectCLI(t, src, "", "init", "--project", "lab", "--project-id", "lab", "--storage-ref", "data", "--confirm")
	_, stop := startLocalProjectDaemon(t, src)
	defer stop()
	var status service.LocalProjectsStatus
	json.Unmarshal(localProjectCLI(t, src, "", "status"), &status)
	var created localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, src, "", "mutate", "--project", "lab", "--operation", "create_issue", "--title", "Question", "--links", `[{"kind":"wiki","id":"DOC-1"}]`, "--expected-head", status.Projects[0].Commit, "--write-mode", "local", "--idempotency-key", "links-create"), &created)
	if len(created.Entity.Metadata.Links) != 1 || created.Entity.Metadata.Links[0] != (localgit.Reference{Kind: localgit.Wiki, ID: "DOC-1"}) {
		t.Fatal("CLI create lost links")
	}
	for i, links := range []string{`[{"kind":"issue","id":"ISSUE-1"}]`, `[]`} {
		var updated localgit.MutationResult
		key := "links-replace"
		if i == 1 {
			key = "links-clear"
		}
		json.Unmarshal(localProjectCLI(t, src, "", "mutate", "--project", "lab", "--operation", "update_issue", "--id", "ISSUE-1", "--title", "Question", "--state", "open", "--links", links, "--expected-revision", created.Entity.Citation.Blob, "--write-mode", "local", "--idempotency-key", key), &updated)
		var read localgit.Entity
		json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_get", map[string]any{"project": "lab", "kind": "issue", "id": "ISSUE-1"}), ""), &read)
		if read.Citation != updated.Entity.Citation || len(read.Metadata.Links) != 1-i || i == 0 && read.Metadata.Links[0].ID != "ISSUE-1" {
			t.Fatal("CLI/MCP links replacement mismatch")
		}
		created = updated
	}
	for _, links := range []string{`null`, `{}`, `[null]`, `[{"kind":null,"id":"DOC-1"}]`, `[{"kind":"wiki","id":null}]`, `[{"Kind":"wiki","id":"DOC-1"}]`, `[{"kind":"wiki","kind":"wiki","id":"DOC-1"}]`, `[{"kind":"wiki","id":"DOC-1","url":"https://example.invalid"}]`, `[{"kind":"wiki","id":"ISSUE-1"}]`, `[] []`, `--invalid`} {
		want := "local_git_invalid_argument"
		if links == `[{"kind":"wiki","id":"ISSUE-1"}]` {
			want = "local_git_invalid_schema" // Shared service retains semantic validation.
		}
		localProjectCLI(t, src, want, "mutate", "--project", "lab", "--operation", "update_issue", "--id", "ISSUE-1", "--title", "Question", "--state", "open", "--links", links, "--expected-revision", created.Entity.Citation.Blob, "--write-mode", "local", "--idempotency-key", "invalid-links")
		json.Unmarshal(localProjectCLI(t, src, "", "status"), &status)
		if status.Projects[0].Commit != created.Entity.Citation.Commit {
			t.Fatal("invalid links published source changes")
		}
	}
}
