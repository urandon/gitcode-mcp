package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memorySource struct {
	env       map[string]string
	files     map[string][]byte
	dirs      map[string]bool
	homeDir   string
	configDir string
	cacheDir  string
	cwd       string
	readErr   map[string]error
}

func newMemorySource(t *testing.T) *memorySource {
	t.Helper()
	root := t.TempDir()
	return &memorySource{
		env:       map[string]string{},
		files:     map[string][]byte{},
		dirs:      map[string]bool{},
		homeDir:   filepath.Join(root, "home"),
		configDir: filepath.Join(root, "config"),
		cacheDir:  filepath.Join(root, "cache"),
		cwd:       root,
		readErr:   map[string]error{},
	}
}

func (s *memorySource) Env(key string) string          { return s.env[key] }
func (s *memorySource) UserHomeDir() (string, error)   { return s.homeDir, nil }
func (s *memorySource) UserConfigDir() (string, error) { return s.configDir, nil }
func (s *memorySource) UserCacheDir() (string, error)  { return s.cacheDir, nil }
func (s *memorySource) WorkingDir() (string, error)    { return s.cwd, nil }
func (s *memorySource) Stat(path string) (os.FileInfo, error) {
	if _, ok := s.files[path]; ok {
		return fakeFileInfo{name: filepath.Base(path), dir: false}, nil
	}
	if s.dirs[path] {
		return fakeFileInfo{name: filepath.Base(path), dir: true}, nil
	}
	return nil, os.ErrNotExist
}
func (s *memorySource) ReadFile(path string) ([]byte, error) {
	if err := s.readErr[path]; err != nil {
		return nil, err
	}
	data, ok := s.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

type fakeFileInfo struct {
	name string
	dir  bool
}

func (f fakeFileInfo) Name() string { return f.name }
func (f fakeFileInfo) Size() int64  { return 0 }
func (f fakeFileInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

func (s *memorySource) defaultConfigPath() string {
	return filepath.Join(s.configDir, "gitcode-mcp", "config.json")
}

func TestConfigLoading(t *testing.T) {
	t.Run("SCN-CONFIG-DEFAULT-ABSENT", func(t *testing.T) {
		src := newMemorySource(t)
		cfg, err := Load(src, Overrides{})
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		wantCache := filepath.Join(src.cacheDir, "gitcode-mcp", "cache.db")
		if cfg.CachePath != wantCache || cfg.LockPath != wantCache+".lock" || cfg.Format != "text" || cfg.DefaultTimeout != 30*time.Second {
			t.Fatalf("unexpected defaults: %#v", cfg)
		}
		if cfg.MCPToolAccess != MCPToolAccessRead {
			t.Fatalf("mcp tool access = %q, want read", cfg.MCPToolAccess)
		}
	})

	t.Run("SCN-CONFIG-EXPLICIT-PATH", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "startup.json")
		src.env[EnvConfigPath] = configPath
		src.files[configPath] = []byte(`{"cache_path":"/tmp/gitcode-cache.db","default_timeout":"30s","format":"json"}`)
		cfg, err := Load(src, Overrides{})
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		if cfg.CachePath != "/tmp/gitcode-cache.db" || cfg.LockPath != "/tmp/gitcode-cache.db.lock" || cfg.Format != "json" {
			t.Fatalf("explicit config not applied: %#v", cfg)
		}
	})

	t.Run("SCN-CONFIG-EXPLICIT-MISSING", func(t *testing.T) {
		src := newMemorySource(t)
		missing := filepath.Join(src.homeDir, "missing.json")
		src.env[EnvConfigPath] = missing
		_, err := Load(src, Overrides{})
		if err == nil {
			t.Fatal("Load returned nil error")
		}
		if strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), src.homeDir) {
			t.Fatalf("error was not redacted: %v", err)
		}
	})

	t.Run("SCN-CONFIG-MALFORMED-EXPLICIT", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(src.homeDir, "bad.json")
		src.env[EnvConfigPath] = configPath
		src.files[configPath] = []byte(`{"cache_path":`)
		_, err := Load(src, Overrides{})
		if err == nil {
			t.Fatal("Load returned nil error")
		}
		if strings.Contains(err.Error(), configPath) || strings.Contains(err.Error(), src.homeDir) {
			t.Fatalf("error was not redacted: %v", err)
		}
	})

	t.Run("SCN-CONFIG-MALFORMED-DEFAULT", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := src.defaultConfigPath()
		src.files[configPath] = []byte(`{"default_timeout":`)
		_, err := Load(src, Overrides{})
		if err == nil {
			t.Fatal("Load returned nil error")
		}
		if strings.Contains(err.Error(), configPath) || strings.Contains(err.Error(), src.configDir) {
			t.Fatalf("error was not redacted: %v", err)
		}
	})

	t.Run("SCN-CONFIG-TOKEN-ENV-ONLY", func(t *testing.T) {
		src := newMemorySource(t)
		src.env[EnvToken] = "secret-token-value"
		cfg, err := Load(src, Overrides{})
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("Marshal returned error: %v", err)
		}
		if Token(src) != "secret-token-value" {
			t.Fatalf("Token returned %q", Token(src))
		}
		if strings.Contains(string(data), "secret-token-value") || strings.Contains(string(data), "token") {
			t.Fatalf("serialized Config contains token context: %s", data)
		}
	})

	t.Run("SCN-CONFIG-REDACTION", func(t *testing.T) {
		src := newMemorySource(t)
		src.env[EnvToken] = "secret-token-value"
		src.env[EnvConfigPath] = filepath.Join(src.homeDir, "config.json")
		src.files[src.env[EnvConfigPath]] = []byte(`{"cache_path":"/tmp/sensitive-cache.db"}`)
		message := "failed with secret-token-value at " + src.env[EnvConfigPath] + " under " + src.cacheDir + " using /tmp/sensitive-cache.db"
		got := RedactDiagnostic(message, src)
		for _, forbidden := range []string{"secret-token-value", src.env[EnvConfigPath], src.homeDir, src.cacheDir, "/tmp/sensitive-cache.db"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("diagnostic %q contains %q", got, forbidden)
			}
		}
	})

	t.Run("unreadable explicit config is redacted", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(src.homeDir, "unreadable.json")
		src.env[EnvConfigPath] = configPath
		src.readErr[configPath] = errors.New("permission denied")
		_, err := Load(src, Overrides{})
		if err == nil {
			t.Fatal("Load returned nil error")
		}
		if strings.Contains(err.Error(), configPath) || strings.Contains(err.Error(), src.homeDir) {
			t.Fatalf("error was not redacted: %v", err)
		}
	})

	t.Run("SCN-CONFIG-MCP-TOOL-ACCESS-JSON", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "mcp.json")
		src.env[EnvConfigPath] = configPath
		src.files[configPath] = []byte(`{"mcp":{"tools":{"access":"write"}}}`)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Config.MCPToolAccess != MCPToolAccessWrite || eff.FieldSources["mcp_tool_access"] != "legacy-json" {
			t.Fatalf("mcp tool access effective=%#v", eff)
		}
	})

	t.Run("SCN-CONFIG-MCP-TOOL-ACCESS-YAML", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "mcp.yaml")
		src.env[EnvMCPConfigPath] = configPath
		src.files[configPath] = []byte("mcp:\n  tools:\n    access: write\n")
		cfg, err := Load(src, Overrides{})
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		if cfg.MCPToolAccess != MCPToolAccessWrite {
			t.Fatalf("mcp tool access = %q, want write", cfg.MCPToolAccess)
		}
	})

	t.Run("SCN-CONFIG-MCP-TOOL-ACCESS-ENV-OVERRIDE", func(t *testing.T) {
		src := newMemorySource(t)
		configPath := filepath.Join(t.TempDir(), "mcp.json")
		src.env[EnvMCPConfigPath] = configPath
		src.env[EnvMCPToolAccess] = "read"
		src.files[configPath] = []byte(`{"mcp":{"tools":{"access":"write"}}}`)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Config.MCPToolAccess != MCPToolAccessRead || eff.FieldSources["mcp_tool_access"] != "env:"+EnvMCPToolAccess {
			t.Fatalf("mcp tool access env override effective=%#v", eff)
		}
	})

	t.Run("SCN-CONFIG-MCP-TOOL-ACCESS-INVALID", func(t *testing.T) {
		src := newMemorySource(t)
		src.env[EnvMCPToolAccess] = "admin"
		_, err := Load(src, Overrides{})
		if err == nil || !strings.Contains(err.Error(), "expected read or write") {
			t.Fatalf("Load error = %v, want invalid access", err)
		}
	})
}

func TestCLIFlagOverride(t *testing.T) {
	src := newMemorySource(t)
	configPath := filepath.Join(t.TempDir(), "startup.json")
	src.env[EnvConfigPath] = configPath
	src.files[configPath] = []byte(`{"cache_path":"/tmp/config-cache.db","default_timeout":"30s","format":"json"}`)
	cfg, err := Load(src, Overrides{DefaultTimeout: 10 * time.Second, CachePath: "/tmp/override-cache.db", Format: "text"})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DefaultTimeout != 10*time.Second {
		t.Fatalf("timeout = %s, want 10s", cfg.DefaultTimeout)
	}
	if cfg.CachePath != "/tmp/override-cache.db" || cfg.LockPath != "/tmp/override-cache.db.lock" || cfg.Format != "text" {
		t.Fatalf("overrides not applied: %#v", cfg)
	}
}
