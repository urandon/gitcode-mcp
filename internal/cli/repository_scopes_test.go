package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestIntegrationRepoSetScopesPreservesSelectedCacheAndConfig(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "selected", "cache.db")
	src := &repoInitLocalSource{env: map[string]string{}, cwd: dir, homeDir: dir, configDir: filepath.Join(dir, "config"), cacheDir: filepath.Join(dir, "cache")}
	configPath := filepath.Join(dir, "operator.yaml")
	content := []byte("# keep operator formatting\ncache_mode: global\ncache_path: " + cachePath + "\n")
	if err := os.WriteFile(configPath, content, 0600); err != nil {
		t.Fatal(err)
	}
	src.env[config.EnvMCPConfigPath] = configPath
	deps := localCommandDeps{Source: src}
	run := func(args ...string) (int, string, string) {
		var out, err bytes.Buffer
		code := executeWithFactoryAndDeps(args, &out, &err, nil, deps)
		return code, out.String(), err.String()
	}
	if code, _, err := run("repo", "add", "--repo", "owner/repo", "--owner", "owner", "--name", "repo", "--api-base-url", "https://custom.example.invalid/api/v5", "--alias", "short", "--display-name", "Fixture", "--scopes", "issues"); code != 0 {
		t.Fatalf("initial binding failed %d: %s", code, err)
	}
	for _, format := range []string{"json", "text"} {
		code, out, err := run("repo", "set-scopes", "--repo", "owner/repo", "--scopes", "issues,wiki,pulls,comments", "--format", format)
		if code != 0 {
			t.Fatalf("scope update failed %d: %s", code, err)
		}
		if format == "json" {
			var binding service.RepositoryBinding
			if json.Unmarshal([]byte(out), &binding) != nil || !reflect.DeepEqual(binding.Scopes, []service.RepositoryScope{service.RepositoryScopeIssues, service.RepositoryScopeWiki}) || binding.APIBaseURL != "https://custom.example.invalid/api/v5" || !reflect.DeepEqual(binding.Aliases, []string{"short"}) {
				t.Fatal("scope receipt lost metadata or normalization")
			}
		} else if !strings.Contains(out, "scopes: issues,wiki") {
			t.Fatal("text readback absent")
		}
	}
	for _, args := range [][]string{
		{"--repo", "short", "--scopes", "wiki"}, {"--repo", "missing", "--scopes", "wiki"},
		{"--repo", "owner/repo"}, {"--repo", "owner/repo", "--scopes", "issues,unknown"},
		{"--repo", "owner/repo", "--scopes", "wiki", "--owner", "other"},
		{"--repo", "owner/repo", "--scopes", "wiki", "--dry-run"},
		{"--repo", "owner/repo", "--scopes", "wiki", "--alias", "new"},
		{"--repo", "owner/repo", "--scopes", "wiki", "unexpected"},
	} {
		if code, _, _ := run(append([]string{"repo", "set-scopes"}, args...)...); code == 0 {
			t.Fatal("invalid, alias or broader metadata mutation accepted")
		}
	}
	gotContent, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(gotContent, content) {
		t.Fatal("scope update changed config bytes/cache mode")
	}
	store, err := cache.NewSQLiteReadOnlyStore(context.Background(), cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetRepository(context.Background(), "owner/repo")
	if err != nil || !reflect.DeepEqual(got.Scopes, []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}) || got.Owner != "owner" || got.Name != "repo" || got.DisplayName != "Fixture" {
		t.Fatal("refusal mutated binding or selected wrong cache")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitcode", "mcp")); !os.IsNotExist(err) {
		t.Fatal("scope update bootstrapped a local cache")
	}
	if code, out, _ := run("repo", "set-scopes", "--help"); code != 0 || !strings.Contains(out, "existing exact") {
		t.Fatal("scope-update help missing")
	}
}

func TestRepoSetScopesUsesPhysicalWriterWithRealFactory(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical path", true: "symlink path"}[alias], func(t *testing.T) {
			if alias && runtime.GOOS == "windows" {
				t.Skip("file symlink requires privileges on Windows")
			}
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "cache.db")
			store, err := cache.NewSQLiteStore(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://api.example.invalid/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			if alias {
				path = filepath.Join(dir, "alias.db")
				if err := os.Symlink(filepath.Join(dir, "cache.db"), path); err != nil {
					t.Fatal("cannot create fixture file alias")
				}
			}
			lease, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "fixture-physical-holder"})
			if err != nil {
				t.Fatal(err)
			}
			defer store.ReleaseWriter(ctx, lease)
			src := &repoInitLocalSource{env: map[string]string{}, cwd: dir, homeDir: dir, configDir: filepath.Join(dir, "config"), cacheDir: filepath.Join(dir, "cache")}
			var out, stderr bytes.Buffer
			args := []string{"repo", "set-scopes", "--cache-path", path, "--repo", "owner/repo", "--scopes", "wiki", "--format", "json"}
			if code := executeWithFactoryAndDeps(args, &out, &stderr, nil, localCommandDeps{Source: src}); code == 0 {
				t.Fatal("CLI crossed held physical writer")
			}
			got, err := store.GetRepository(ctx, "owner/repo")
			if err != nil || !reflect.DeepEqual(got.Scopes, []cache.RepositoryScope{cache.RepositoryScopeIssues}) {
				t.Fatal("blocked scope writer mutated the binding")
			}
			if err := store.ReleaseWriter(ctx, lease); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			stderr.Reset()
			if code := executeWithFactoryAndDeps(args, &out, &stderr, nil, localCommandDeps{Source: src}); code != 0 {
				t.Fatal("scope writer did not recover after release")
			}
		})
	}
}
