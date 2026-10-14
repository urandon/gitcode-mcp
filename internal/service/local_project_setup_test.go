package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
)

type localSetupSource struct{ root, path string }

func (s localSetupSource) Env(key string) string {
	if key == config.EnvToken || key == "GITCODE_MCP_TEST_KEYCHAIN_TOKEN" {
		panic("setup discovered credentials")
	}
	if key == config.EnvMCPConfigPath {
		return s.path
	}
	return ""
}
func (s localSetupSource) UserHomeDir() (string, error) { return s.root, nil }
func (s localSetupSource) UserConfigDir() (string, error) {
	return filepath.Join(s.root, "config"), nil
}
func (s localSetupSource) UserCacheDir() (string, error)      { return filepath.Join(s.root, "cache"), nil }
func (s localSetupSource) ReadFile(p string) ([]byte, error)  { return os.ReadFile(p) }
func (s localSetupSource) Stat(p string) (os.FileInfo, error) { return os.Stat(p) }
func (s localSetupSource) WorkingDir() (string, error)        { return s.root, nil }

func TestLocalProjectSetupPreservesConfigAndExplicitRecovery(t *testing.T) {
	src := localSetupSource{root: t.TempDir()}
	src.path = filepath.Join(src.root, "selected.yaml")
	original := []byte("# retained trusted settings\nformat: json\nmcp: {tools: {access: read}}\nservice: {runtime_dir: fixture-runtime}\n")
	if os.WriteFile(src.path, original, 0600) != nil {
		t.Fatal("config fixture unavailable")
	}
	req := LocalProjectSetupRequest{Project: "lab", ProjectID: "lab", StorageRef: "data", Ref: "refs/heads/main", Initialize: true}
	path, _ := config.LocalProjectStorePath(src, req.StorageRef)
	if _, err := SetupLocalProject(context.Background(), src, req); err == nil {
		t.Fatal("unconfirmed setup accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unconfirmed setup created store")
	}
	req.Confirm = true
	if _, err := SetupLocalProject(context.Background(), src, req); err != nil {
		t.Fatal("confirmed setup unavailable")
	}
	before, _ := os.ReadFile(src.path)
	if !bytes.Contains(before, []byte("# retained trusted settings")) {
		t.Fatal("unrelated config comment lost")
	}
	eff, err := config.LoadEffective(src, config.Overrides{})
	if err != nil || eff.Config.MCPToolAccess != "read" || eff.Config.Service.RuntimeDir != "fixture-runtime" || eff.Config.LocalProjects["lab"].StorageRef != "data" {
		t.Fatal("setup changed unrelated config or failed registration")
	}
	req.Initialize = false
	if _, err := SetupLocalProject(context.Background(), src, req); err != nil {
		t.Fatal("explicit register replay failed")
	}
	after, _ := os.ReadFile(src.path)
	if !bytes.Equal(before, after) {
		t.Fatal("unchanged registration rewrote config")
	}
	req.Project = "alias"
	req.ReadOnly = true
	if _, err := SetupLocalProject(context.Background(), src, req); err != nil {
		t.Fatal("canonical alias registration unavailable")
	}
	// No selector retarget: the new valid Git store survives a failed config step.
	req.Project = "lab"
	req.ProjectID = "other"
	req.StorageRef = "other"
	req.ReadOnly = false
	req.Initialize = true
	configBefore, _ := os.ReadFile(src.path)
	if _, err := SetupLocalProject(context.Background(), src, req); err == nil {
		t.Fatal("existing selector silently retargeted")
	}
	configAfter, _ := os.ReadFile(src.path)
	if !bytes.Equal(configBefore, configAfter) {
		t.Fatal("failed registration changed trusted config")
	}
	other, _ := config.LocalProjectStorePath(src, "other")
	store, err := localgit.Open(context.Background(), other, localgit.Limits{})
	if err != nil {
		t.Fatal("registration failure deleted valid Git data")
	}
	snap, err := store.Snapshot(context.Background(), "refs/heads/main")
	if err != nil || snap.Project().ID != "other" {
		t.Fatal("unregistered authority not recoverable")
	}
	req.Initialize = false
	req.Project = "other"
	if _, err := SetupLocalProject(context.Background(), src, req); err != nil {
		t.Fatal("explicit orphan registration failed")
	}
	// Same logical project in a distinct physical store must not create writers.
	req.Initialize = true
	req.Project = "duplicate"
	req.StorageRef = "duplicate"
	req.ProjectID = "lab"
	if _, err := SetupLocalProject(context.Background(), src, req); err == nil {
		t.Fatal("duplicate project physical authority accepted")
	}
	if info, err := os.Stat(src.path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("trusted config permissions changed")
	}
}
