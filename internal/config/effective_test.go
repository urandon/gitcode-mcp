package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveConfigScenarios(t *testing.T) {
	t.Run("SCN-CONFIG-LOCATE-DEFAULT-YAML", func(t *testing.T) {
		src := newMemorySource(t)
		loc := Locate(src)
		want := filepath.Join(src.configDir, "gitcode-mcp", "config.yaml")
		if loc.Path != want || loc.Source != "defaults" || loc.Format != "yaml" || loc.Exists {
			t.Fatalf("locate = %#v want path %q defaults yaml missing", loc, want)
		}
	})

	t.Run("SCN-CONFIG-LEGACY-JSON-COMPAT", func(t *testing.T) {
		src := newMemorySource(t)
		legacy := filepath.Join(src.homeDir, "legacy.json")
		src.env[EnvConfigPath] = legacy
		src.files[legacy] = []byte(`{"cache_path":"/tmp/legacy-cache.db","format":"json"}`)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Location.Source != "legacy-json" || eff.Location.Format != "json" || eff.Config.CachePath != "/tmp/legacy-cache.db" || eff.FieldSources["cache_path"] != "legacy-json" {
			t.Fatalf("legacy effective config = %#v", eff)
		}
	})

	t.Run("SCN-AUTH-ENV-TOKEN-PRESENT", func(t *testing.T) {
		src := newMemorySource(t)
		src.env[EnvToken] = "secret-token-value"
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		provider := EnvCredentialProvider{Source: src}
		secret, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if secret.Value() != "secret-token-value" || !status.Present || status.Source != "env:GITCODE_TOKEN" {
			t.Fatalf("status=%#v secret=%q", status, secret.Value())
		}
		rendered := RenderCredentialStatus(status)
		if strings.Contains(rendered, "secret-token-value") {
			t.Fatalf("rendered status leaked token: %q", rendered)
		}
	})

	t.Run("SCN-AUTH-KEYRING-PRESENT", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		provider := StaticCredentialProvider{Source: "keyring", Token: "keyring-secret", StoreMode: "auto"}
		_, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if !status.Present || status.Source != "keyring" {
			t.Fatalf("status=%#v", status)
		}
		if strings.Contains(RenderCredentialStatus(status), "keyring-secret") {
			t.Fatalf("rendered keyring status leaked token")
		}
	})

	t.Run("SCN-AUTH-SYSTEM-KEYRING-PROVIDER", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		provider := KeychainCredentialProvider{Get: func(service, user string) (string, error) {
			if service != "gitcode-mcp" || user != "token" {
				t.Fatalf("keyring lookup = %s/%s, want gitcode-mcp/token", service, user)
			}
			return "keyring-secret", nil
		}}
		secret, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if secret.Value() != "keyring-secret" || !status.Present || status.Source != "keyring" || status.StoreMode != "auto" {
			t.Fatalf("status=%#v secret=%q", status, secret.Value())
		}
		if strings.Contains(RenderCredentialStatus(status), "keyring-secret") {
			t.Fatalf("rendered keyring status leaked token")
		}
	})

	t.Run("SCN-AUTH-MISSING-TOKEN", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		status := EnvCredentialProvider{Source: src}.Status(context.Background(), eff)
		if status.Present || status.ErrorClass != "token-missing" || status.Remediation == "" {
			t.Fatalf("status=%#v", status)
		}
	})

	t.Run("SCN-AUTH-DEFAULT-CHAIN-LISTS-SOURCES", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		provider := ChainCredentialProvider{Providers: []CredentialProvider{EnvCredentialProvider{Source: src}, StaticCredentialProvider{Source: "keyring", StoreMode: "auto", ErrorClass: "token-missing"}}}
		status := provider.Status(context.Background(), eff)
		if status.Present || status.ErrorClass != "token-missing" || status.Source != "missing" {
			t.Fatalf("status=%#v", status)
		}
		rendered := RenderCredentialStatus(status)
		for _, want := range []string{"available_sources:", "env:GITCODE_TOKEN", "keyring"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("rendered missing %q in %q", want, rendered)
			}
		}
	})

	t.Run("SCN-AUTH-KEYCHAIN-STORE-ALIASES-KEYRING", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.env[EnvMCPConfigPath] = configPath
		src.files[configPath] = []byte("credential:\n  store: keychain\n")
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.CredentialPolicy.Store != "keyring" {
			t.Fatalf("credential store = %q, want keyring alias", eff.CredentialPolicy.Store)
		}
	})

	t.Run("SCN-CACHE-REPO-LOCAL-DISCOVERED-FROM-NESTED-CWD", func(t *testing.T) {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "workspace", "repo")
		src.cwd = filepath.Join(root, "subdir", "nested")
		src.dirs[filepath.Join(root, ".git")] = true
		repoCfg := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
		src.files[repoCfg] = []byte("cache_mode: repo-local\n")
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		wantCache := filepath.Join(root, ".gitcode", "mcp", "cache.db")
		if eff.Config.CacheMode != CacheModeRepoLocal || eff.Config.CachePath != wantCache || eff.Config.LockPath != wantCache+".lock" {
			t.Fatalf("repo-local effective=%#v want cache %q", eff, wantCache)
		}
		if eff.RepoRoot != root || eff.RepoLocalConfigPath != repoCfg || eff.CachePathSource != "repo-local:"+repoCfg {
			t.Fatalf("repo-local discovery metadata=%#v", eff)
		}
	})

	t.Run("SCN-CACHE-EXPLICIT-PATH-BEATS-REPO-LOCAL", func(t *testing.T) {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "workspace", "repo")
		src.cwd = root
		src.dirs[filepath.Join(root, ".git")] = true
		repoCfg := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
		src.files[repoCfg] = []byte("cache_mode: repo-local\n")
		eff, err := LoadEffective(src, Overrides{CachePath: "/tmp/explicit-cache.db"})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Config.CachePath != "/tmp/explicit-cache.db" || eff.CachePathSource != "command" || eff.Config.CacheMode != CacheModeGlobal {
			t.Fatalf("explicit cache did not win: %#v", eff)
		}
	})

	t.Run("SCN-CACHE-EXPLICIT-PATH-SKIPS-MALFORMED-REPO-LOCAL-CONFIG", func(t *testing.T) {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "workspace", "repo")
		src.cwd = root
		src.dirs[filepath.Join(root, ".git")] = true
		repoCfg := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
		src.files[repoCfg] = []byte("cache_mode\n")
		eff, err := LoadEffective(src, Overrides{CachePath: "/tmp/explicit-cache.db"})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Config.CachePath != "/tmp/explicit-cache.db" || eff.CachePathSource != "command" {
			t.Fatalf("explicit cache did not bypass repo-local config: %#v", eff)
		}
	})

	t.Run("SCN-CACHE-GLOBAL-FALLBACK-WHEN-NO-REPO-OPT-IN", func(t *testing.T) {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "workspace", "repo")
		src.cwd = root
		src.dirs[filepath.Join(root, ".git")] = true
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		wantCache := filepath.Join(src.cacheDir, "gitcode-mcp", "cache.db")
		if eff.Config.CacheMode != CacheModeGlobal || eff.Config.CachePath != wantCache || eff.CachePathSource != "default" {
			t.Fatalf("fallback effective=%#v want global cache %q", eff, wantCache)
		}
	})
}
