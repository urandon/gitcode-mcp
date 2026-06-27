package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitcode-mcp/internal/diagnostics"
)

const (
	EnvConfigPath = "GITCODE_CONFIG"
	EnvToken      = "GITCODE_TOKEN"
)

type Config struct {
	CachePath       string        `json:"cache_path"`
	LockPath        string        `json:"lock_path"`
	GitCodeBaseURL  string        `json:"gitcode_base_url"`
	DefaultTimeout  time.Duration `json:"default_timeout"`
	MaxResponseSize int64         `json:"max_response_size"`
	MaxRetries      int           `json:"max_retries"`
	Format          string        `json:"format"`
	MCPToolAccess   string        `json:"mcp_tool_access"`
}

type Overrides struct {
	CachePath       string
	LockPath        string
	GitCodeBaseURL  string
	DefaultTimeout  time.Duration
	MaxResponseSize int64
	MaxRetries      *int
	Format          string
	MCPToolAccess   string
}

type Source interface {
	Env(key string) string
	UserHomeDir() (string, error)
	UserConfigDir() (string, error)
	UserCacheDir() (string, error)
	ReadFile(path string) ([]byte, error)
}

type OSSource struct{}

func (OSSource) Env(key string) string                { return os.Getenv(key) }
func (OSSource) UserHomeDir() (string, error)         { return os.UserHomeDir() }
func (OSSource) UserConfigDir() (string, error)       { return os.UserConfigDir() }
func (OSSource) UserCacheDir() (string, error)        { return os.UserCacheDir() }
func (OSSource) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func Default() Config {
	return defaultWithSource(OSSource{})
}

func Load(src Source, overrides Overrides) (Config, error) {
	if src == nil {
		src = OSSource{}
	}
	if src.Env(EnvMCPConfigPath) != "" || src.Env(EnvMCPCacheDir) != "" || src.Env(EnvAPIURL) != "" || src.Env(EnvMCPToolAccess) != "" {
		eff, err := LoadEffective(src, overrides)
		if err != nil {
			return Config{}, err
		}
		return eff.Config, nil
	}
	cfg := defaultWithSource(src)
	path, explicit := configPath(src)
	if path != "" {
		fileCfg, err := readConfigFile(src, path, explicit)
		if err != nil {
			return Config{}, errors.New(RedactDiagnostic(err.Error(), src))
		}
		cfg, err = mergeFile(cfg, fileCfg)
		if err != nil {
			return Config{}, errors.New(RedactDiagnostic(err.Error(), src))
		}
	}
	cfg = mergeOverrides(cfg, overrides)
	return cfg, nil
}

func Token(src Source) string {
	if src == nil {
		src = OSSource{}
	}
	return src.Env(EnvToken)
}

func RedactDiagnostic(message string, src Source) string {
	if src == nil {
		src = OSSource{}
	}
	values := []string{src.Env(EnvToken), src.Env(EnvConfigPath), src.Env("GITCODE_E2E_REPO_ID"), src.Env("GITCODE_E2E_OWNER"), src.Env("GITCODE_E2E_REPO"), src.Env("GITCODE_E2E_API_BASE_URL"), src.Env("GITCODE_E2E_BASE_URL")}
	if path, _ := defaultConfigPath(src); path != "" {
		values = append(values, path)
	}
	if path, _ := configPath(src); path != "" {
		values = append(values, configStringValues(src, path)...)
	}
	if home, err := src.UserHomeDir(); err == nil {
		values = append(values, home)
	}
	if dir, err := src.UserConfigDir(); err == nil {
		values = append(values, dir)
	}
	if dir, err := src.UserCacheDir(); err == nil {
		values = append(values, dir)
	}
	return diagnostics.RedactText(message, values...)
}

type fileConfig struct {
	CachePath       *string    `json:"cache_path"`
	LockPath        *string    `json:"lock_path"`
	GitCodeBaseURL  *string    `json:"gitcode_base_url"`
	DefaultTimeout  *string    `json:"default_timeout"`
	MaxResponseSize *int64     `json:"max_response_size"`
	MaxRetries      *int       `json:"max_retries"`
	Format          *string    `json:"format"`
	MCPToolAccess   *string    `json:"mcp_tool_access"`
	MCP             *MCPConfig `json:"mcp"`
}

func defaultWithSource(src Source) Config {
	cachePath := filepath.Join(".", "cache.db")
	if dir, err := src.UserCacheDir(); err == nil && dir != "" {
		cachePath = filepath.Join(dir, "gitcode-mcp", "cache.db")
	}
	return Config{
		CachePath:       cachePath,
		LockPath:        cachePath + ".lock",
		GitCodeBaseURL:  "https://api.gitcode.com/api/v5",
		DefaultTimeout:  30 * time.Second,
		MaxResponseSize: 10 << 20,
		MaxRetries:      2,
		Format:          "text",
		MCPToolAccess:   MCPToolAccessRead,
	}
}

func configPath(src Source) (string, bool) {
	if path := src.Env(EnvConfigPath); path != "" {
		return path, true
	}
	path, err := defaultConfigPath(src)
	if err != nil {
		return "", false
	}
	return path, false
}

func defaultConfigPath(src Source) (string, error) {
	dir, err := src.UserConfigDir()
	if err != nil || dir == "" {
		return "", err
	}
	return filepath.Join(dir, "gitcode-mcp", "config.json"), nil
}

func configStringValues(src Source, path string) []string {
	data, err := src.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		if s, ok := value.(string); ok && s != "" {
			values = append(values, s)
		}
	}
	return values
}

func readConfigFile(src Source, path string, explicit bool) (fileConfig, error) {
	data, err := src.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return fileConfig{}, nil
		}
		return fileConfig{}, fmt.Errorf("config: cannot read config file %s: %w", path, err)
	}
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("config: malformed config file %s: %w", path, err)
	}
	return cfg, nil
}

func mergeFile(cfg Config, file fileConfig) (Config, error) {
	if file.CachePath != nil {
		cfg.CachePath = *file.CachePath
	}
	if file.LockPath != nil {
		cfg.LockPath = *file.LockPath
	}
	if file.GitCodeBaseURL != nil {
		cfg.GitCodeBaseURL = *file.GitCodeBaseURL
	}
	if file.DefaultTimeout != nil {
		d, err := time.ParseDuration(*file.DefaultTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("config: invalid default_timeout %q: %w", *file.DefaultTimeout, err)
		}
		cfg.DefaultTimeout = d
	}
	if file.MaxResponseSize != nil {
		cfg.MaxResponseSize = *file.MaxResponseSize
	}
	if file.MaxRetries != nil {
		cfg.MaxRetries = *file.MaxRetries
	}
	if file.Format != nil {
		cfg.Format = *file.Format
	}
	if file.MCP != nil && strings.TrimSpace(file.MCP.Tools.Access) != "" {
		value := file.MCP.Tools.Access
		file.MCPToolAccess = &value
	}
	if file.MCPToolAccess != nil {
		access, err := NormalizeMCPToolAccess(*file.MCPToolAccess)
		if err != nil {
			return Config{}, err
		}
		cfg.MCPToolAccess = access
	}
	if file.LockPath == nil && file.CachePath != nil {
		cfg.LockPath = cfg.CachePath + ".lock"
	}
	return cfg, nil
}

func mergeOverrides(cfg Config, overrides Overrides) Config {
	if overrides.CachePath != "" {
		cfg.CachePath = overrides.CachePath
		if overrides.LockPath == "" {
			cfg.LockPath = overrides.CachePath + ".lock"
		}
	}
	if overrides.LockPath != "" {
		cfg.LockPath = overrides.LockPath
	}
	if overrides.GitCodeBaseURL != "" {
		cfg.GitCodeBaseURL = overrides.GitCodeBaseURL
	}
	if overrides.DefaultTimeout != 0 {
		cfg.DefaultTimeout = overrides.DefaultTimeout
	}
	if overrides.MaxResponseSize != 0 {
		cfg.MaxResponseSize = overrides.MaxResponseSize
	}
	if overrides.MaxRetries != nil {
		cfg.MaxRetries = *overrides.MaxRetries
	}
	if overrides.Format != "" {
		cfg.Format = overrides.Format
	}
	if overrides.MCPToolAccess != "" {
		cfg.MCPToolAccess = overrides.MCPToolAccess
	}
	return cfg
}

func NormalizeMCPToolAccess(value string) (string, error) {
	access := strings.ToLower(strings.TrimSpace(value))
	if access == "" {
		return MCPToolAccessRead, nil
	}
	if access != MCPToolAccessRead && access != MCPToolAccessWrite {
		return "", fmt.Errorf("config: invalid mcp tool access %q: expected read or write", value)
	}
	return access, nil
}
