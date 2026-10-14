package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

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
