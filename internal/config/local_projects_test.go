package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type localEffectiveSource struct{ Source }

func (s localEffectiveSource) Env(key string) string {
	if key == EnvToken {
		panic("local configuration consulted credentials")
	}
	return s.Source.Env(key)
}
func (s localEffectiveSource) WorkingDir() (string, error) {
	panic("explicit local configuration discovered cwd")
}

func TestLocalProjectEffectivePreservesTrustedOverrides(t *testing.T) {
	src := newMemorySource(t)
	path := filepath.Join(src.configDir, "local.yaml")
	src.env[EnvMCPConfigPath] = path
	src.files[path] = []byte("service: {mode: local}\nlocal_projects:\n  lab: {backend: local_git, project_id: lab, storage_ref: data, ref: refs/heads/main, rag_profile: disabled}\n")
	src.env[EnvServiceRuntimeDir] = filepath.Join(src.cacheDir, "runtime")
	src.env[EnvMCPCacheDir] = filepath.Join(src.cacheDir, "derived")
	src.env[EnvMCPToolAccess] = "read"
	src.env[EnvRAGProfile] = "fixture"
	src.env[EnvRAGModelStore] = filepath.Join(src.cacheDir, "models")
	src.env[EnvRAGProviderEndpoint] = "http://127.0.0.1:11434"
	ordinary, err := LoadEffective(src, Overrides{})
	if err != nil {
		t.Fatal("trusted fixture unavailable")
	}
	local, err := LoadLocalProjectEffective(localEffectiveSource{src})
	if err != nil || !reflect.DeepEqual(local, ordinary) || local.Config.MCPToolAccess != "read" || local.Config.Service.RuntimeDir != src.env[EnvServiceRuntimeDir] || local.Config.CachePath != filepath.Join(src.env[EnvMCPCacheDir], "cache.db") || local.Config.RAG.DefaultProfile != "fixture" {
		t.Fatal("explicit local loading changed trusted override semantics")
	}
	// Even before local mode is configured, explicit project intent does not
	// inspect cwd. The caller can refuse the non-local mode without fallback.
	src.files[path] = []byte("cache_mode: global\n")
	local, err = LoadLocalProjectEffective(localEffectiveSource{src})
	if err != nil || local.Config.Service.Mode == ServiceModeLocal || local.RepoRoot != "" {
		t.Fatal("explicit local intent discovered remote repository configuration")
	}
}

func TestLocalProjectConfigOpaqueMachineBindings(t *testing.T) {
	src := newMemorySource(t)
	path := filepath.Join(src.configDir, "local.yaml")
	src.env[EnvMCPConfigPath] = path
	src.files[path] = []byte("service:\n  mode: local\nlocal_projects:\n  lab:\n    backend: local_git\n    project_id: lab\n    storage_ref: lab-data\n    ref: refs/heads/main\n    read_only: false\n    rag_profile: disabled\n")
	eff, err := LoadEffective(src, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Config.Service.Mode != ServiceModeLocal || len(eff.Config.LocalProjects) != 1 || eff.FieldSources["local_projects"] != "explicit-yaml" {
		t.Fatal("local config was not loaded")
	}
	got, err := LocalProjectStorePath(src, eff.Config.LocalProjects["lab"].StorageRef)
	if err != nil || got != filepath.Join(src.configDir, "gitcode-mcp", "local-projects", "lab-data.git") {
		t.Fatal("opaque store resolution mismatch")
	}
	for _, bad := range []string{"../escape", "/private-coordinate", "https://example.invalid/repo", "a.b"} {
		if _, err := LocalProjectStorePath(src, bad); err == nil || strings.Contains(err.Error(), bad) {
			t.Fatal("invalid locator accepted or leaked")
		}
	}
}

func TestLocalProjectsIgnoreRepositoryOwnedProviderAndCacheOverrides(t *testing.T) {
	src := newMemorySource(t)
	root := filepath.Join(src.homeDir, "workspace", "repo")
	src.cwd = root
	src.dirs[filepath.Join(root, ".git")] = true
	global := filepath.Join(src.configDir, "local.yaml")
	src.env[EnvMCPConfigPath] = global
	trustedRAG := "rag:\n  providers:\n    fixture:\n      type: ollama\n      endpoint: https://trusted.example.invalid\n      data_boundary: local_network\n  profiles:\n    fixture:\n      provider: fixture\n      model: fixture-embedding\n      dimensions: 8\n"
	src.files[global] = []byte("service: {mode: local}\nlocal_projects:\n  lab: {backend: local_git, project_id: lab, storage_ref: data, ref: refs/heads/main, rag_profile: fixture}\n" + trustedRAG)
	repoPath := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
	poison := "cache_mode: repo-local\nrag:\n  providers:\n    fixture:\n      type: ollama\n      endpoint: https://untrusted.example.invalid\n      data_boundary: local_network\n"
	for _, repoConfig := range []string{poison, "[malformed repository config"} {
		src.files[repoPath] = []byte(repoConfig)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil || eff.Config.RAG.Providers["fixture"].Endpoint != "https://trusted.example.invalid" || eff.CachePathSource != "default" || eff.RepoRoot != "" {
			t.Fatal("working directory retargeted local project provider/cache or blocked local serving")
		}
	}
	// Ordinary GitCode mode keeps its existing repository-owned RAG/cache rules.
	src.files[global] = []byte(trustedRAG)
	src.files[repoPath] = []byte(poison)
	eff, err := LoadEffective(src, Overrides{})
	if err != nil || eff.Config.RAG.Providers["fixture"].Endpoint != "https://untrusted.example.invalid" || eff.RepoRoot != root {
		t.Fatal("local-only trust fence changed ordinary repository behavior")
	}
}

func TestLocalProjectsRepoOnlyBindingRefusesRemoteFallback(t *testing.T) {
	src := newMemorySource(t)
	root := filepath.Join(src.homeDir, "workspace", "repo")
	src.cwd = root
	src.dirs[filepath.Join(root, ".git")] = true
	path := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
	src.files[path] = []byte("local_projects:\n  lab: {backend: local_git, project_id: lab, storage_ref: data, ref: refs/heads/main}\n")
	_, err := LoadEffective(src, Overrides{})
	var coded LocalProjectConfigError
	if !errors.As(err, &coded) {
		t.Fatal("repo-only local binding did not fail closed")
	}
}

func TestLocalProjectConfigRejectsUnknownDuplicateAndPrivateFields(t *testing.T) {
	valid := "backend: local_git, project_id: lab, storage_ref: data, ref: refs/heads/main"
	for _, binding := range []string{valid + ", path: private-coordinate", valid + ", read_only: false, Read_Only: true", valid + ", storage_ref: other", valid + ", read_only: null", "backend: remote, project_id: lab, storage_ref: data, ref: refs/heads/main"} {
		src := newMemorySource(t)
		path := filepath.Join(src.configDir, "invalid.yaml")
		src.env[EnvMCPConfigPath] = path
		src.files[path] = []byte("service: {mode: local}\nlocal_projects: {lab: {" + binding + "}}\n")
		_, err := LoadEffective(src, Overrides{})
		if err == nil || strings.Contains(err.Error(), "private-coordinate") {
			t.Fatal("invalid local binding accepted or leaked")
		}
	}
	for _, binding := range []string{
		`{"backend":"local_git","project_id":"lab","storage_ref":"data","ref":"refs/heads/main","path":"private-coordinate"}`,
		`{"backend":"local_git","project_id":"lab","storage_ref":"data","ref":"refs/heads/main","Read_Only":true}`,
		`{"backend":"local_git","project_id":"lab","storage_ref":"data","storage_ref":"other","ref":"refs/heads/main"}`,
		`{"backend":"local_git","project_id":"lab","storage_ref":"data","ref":"refs/heads/main","read_only":null}`,
	} {
		src := newMemorySource(t)
		path := filepath.Join(src.configDir, "invalid.json")
		src.env[EnvMCPConfigPath] = path
		src.files[path] = []byte(`{"service":{"mode":"local"},"local_projects":{"lab":` + binding + `}}`)
		_, err := LoadEffective(src, Overrides{})
		var coded LocalProjectConfigError
		if !errors.As(err, &coded) || strings.Contains(err.Error(), "private-coordinate") {
			t.Fatal("JSON binding boundary not preserved")
		}
	}
}
