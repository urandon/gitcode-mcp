package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
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

	t.Run("SCN-AUTH-SYSTEM-KEYRING-CONFIGURED-IDENTITY", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.env[EnvMCPConfigPath] = configPath
		src.files[configPath] = []byte("credential:\n  store: keyring\n  keyring_service: gitcode-mcp-codex\n  keyring_account: write-agent\n")
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.CredentialPolicy.KeyringService != "gitcode-mcp-codex" || eff.CredentialPolicy.KeyringAccount != "write-agent" {
			t.Fatalf("credential policy=%#v", eff.CredentialPolicy)
		}
		provider := KeychainCredentialProvider{Get: func(service, user string) (string, error) {
			if service != "gitcode-mcp-codex" || user != "write-agent" {
				t.Fatalf("keyring lookup = %s/%s, want gitcode-mcp-codex/write-agent", service, user)
			}
			return "configured-secret", nil
		}}
		secret, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if secret.Value() != "configured-secret" || !status.Present || status.KeyringService != "gitcode-mcp-codex" || status.KeyringAccount != "write-agent" {
			t.Fatalf("status=%#v secret=%q", status, secret.Value())
		}
	})

	t.Run("SCN-AUTH-SYSTEM-KEYRING-ENV-OVERRIDES-IDENTITY", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.env[EnvMCPConfigPath] = configPath
		src.env[EnvKeyringService] = "gitcode-mcp-env"
		src.env[EnvKeyringAccount] = "env-agent"
		src.files[configPath] = []byte("credential:\n  store: keyring\n  keyring_service: gitcode-mcp-file\n  keyring_account: file-agent\n")
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.CredentialPolicy.KeyringService != "gitcode-mcp-env" || eff.CredentialPolicy.KeyringAccount != "env-agent" {
			t.Fatalf("credential policy=%#v", eff.CredentialPolicy)
		}
		if eff.FieldSources["credential.keyring_service"] != "env:"+EnvKeyringService || eff.FieldSources["credential.keyring_account"] != "env:"+EnvKeyringAccount {
			t.Fatalf("field sources=%#v", eff.FieldSources)
		}
	})

	t.Run("SCN-AUTH-SYSTEM-KEYRING-JSON-CONFIGURED-IDENTITY", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "config.json")
		src.env[EnvConfigPath] = configPath
		src.files[configPath] = []byte(`{"credential":{"store":"keyring","keyring_service":"gitcode-mcp-json","keyring_account":"json-agent"}}`)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.CredentialPolicy.Store != "keyring" || eff.CredentialPolicy.KeyringService != "gitcode-mcp-json" || eff.CredentialPolicy.KeyringAccount != "json-agent" {
			t.Fatalf("credential policy=%#v", eff.CredentialPolicy)
		}
	})

	t.Run("SCN-AUTH-SYSTEM-KEYRING-EXPLICIT-ACCOUNT-NO-FALLBACK", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.env[EnvMCPConfigPath] = configPath
		src.files[configPath] = []byte("credential:\n  store: keyring\n  keyring_account: write-agent\n")
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		var calls []string
		provider := KeychainCredentialProvider{Get: func(service, user string) (string, error) {
			calls = append(calls, service+"/"+user)
			return "", keyring.ErrNotFound
		}}
		_, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if status.Present || status.ErrorClass != "token-missing" {
			t.Fatalf("status=%#v", status)
		}
		if strings.Join(calls, ",") != "gitcode-mcp/write-agent" {
			t.Fatalf("calls = %#v, want only configured account", calls)
		}
		chain := ChainCredentialProvider{Providers: []CredentialProvider{
			EnvCredentialProvider{Source: src},
			KeychainCredentialProvider{Get: func(service, user string) (string, error) {
				return "", keyring.ErrNotFound
			}},
		}}
		_, chainStatus, err := chain.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve chain returned error: %v", err)
		}
		if chainStatus.KeyringService != "gitcode-mcp" || chainStatus.KeyringAccount != "write-agent" {
			t.Fatalf("chain status lost keyring identity: %#v", chainStatus)
		}
	})

	t.Run("SCN-AUTH-ENV-STORE-IGNORES-KEYRING-IDENTITY", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.env[EnvMCPConfigPath] = configPath
		src.files[configPath] = []byte("credential:\n  store: env\n  keyring_service: gitcode-mcp-ignored\n  keyring_account: ignored-agent\n")
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		provider := ChainCredentialProvider{Providers: []CredentialProvider{
			EnvCredentialProvider{Source: src},
			KeychainCredentialProvider{Get: func(service, user string) (string, error) {
				t.Fatalf("keyring called for env-only store: %s/%s", service, user)
				return "", nil
			}},
		}}
		_, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if status.Present || status.StoreMode != "env" || status.ErrorClass != "token-missing" {
			t.Fatalf("status=%#v", status)
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

	t.Run("SCN-RAG-DEFAULTS", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		wantBase := filepath.Join(src.cacheDir, "gitcode-mcp")
		if eff.Config.Service.RuntimeDir != filepath.Join(wantBase, "runtime") {
			t.Fatalf("service runtime dir=%q want under %q", eff.Config.Service.RuntimeDir, wantBase)
		}
		if eff.Config.RAG.ModelStorePath != filepath.Join(wantBase, "models") {
			t.Fatalf("model store=%q want under %q", eff.Config.RAG.ModelStorePath, wantBase)
		}
		if eff.Config.RAG.DefaultProfile != DefaultRAGProfile || eff.Config.RAG.Indexing.Profile != DefaultRAGProfile || eff.Config.RAG.Search.Profile != DefaultRAGProfile {
			t.Fatalf("default rag profile not wired through: %#v", eff.Config.RAG)
		}
		provider := eff.Config.RAG.Providers["ollama"]
		if provider.Endpoint != "http://127.0.0.1:11434" || !provider.Autostart || provider.ModelStorage.Env != "OLLAMA_MODELS" {
			t.Fatalf("default provider=%#v", provider)
		}
		profile := eff.Config.RAG.Profiles[DefaultRAGProfile]
		if profile.Provider != "ollama" || profile.Model != "qwen3-embedding:0.6b" || profile.Dimensions != 512 {
			t.Fatalf("default profile=%#v", profile)
		}
	})

	t.Run("SCN-RAG-GLOBAL-YAML", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.env[EnvMCPConfigPath] = configPath
		src.files[configPath] = []byte(strings.Join([]string{
			"service:",
			"  runtime_dir: /Volumes/fast/gitcode-mcp/runtime",
			"rag:",
			"  model_store_path: /Volumes/models/gitcode-mcp",
			"  default_profile: qwen3-custom",
			"  providers:",
			"    ollama:",
			"      endpoint: http://127.0.0.1:21434",
			"      executable: /opt/homebrew/bin/ollama",
			"      autostart: false",
			"      timeout: 45s",
			"      env:",
			"        OLLAMA_MODELS: /Volumes/models/ollama",
			"      model_storage:",
			"        mode: provider-owned",
			"        env: OLLAMA_MODELS",
			"    test-provider:",
			"      type: openai-compatible",
			"      endpoint: http://127.0.0.1:9999",
			"  profiles:",
			"    qwen3-custom:",
			"      provider: ollama",
			"      model: qwen3-embedding:0.6b",
			"      dimensions: 512",
			"      max_input_tokens: 512",
			"      batch_size: 8",
			"  indexing:",
			"    chunk_tokens: 384",
			"    overlap: 48",
			"  search:",
			"    top_k: 12",
			"    hybrid: false",
			"",
		}, "\n"))
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Config.Service.RuntimeDir != "/Volumes/fast/gitcode-mcp/runtime" || eff.Config.RAG.ModelStorePath != "/Volumes/models/gitcode-mcp" {
			t.Fatalf("global storage paths not applied: %#v", eff.Config)
		}
		if eff.Config.RAG.DefaultProfile != "qwen3-custom" || eff.Config.RAG.Indexing.Profile != "qwen3-custom" || eff.Config.RAG.Search.Profile != "qwen3-custom" {
			t.Fatalf("global profile not applied to default/index/search: %#v", eff.Config.RAG)
		}
		provider := eff.Config.RAG.Providers["ollama"]
		if provider.Endpoint != "http://127.0.0.1:21434" || provider.Executable != "/opt/homebrew/bin/ollama" || provider.Autostart || provider.Env["OLLAMA_MODELS"] != "/Volumes/models/ollama" || provider.Timeout.String() != "45s" {
			t.Fatalf("global provider not applied: %#v", provider)
		}
		if eff.Config.RAG.Indexing.ChunkTokens != 384 || eff.Config.RAG.Indexing.Overlap != 48 || eff.Config.RAG.Search.TopK != 12 || eff.Config.RAG.Search.Hybrid {
			t.Fatalf("index/search config not applied: %#v", eff.Config.RAG)
		}
		if eff.FieldSources["service.runtime_dir"] != "explicit-yaml" || eff.FieldSources["rag.model_store_path"] != "explicit-yaml" || eff.FieldSources["rag.providers.ollama.endpoint"] != "explicit-yaml" {
			t.Fatalf("field sources=%#v", eff.FieldSources)
		}
	})

	t.Run("SCN-RAG-ENV-OVERRIDES", func(t *testing.T) {
		src := newMemorySource(t)
		src.env[EnvRAGProfile] = "env-profile"
		src.env[EnvRAGModelStore] = "/Volumes/env-models/gitcode-mcp"
		src.env[EnvRAGProviderEndpoint] = "http://127.0.0.1:31434"
		src.env[EnvServiceRuntimeDir] = "/Volumes/env-runtime/gitcode-mcp"
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Config.RAG.DefaultProfile != "env-profile" || eff.Config.RAG.Indexing.Profile != "env-profile" || eff.Config.RAG.Search.Profile != "env-profile" {
			t.Fatalf("env profile did not update default/index/search: %#v", eff.Config.RAG)
		}
		if eff.Config.RAG.ModelStorePath != "/Volumes/env-models/gitcode-mcp" || eff.Config.Service.RuntimeDir != "/Volumes/env-runtime/gitcode-mcp" {
			t.Fatalf("env paths not applied: %#v", eff.Config)
		}
		if eff.Config.RAG.Providers["ollama"].Endpoint != "http://127.0.0.1:31434" {
			t.Fatalf("env provider endpoint not applied: %#v", eff.Config.RAG.Providers["ollama"])
		}
		if eff.FieldSources["rag.default_profile"] != "env:"+EnvRAGProfile || eff.FieldSources["rag.model_store_path"] != "env:"+EnvRAGModelStore || eff.FieldSources["service.runtime_dir"] != "env:"+EnvServiceRuntimeDir || eff.FieldSources["rag.providers.ollama.endpoint"] != "env:"+EnvRAGProviderEndpoint {
			t.Fatalf("field sources=%#v", eff.FieldSources)
		}
	})

	t.Run("SCN-RAG-REPO-LOCAL-LIGHTWEIGHT", func(t *testing.T) {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "workspace", "repo")
		src.cwd = filepath.Join(root, "subdir")
		src.dirs[filepath.Join(root, ".git")] = true
		repoCfg := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
		src.files[repoCfg] = []byte(strings.Join([]string{
			"rag:",
			"  default_profile: repo-profile",
			"  profiles:",
			"    repo-profile:",
			"      provider: ollama",
			"      model: repo-embedding",
			"      dimensions: 512",
			"  indexing:",
			"    chunk_tokens: 256",
			"    overlap: 32",
			"  search:",
			"    top_k: 5",
			"",
		}, "\n"))
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Config.RAG.DefaultProfile != "repo-profile" || eff.Config.RAG.Indexing.ChunkTokens != 256 || eff.Config.RAG.Search.TopK != 5 {
			t.Fatalf("repo-local rag override not applied: %#v", eff.Config.RAG)
		}
		wantGlobalModelStore := filepath.Join(src.cacheDir, "gitcode-mcp", "models")
		if eff.Config.RAG.ModelStorePath != wantGlobalModelStore || eff.Config.Service.RuntimeDir != filepath.Join(src.cacheDir, "gitcode-mcp", "runtime") {
			t.Fatalf("repo-local config changed storage paths: %#v", eff.Config)
		}
		if eff.FieldSources["rag.default_profile"] != "repo-local:"+repoCfg || eff.FieldSources["rag.search.top_k"] != "repo-local:"+repoCfg {
			t.Fatalf("field sources=%#v", eff.FieldSources)
		}
	})

	t.Run("SCN-RAG-REPO-LOCAL-REJECTS-MODEL-STORE", func(t *testing.T) {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "workspace", "repo")
		src.cwd = root
		src.dirs[filepath.Join(root, ".git")] = true
		repoCfg := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
		src.files[repoCfg] = []byte("rag:\n  model_store_path: .gitcode/models\n")
		_, err := LoadEffective(src, Overrides{})
		if err == nil || !strings.Contains(err.Error(), "repo-local") || !strings.Contains(err.Error(), "rag.model_store_path") {
			t.Fatalf("LoadEffective error=%v, want repo-local model_store_path rejection", err)
		}
	})

	t.Run("SCN-RAG-REPO-LOCAL-REJECTS-SERVICE-RUNTIME", func(t *testing.T) {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "workspace", "repo")
		src.cwd = root
		src.dirs[filepath.Join(root, ".git")] = true
		repoCfg := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
		src.files[repoCfg] = []byte("service:\n  runtime_dir: .gitcode/runtime\n")
		_, err := LoadEffective(src, Overrides{})
		if err == nil || !strings.Contains(err.Error(), "repo-local") || !strings.Contains(err.Error(), "service.runtime_dir") {
			t.Fatalf("LoadEffective error=%v, want repo-local runtime rejection", err)
		}
	})
}
