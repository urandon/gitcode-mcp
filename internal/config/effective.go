package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	EnvMCPConfigPath = "GITCODE_MCP_CONFIG"
	EnvMCPCacheDir   = "GITCODE_MCP_CACHE_DIR"
	EnvAPIURL        = "GITCODE_API_URL"
	EnvMCPToolAccess = "GITCODE_MCP_TOOL_ACCESS"
)

const (
	MCPToolAccessRead  = "read"
	MCPToolAccessWrite = "write"
)

type ConfigLocation struct {
	Path     string `json:"path"`
	Source   string `json:"source"`
	Explicit bool   `json:"explicit"`
	Exists   bool   `json:"exists"`
	Format   string `json:"format"`
}

type CredentialConfig struct {
	Store string `json:"store"`
}

type MCPToolsConfig struct {
	Access string `json:"access"`
}

type MCPConfig struct {
	Tools MCPToolsConfig `json:"tools"`
}

type EffectiveConfig struct {
	Config           Config            `json:"config"`
	Location         ConfigLocation    `json:"location"`
	FieldSources     map[string]string `json:"field_sources"`
	CredentialPolicy CredentialConfig  `json:"credential_policy"`
	CachePathSource  string            `json:"cache_path_source"`
}

type CredentialStatus struct {
	Source             string               `json:"source"`
	Present            bool                 `json:"present"`
	StoreMode          string               `json:"store_mode"`
	RedactedToken      string               `json:"redacted_token,omitempty"`
	ErrorClass         string               `json:"error_class,omitempty"`
	Remediation        string               `json:"remediation,omitempty"`
	AttemptedSources   []string             `json:"attempted_sources,omitempty"`
	AvailableSources   []string             `json:"available_sources,omitempty"`
	UnavailableSources []string             `json:"unavailable_sources,omitempty"`
	AuthProbe          *CredentialAuthProbe `json:"auth_probe,omitempty"`
}

type CredentialResolutionResult struct {
	Present            bool
	Token              SecretString
	Source             string
	StoreMode          string
	AttemptedSources   []string
	AvailableSources   []string
	UnavailableSources []string
	ErrorClass         string
	Remediation        string
}

func (r CredentialResolutionResult) Status() CredentialStatus {
	return CredentialStatus{Source: r.Source, Present: r.Present, StoreMode: r.StoreMode, AttemptedSources: append([]string(nil), r.AttemptedSources...), AvailableSources: append([]string(nil), r.AvailableSources...), UnavailableSources: append([]string(nil), r.UnavailableSources...), ErrorClass: r.ErrorClass, Remediation: r.Remediation}
}

type MissingCredentialError struct {
	Status CredentialStatus
}

func (e MissingCredentialError) Error() string {
	if strings.TrimSpace(e.Status.Source) == "" || e.Status.Source == "missing" {
		return "missing_credential: live provider requires GITCODE_TOKEN or configured credential"
	}
	return "missing_credential: live provider requires GITCODE_TOKEN or configured credential; credential_source=" + e.Status.Source
}

func (e MissingCredentialError) DiagnosticCode() string { return "missing_credential" }

type CredentialAuthProbe struct {
	Status       string `json:"status"`
	FailureClass string `json:"failure_class,omitempty"`
	Message      string `json:"message,omitempty"`
}

type SecretString struct {
	value string
}

func NewSecretString(value string) SecretString { return SecretString{value: value} }
func (s SecretString) Value() string            { return s.value }

type CredentialStatusReporter interface {
	Status(context.Context, EffectiveConfig) CredentialStatus
}

type TokenResolver interface {
	ResolveToken(context.Context, EffectiveConfig) (SecretString, CredentialStatus, error)
}

type CredentialProvider interface {
	Resolve(context.Context, EffectiveConfig) (SecretString, CredentialStatus, error)
	Status(context.Context, EffectiveConfig) CredentialStatus
}

type EnvCredentialProvider struct {
	Source Source
}

func (p EnvCredentialProvider) Resolve(ctx context.Context, eff EffectiveConfig) (SecretString, CredentialStatus, error) {
	_ = ctx
	src := p.Source
	if src == nil {
		src = OSSource{}
	}
	value := strings.TrimSpace(src.Env(EnvToken))
	if value == "" {
		status := missingCredentialStatus(eff.CredentialPolicy.Store)
		status.Source = "env:" + EnvToken
		status.AttemptedSources = []string{"env:" + EnvToken}
		status.AvailableSources = []string{"env:" + EnvToken}
		return SecretString{}, status, nil
	}
	return SecretString{value: value}, CredentialStatus{Source: "env:" + EnvToken, Present: true, StoreMode: eff.CredentialPolicy.Store, AttemptedSources: []string{"env:" + EnvToken}, AvailableSources: []string{"env:" + EnvToken}}, nil
}

func (p EnvCredentialProvider) Status(ctx context.Context, eff EffectiveConfig) CredentialStatus {
	_, status, _ := p.Resolve(ctx, eff)
	return status
}

type StaticCredentialProvider struct {
	Source      string
	Token       string
	StoreMode   string
	ErrorClass  string
	Remediation string
}

func (p StaticCredentialProvider) Resolve(ctx context.Context, eff EffectiveConfig) (SecretString, CredentialStatus, error) {
	_ = ctx
	status := p.Status(ctx, eff)
	if status.Present {
		return SecretString{value: strings.TrimSpace(p.Token)}, status, nil
	}
	return SecretString{}, status, nil
}

func (p StaticCredentialProvider) Status(ctx context.Context, eff EffectiveConfig) CredentialStatus {
	_ = ctx
	store := p.StoreMode
	if store == "" {
		store = eff.CredentialPolicy.Store
	}
	source := p.Source
	if source == "" {
		source = "keyring"
	}
	if strings.TrimSpace(p.Token) != "" {
		return CredentialStatus{Source: source, Present: true, StoreMode: store, AttemptedSources: []string{source}, AvailableSources: []string{source}}
	}
	if p.ErrorClass != "" {
		return CredentialStatus{Source: source, Present: false, StoreMode: store, ErrorClass: p.ErrorClass, Remediation: p.Remediation, AttemptedSources: []string{source}, UnavailableSources: []string{source}}
	}
	status := missingCredentialStatus(store)
	status.Source = source
	status.AttemptedSources = []string{source}
	status.AvailableSources = []string{source}
	return status
}

type ChainCredentialProvider struct {
	Providers []CredentialProvider
}

func DefaultCredentialProvider(src Source) ChainCredentialProvider {
	providers := []CredentialProvider{EnvCredentialProvider{Source: src}}
	if src != nil {
		if token := strings.TrimSpace(src.Env("GITCODE_MCP_TEST_KEYCHAIN_TOKEN")); token != "" {
			providers = append(providers, StaticCredentialProvider{Source: "mock-keychain", Token: token, StoreMode: "keychain"})
			return ChainCredentialProvider{Providers: providers}
		}
	}
	providers = append(providers, KeychainCredentialProvider{})
	return ChainCredentialProvider{Providers: providers}
}

func (p ChainCredentialProvider) Resolve(ctx context.Context, eff EffectiveConfig) (SecretString, CredentialStatus, error) {
	result, err := p.ResolveLiveCredential(ctx, eff)
	if err != nil {
		var missing MissingCredentialError
		if errors.As(err, &missing) {
			return SecretString{}, missing.Status, nil
		}
		return SecretString{}, result.Status(), err
	}
	return result.Token, result.Status(), nil
}

func (p ChainCredentialProvider) ResolveLiveCredential(ctx context.Context, eff EffectiveConfig) (CredentialResolutionResult, error) {
	providers := p.Providers
	if eff.CredentialPolicy.Store == "env" && len(providers) > 0 {
		providers = providers[:1]
	}
	var attempted, available, unavailable []string
	var last CredentialStatus
	for _, provider := range providers {
		secret, status, err := provider.Resolve(ctx, eff)
		source := providerStatusSource(provider, status)
		if source != "" {
			attempted = append(attempted, source)
			if err != nil || status.ErrorClass == "credential-store-unavailable" {
				unavailable = append(unavailable, source)
			} else {
				available = append(available, source)
			}
		}
		if err != nil && status.ErrorClass == "" {
			status.ErrorClass = "credential-store-unavailable"
			status.Remediation = "Use GITCODE_TOKEN or configure credential.store: env."
		}
		status.AttemptedSources = uniqueStrings(append(status.AttemptedSources, attempted...))
		status.AvailableSources = uniqueStrings(append(status.AvailableSources, available...))
		status.UnavailableSources = uniqueStrings(append(status.UnavailableSources, unavailable...))
		last = status
		if status.Present && strings.TrimSpace(secret.Value()) != "" {
			return CredentialResolutionResult{Present: true, Token: secret, Source: status.Source, StoreMode: status.StoreMode, AttemptedSources: status.AttemptedSources, AvailableSources: status.AvailableSources, UnavailableSources: status.UnavailableSources, ErrorClass: status.ErrorClass, Remediation: status.Remediation}, nil
		}
	}
	last = missingCredentialStatus(eff.CredentialPolicy.Store)
	last.Present = false
	last.ErrorClass = firstNonEmpty(last.ErrorClass, "token-missing")
	last.Remediation = firstNonEmpty(last.Remediation, "Set GITCODE_TOKEN or configure a credential store.")
	last.AttemptedSources = uniqueStrings(append(last.AttemptedSources, attempted...))
	last.AvailableSources = uniqueStrings(append(last.AvailableSources, available...))
	last.UnavailableSources = uniqueStrings(append(last.UnavailableSources, unavailable...))
	result := CredentialResolutionResult{Present: false, Source: last.Source, StoreMode: last.StoreMode, AttemptedSources: last.AttemptedSources, AvailableSources: last.AvailableSources, UnavailableSources: last.UnavailableSources, ErrorClass: last.ErrorClass, Remediation: last.Remediation}
	return result, MissingCredentialError{Status: result.Status()}
}

func (p ChainCredentialProvider) Status(ctx context.Context, eff EffectiveConfig) CredentialStatus {
	result, err := p.ResolveLiveCredential(ctx, eff)
	if err != nil {
		var missing MissingCredentialError
		if errors.As(err, &missing) {
			return missing.Status
		}
	}
	return result.Status()
}

func Locate(src Source) ConfigLocation {
	if src == nil {
		src = OSSource{}
	}
	if path := src.Env(EnvMCPConfigPath); path != "" {
		return locationFor(src, path, "explicit-yaml", true, "yaml")
	}
	if path, err := defaultYAMLConfigPath(src); err == nil && path != "" {
		loc := locationFor(src, path, "default-yaml", false, "yaml")
		if loc.Exists {
			return loc
		}
	}
	if path := src.Env(EnvConfigPath); path != "" {
		return locationFor(src, path, "legacy-json", true, "json")
	}
	path, _ := defaultYAMLConfigPath(src)
	return locationFor(src, path, "defaults", false, "yaml")
}

func LoadEffective(src Source, overrides Overrides) (EffectiveConfig, error) {
	if src == nil {
		src = OSSource{}
	}
	eff := EffectiveConfig{
		Config:           defaultWithSource(src),
		Location:         Locate(src),
		FieldSources:     defaultFieldSources(),
		CredentialPolicy: CredentialConfig{Store: "auto"},
		CachePathSource:  "default",
	}
	if eff.Location.Path != "" && (eff.Location.Exists || eff.Location.Explicit) {
		fileCfg, cred, err := readLocatedConfig(src, eff.Location)
		if err != nil {
			return EffectiveConfig{}, errors.New(RedactDiagnostic(err.Error(), src))
		}
		var mergeErr error
		eff.Config, mergeErr = mergeFile(eff.Config, fileCfg)
		if mergeErr != nil {
			return EffectiveConfig{}, errors.New(RedactDiagnostic(mergeErr.Error(), src))
		}
		applyFileSources(eff.FieldSources, fileCfg, eff.Location.Source)
		if fileCfg.CachePath != nil {
			eff.CachePathSource = eff.Location.Source
		}
		if cred.Store != "" {
			eff.CredentialPolicy.Store = cred.Store
		}
	}
	if err := applyEnvOverrides(src, &eff); err != nil {
		return EffectiveConfig{}, errors.New(RedactDiagnostic(err.Error(), src))
	}
	beforeCache := eff.Config.CachePath
	eff.Config = mergeOverrides(eff.Config, overrides)
	applyCommandOverrideSources(&eff, overrides, beforeCache)
	return eff, nil
}

func RenderRedactedEffectiveConfig(eff EffectiveConfig, status CredentialStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "config_path: %s\n", emptyAsNone(eff.Location.Path))
	fmt.Fprintf(&b, "config_source: %s\n", eff.Location.Source)
	fmt.Fprintf(&b, "config_format: %s\n", eff.Location.Format)
	fmt.Fprintf(&b, "config_exists: %t\n", eff.Location.Exists)
	fmt.Fprintf(&b, "cache_path: %s\n", eff.Config.CachePath)
	fmt.Fprintf(&b, "cache_path_source: %s\n", eff.CachePathSource)
	fmt.Fprintf(&b, "gitcode_base_url_source: %s\n", eff.FieldSources["gitcode_base_url"])
	fmt.Fprintf(&b, "credential_store_mode: %s\n", eff.CredentialPolicy.Store)
	fmt.Fprintf(&b, "credential_source: %s\n", emptyAsNone(status.Source))
	fmt.Fprintf(&b, "token_present: %t\n", status.Present)
	if status.ErrorClass != "" {
		fmt.Fprintf(&b, "credential_error_class: %s\n", status.ErrorClass)
	}
	if status.Remediation != "" {
		fmt.Fprintf(&b, "remediation: %s\n", status.Remediation)
	}
	keys := make([]string, 0, len(eff.FieldSources))
	for key := range eff.FieldSources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "field_source.%s: %s\n", key, eff.FieldSources[key])
	}
	return b.String()
}

func RenderCredentialStatus(status CredentialStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "credential_source: %s\n", emptyAsNone(status.Source))
	fmt.Fprintf(&b, "token_present: %t\n", status.Present)
	fmt.Fprintf(&b, "credential_store_mode: %s\n", status.StoreMode)
	if len(status.AvailableSources) > 0 {
		fmt.Fprintf(&b, "available_sources: %s\n", strings.Join(status.AvailableSources, ","))
	}
	if status.RedactedToken != "" {
		fmt.Fprintf(&b, "redacted_token: %s\n", status.RedactedToken)
	}
	if status.ErrorClass != "" {
		fmt.Fprintf(&b, "credential_error_class: %s\n", status.ErrorClass)
	}
	if status.Remediation != "" {
		fmt.Fprintf(&b, "remediation: %s\n", status.Remediation)
	}
	if status.AuthProbe != nil {
		fmt.Fprintf(&b, "auth_probe_status: %s\n", status.AuthProbe.Status)
		if status.AuthProbe.FailureClass != "" {
			fmt.Fprintf(&b, "auth_probe_failure_class: %s\n", status.AuthProbe.FailureClass)
		}
		if status.AuthProbe.Message != "" {
			fmt.Fprintf(&b, "auth_probe_message: %s\n", status.AuthProbe.Message)
		}
	}
	return b.String()
}

func InitYAMLConfig(path string, overwrite bool) error {
	if path == "" {
		return fmt.Errorf("config init: path is required")
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config init: config already exists at %s; use --overwrite", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("config init: cannot inspect config path %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config init: cannot create config directory: %w", err)
	}
	return os.WriteFile(path, []byte(defaultYAMLConfig()), 0o600)
}

func defaultYAMLConfigPath(src Source) (string, error) {
	dir := src.Env("XDG_CONFIG_HOME")
	if dir == "" {
		var err error
		dir, err = src.UserConfigDir()
		if err != nil || dir == "" {
			return "", err
		}
	}
	return filepath.Join(dir, "gitcode-mcp", "config.yaml"), nil
}

func locationFor(src Source, path, source string, explicit bool, format string) ConfigLocation {
	loc := ConfigLocation{Path: path, Source: source, Explicit: explicit, Format: format}
	if path != "" {
		if _, err := src.ReadFile(path); err == nil {
			loc.Exists = true
		}
	}
	return loc
}

func readLocatedConfig(src Source, loc ConfigLocation) (fileConfig, CredentialConfig, error) {
	data, err := src.ReadFile(loc.Path)
	if err != nil {
		return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: cannot read config file %s: %w", loc.Path, err)
	}
	if loc.Format == "json" || strings.HasSuffix(loc.Path, ".json") {
		var cfg fileConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: malformed config file %s: %w", loc.Path, err)
		}
		return cfg, CredentialConfig{}, nil
	}
	return parseYAMLConfig(data, loc.Path)
}

func parseYAMLConfig(data []byte, path string) (fileConfig, CredentialConfig, error) {
	var cfg fileConfig
	var cred CredentialConfig
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, ":") {
			name := strings.TrimSuffix(line, ":")
			indent := len(raw) - len(strings.TrimLeft(raw, " "))
			if indent > 0 && section == "mcp" && name == "tools" {
				section = "mcp.tools"
			} else {
				section = name
			}
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: malformed config file %s", path)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
		if section == "mcp.tools" && key == "access" {
			cfg.MCPToolAccess = &value
			continue
		}
		if section == "credential" && key == "store" {
			cred.Store = value
			continue
		}
		switch key {
		case "cache_path":
			cfg.CachePath = &value
		case "lock_path":
			cfg.LockPath = &value
		case "gitcode_base_url":
			cfg.GitCodeBaseURL = &value
		case "default_timeout":
			cfg.DefaultTimeout = &value
		case "max_response_size":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: invalid max_response_size %q: %w", value, err)
			}
			cfg.MaxResponseSize = &n
		case "max_retries":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: invalid max_retries %q: %w", value, err)
			}
			cfg.MaxRetries = &n
		case "format":
			cfg.Format = &value
		}
	}
	return cfg, cred, nil
}

func defaultFieldSources() map[string]string {
	return map[string]string{
		"cache_path":        "default",
		"lock_path":         "default",
		"gitcode_base_url":  "default",
		"default_timeout":   "default",
		"max_response_size": "default",
		"max_retries":       "default",
		"format":            "default",
		"mcp_tool_access":   "default",
	}
}

func applyFileSources(sources map[string]string, file fileConfig, source string) {
	if file.CachePath != nil {
		sources["cache_path"] = source
		if file.LockPath == nil {
			sources["lock_path"] = source
		}
	}
	if file.LockPath != nil {
		sources["lock_path"] = source
	}
	if file.GitCodeBaseURL != nil {
		sources["gitcode_base_url"] = source
	}
	if file.DefaultTimeout != nil {
		sources["default_timeout"] = source
	}
	if file.MaxResponseSize != nil {
		sources["max_response_size"] = source
	}
	if file.MaxRetries != nil {
		sources["max_retries"] = source
	}
	if file.Format != nil {
		sources["format"] = source
	}
	if file.MCPToolAccess != nil || (file.MCP != nil && strings.TrimSpace(file.MCP.Tools.Access) != "") {
		sources["mcp_tool_access"] = source
	}
}

func applyEnvOverrides(src Source, eff *EffectiveConfig) error {
	if dir := src.Env(EnvMCPCacheDir); dir != "" {
		eff.Config.CachePath = filepath.Join(dir, "cache.db")
		eff.Config.LockPath = eff.Config.CachePath + ".lock"
		eff.FieldSources["cache_path"] = "env:" + EnvMCPCacheDir
		eff.FieldSources["lock_path"] = "env:" + EnvMCPCacheDir
		eff.CachePathSource = "env:" + EnvMCPCacheDir
	}
	if api := src.Env(EnvAPIURL); api != "" {
		eff.Config.GitCodeBaseURL = api
		eff.FieldSources["gitcode_base_url"] = "env:" + EnvAPIURL
	}
	if access := src.Env(EnvMCPToolAccess); access != "" {
		normalized, err := NormalizeMCPToolAccess(access)
		if err != nil {
			return err
		}
		eff.Config.MCPToolAccess = normalized
		eff.FieldSources["mcp_tool_access"] = "env:" + EnvMCPToolAccess
	}
	return nil
}

func applyCommandOverrideSources(eff *EffectiveConfig, overrides Overrides, beforeCache string) {
	if overrides.CachePath != "" && eff.Config.CachePath != beforeCache {
		eff.FieldSources["cache_path"] = "command"
		eff.CachePathSource = "command"
		if overrides.LockPath == "" {
			eff.FieldSources["lock_path"] = "command"
		}
	}
	if overrides.LockPath != "" {
		eff.FieldSources["lock_path"] = "command"
	}
	if overrides.GitCodeBaseURL != "" {
		eff.FieldSources["gitcode_base_url"] = "command"
	}
	if overrides.DefaultTimeout != 0 {
		eff.FieldSources["default_timeout"] = "command"
	}
	if overrides.MaxResponseSize != 0 {
		eff.FieldSources["max_response_size"] = "command"
	}
	if overrides.MaxRetries != nil {
		eff.FieldSources["max_retries"] = "command"
	}
	if overrides.Format != "" {
		eff.FieldSources["format"] = "command"
	}
	if overrides.MCPToolAccess != "" {
		eff.FieldSources["mcp_tool_access"] = "command"
	}
}

func missingCredentialStatus(store string) CredentialStatus {
	if store == "" {
		store = "auto"
	}
	return CredentialStatus{Source: "missing", Present: false, StoreMode: store, ErrorClass: "token-missing", Remediation: "Set GITCODE_TOKEN or configure a credential store."}
}

func providerStatusSource(provider CredentialProvider, status CredentialStatus) string {
	if _, ok := provider.(EnvCredentialProvider); ok {
		return "env:" + EnvToken
	}
	if _, ok := provider.(KeychainCredentialProvider); ok {
		return "keychain"
	}
	return status.Source
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func emptyAsNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func defaultYAMLConfig() string {
	return "gitcode_base_url: https://api.gitcode.com/api/v5\ncredential:\n  store: auto\n"
}
