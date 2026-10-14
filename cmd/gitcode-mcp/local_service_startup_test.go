package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/config"
)

type localStartupSource struct{ *testSource }

func (s localStartupSource) Env(key string) string {
	if key == config.EnvToken || key == "GITCODE_MCP_TEST_KEYCHAIN_TOKEN" {
		panic("local service startup discovered credentials")
	}
	return s.testSource.Env(key)
}

func TestServiceStartupNeverBuildsRemoteCredentials(t *testing.T) {
	src := newTestSource(t)
	path := filepath.Join(src.configDir, "config.yaml")
	src.env[config.EnvMCPConfigPath] = path
	src.files[path] = []byte("service: {mode: local}\nlocal_projects:\n  lab: {backend: local_git, project_id: lab, storage_ref: data, ref: refs/heads/main}\n")
	old := cliRoute
	defer func() { cliRoute = old }()
	called := false
	cliRoute = func(_ context.Context, args []string, _ io.Writer, _ io.Writer, deps StartupDeps) int {
		called = true
		if len(args) != 2 || args[0] != "service" || args[1] != "run" || deps.CredentialResolver != nil || deps.GitCode.Token != "" || deps.Config.Service.Mode != config.ServiceModeLocal {
			t.Fatal("local daemon boot entered remote dependency graph")
		}
		return 0
	}
	var out, errs bytes.Buffer
	if code := run([]string{"service", "run"}, nil, &out, &errs, localStartupSource{src}); code != 0 || !called {
		t.Fatal("local startup route unavailable")
	}
}
