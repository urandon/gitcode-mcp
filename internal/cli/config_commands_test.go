package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/config"
)

type cliConfigSource struct {
	env       map[string]string
	homeDir   string
	configDir string
	cacheDir  string
}

func newCLIConfigSource(t *testing.T) *cliConfigSource {
	t.Helper()
	root := t.TempDir()
	return &cliConfigSource{
		env:       map[string]string{},
		homeDir:   filepath.Join(root, "home"),
		configDir: filepath.Join(root, "config"),
		cacheDir:  filepath.Join(root, "cache"),
	}
}

func (s *cliConfigSource) Env(key string) string          { return s.env[key] }
func (s *cliConfigSource) UserHomeDir() (string, error)   { return s.homeDir, nil }
func (s *cliConfigSource) UserConfigDir() (string, error) { return s.configDir, nil }
func (s *cliConfigSource) UserCacheDir() (string, error)  { return s.cacheDir, nil }
func (s *cliConfigSource) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

type statusReporter struct{ status config.CredentialStatus }

func (r statusReporter) Status(context.Context, config.EffectiveConfig) config.CredentialStatus {
	return r.status
}

func TestConfigAuthCommandsRedactedUX(t *testing.T) {
	t.Run("SCN-CONFIG-INIT-YAML-ONLY", func(t *testing.T) {
		src := newCLIConfigSource(t)
		path := filepath.Join(t.TempDir(), "config.yaml")
		src.env[config.EnvMCPConfigPath] = path
		var stdout, stderr bytes.Buffer
		code := executeWithFactoryAndDeps([]string{"config", "init"}, &stdout, &stderr, nil, localCommandDeps{Source: src})
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "config_format: yaml") {
			t.Fatalf("missing yaml output: %q", stdout.String())
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("config not written: %v", err)
		}
		if _, err := os.Stat(strings.TrimSuffix(path, ".yaml") + ".json"); !os.IsNotExist(err) {
			t.Fatalf("json config should not be written")
		}
		stdout.Reset()
		stderr.Reset()
		if code := executeWithFactoryAndDeps([]string{"config", "init"}, &stdout, &stderr, nil, localCommandDeps{Source: src}); code == 0 {
			t.Fatalf("overwrite without flag succeeded")
		}
	})

	t.Run("SCN-CONFIG-LOCATE-GITCODE-MCP-CONFIG", func(t *testing.T) {
		src := newCLIConfigSource(t)
		path := filepath.Join(t.TempDir(), "active.yaml")
		if err := os.WriteFile(path, []byte("gitcode_base_url: https://example.invalid\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		src.env[config.EnvMCPConfigPath] = path
		var stdout, stderr bytes.Buffer
		code := executeWithFactoryAndDeps([]string{"config", "locate"}, &stdout, &stderr, nil, localCommandDeps{Source: src})
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		for _, want := range []string{path, "config_source: explicit-yaml", "config_format: yaml", "config_exists: true"} {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("locate missing %q in %q", want, stdout.String())
			}
		}
	})

	t.Run("SCN-CONFIG-ENV-OVERRIDES-WIN", func(t *testing.T) {
		src := newCLIConfigSource(t)
		path := filepath.Join(t.TempDir(), "active.yaml")
		secret := "file-contained-secret"
		if err := os.WriteFile(path, []byte("cache_path: /tmp/file-cache.db\ngitcode_base_url: "+secret+"\ncredential:\n  store: env\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		src.env[config.EnvMCPConfigPath] = path
		src.env[config.EnvMCPCacheDir] = filepath.Join(t.TempDir(), "cache-dir")
		src.env[config.EnvAPIURL] = "https://api.example.invalid"
		src.env[config.EnvToken] = "secret-token-value"
		var stdout, stderr bytes.Buffer
		code := executeWithFactoryAndDeps([]string{"config", "show", "--redacted"}, &stdout, &stderr, nil, localCommandDeps{Source: src})
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		out := stdout.String() + stderr.String()
		for _, want := range []string{"config_source: explicit-yaml", "cache_path_source: env:GITCODE_MCP_CACHE_DIR", "gitcode_base_url_source: env:GITCODE_API_URL", "credential_store_mode: env", "token_present: true"} {
			if !strings.Contains(out, want) {
				t.Fatalf("show missing %q in %q", want, out)
			}
		}
		for _, forbidden := range []string{"secret-token-value", secret} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("leaked %q in %q", forbidden, out)
			}
		}
	})

	t.Run("SCN-AUTH-KEYRING-UNAVAILABLE", func(t *testing.T) {
		src := newCLIConfigSource(t)
		rawErr := "raw dbus failure details"
		reporter := statusReporter{status: config.CredentialStatus{Source: "keyring", Present: false, StoreMode: "auto", ErrorClass: "credential-store-unavailable", Remediation: "Use GITCODE_TOKEN or credential.store env."}}
		var stdout, stderr bytes.Buffer
		code := executeWithFactoryAndDeps([]string{"auth", "status"}, &stdout, &stderr, nil, localCommandDeps{Source: src, CredentialReporter: reporter})
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		out := stdout.String() + stderr.String()
		for _, want := range []string{"credential_source: keyring", "token_present: false", "credential_error_class: credential-store-unavailable", "remediation:"} {
			if !strings.Contains(out, want) {
				t.Fatalf("auth missing %q in %q", want, out)
			}
		}
		if strings.Contains(out, rawErr) {
			t.Fatalf("raw provider error leaked: %q", out)
		}
	})
}

func TestConfigCommandDoesNotOpenService(t *testing.T) {
	src := newCLIConfigSource(t)
	called := false
	factory := func(context.Context, string) (queryService, func() error, error) {
		called = true
		return nil, nil, nil
	}
	var stdout, stderr bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"auth", "status"}, &stdout, &stderr, factory, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if called {
		t.Fatalf("local command opened service")
	}
}
