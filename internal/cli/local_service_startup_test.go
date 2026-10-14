package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/config"
)

// Reuse the regular configuration Source, but fail if the service-only path
// initializes any credential provider before dispatching the strict host.
type localServiceSource struct{ config.Source }

func (s localServiceSource) Env(key string) string {
	if key == config.EnvToken || key == "GITCODE_MCP_TEST_KEYCHAIN_TOKEN" {
		panic("local service CLI discovered credentials")
	}
	return s.Source.Env(key)
}
func TestLocalServiceCLIRejectsMissingStoreWithoutCredentialDiscovery(t *testing.T) {
	src := newCLIConfigSource(t)
	path := filepath.Join(src.configDir, "local.yaml")
	src.env[config.EnvMCPConfigPath] = path
	if os.MkdirAll(src.configDir, 0700) != nil || os.WriteFile(path, []byte("service: {mode: local}\nlocal_projects:\n  lab: {backend: local_git, project_id: lab, storage_ref: absent, ref: refs/heads/main}\n"), 0600) != nil {
		t.Fatal("local config fixture unavailable")
	}
	var out, errs bytes.Buffer
	code := ExecuteWithSourceContext(context.Background(), []string{"service", "run", "--format", "json"}, &out, &errs, localServiceSource{src})
	if code == 0 || !bytes.Contains(errs.Bytes(), []byte("local_git_unavailable")) {
		t.Fatal("missing local store was not a typed local-only failure")
	}
}
