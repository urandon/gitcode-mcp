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
	EnvMCPConfigPath  = "GITCODE_MCP_CONFIG"
	EnvMCPCacheDir    = "GITCODE_MCP_CACHE_DIR"
	EnvAPIURL         = "GITCODE_API_URL"
	EnvMCPToolAccess  = "GITCODE_MCP_TOOL_ACCESS"
	EnvKeyringService = "GITCODE_MCP_KEYRING_SERVICE"
	EnvKeyringAccount = "GITCODE_MCP_KEYRING_ACCOUNT"
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
	Store          string `json:"store"`
	KeyringService string `json:"keyring_service,omitempty"`
	KeyringAccount string `json:"keyring_account,omitempty"`
}

type MCPToolsConfig struct {
	Access string `json:"access"`
}

type MCPConfig struct {
	Tools MCPToolsConfig `json:"tools"`
}

type EffectiveConfig struct {
	Config              Config            `json:"config"`
	Location            ConfigLocation    `json:"location"`
	FieldSources        map[string]string `json:"field_sources"`
	CredentialPolicy    CredentialConfig  `json:"credential_policy"`
	CachePathSource     string            `json:"cache_path_source"`
	RepoRoot            string            `json:"repo_root,omitempty"`
	RepoLocalConfigPath string            `json:"repo_local_config_path,omitempty"`
}

type CredentialStatus struct {
	Source             string               `json:"source"`
	Present            bool                 `json:"present"`
	StoreMode          string               `json:"store_mode"`
	KeyringService     string               `json:"keyring_service,omitempty"`
	KeyringAccount     string               `json:"keyring_account,omitempty"`
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
	KeyringService     string
	KeyringAccount     string
	AttemptedSources   []string
	AvailableSources   []string
	UnavailableSources []string
	ErrorClass         string
	Remediation        string
}

func (r CredentialResolutionResult) Status() CredentialStatus {
	return CredentialStatus{Source: r.Source, Present: r.Present, StoreMode: r.StoreMode, KeyringService: r.KeyringService, KeyringAccount: r.KeyringAccount, AttemptedSources: append([]string(nil), r.AttemptedSources...), AvailableSources: append([]string(nil), r.AvailableSources...), UnavailableSources: append([]string(nil), r.UnavailableSources...), ErrorClass: r.ErrorClass, Remediation: r.Remediation}
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
			providers = append(providers, StaticCredentialProvider{Source: "mock-keyring", Token: token, StoreMode: "keyring"})
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
			return CredentialResolutionResult{Present: true, Token: secret, Source: status.Source, StoreMode: status.StoreMode, KeyringService: status.KeyringService, KeyringAccount: status.KeyringAccount, AttemptedSources: status.AttemptedSources, AvailableSources: status.AvailableSources, UnavailableSources: status.UnavailableSources, ErrorClass: status.ErrorClass, Remediation: status.Remediation}, nil
		}
	}
	lastKeyringService := last.KeyringService
	lastKeyringAccount := last.KeyringAccount
	last = missingCredentialStatus(eff.CredentialPolicy.Store)
	last.KeyringService = lastKeyringService
	last.KeyringAccount = lastKeyringAccount
	last.Present = false
	last.ErrorClass = firstNonEmpty(last.ErrorClass, "token-missing")
	last.Remediation = firstNonEmpty(last.Remediation, "Set GITCODE_TOKEN or configure a credential store.")
	last.AttemptedSources = uniqueStrings(append(last.AttemptedSources, attempted...))
	last.AvailableSources = uniqueStrings(append(last.AvailableSources, available...))
	last.UnavailableSources = uniqueStrings(append(last.UnavailableSources, unavailable...))
	result := CredentialResolutionResult{Present: false, Source: last.Source, StoreMode: last.StoreMode, KeyringService: last.KeyringService, KeyringAccount: last.KeyringAccount, AttemptedSources: last.AttemptedSources, AvailableSources: last.AvailableSources, UnavailableSources: last.UnavailableSources, ErrorClass: last.ErrorClass, Remediation: last.Remediation}
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
		CredentialPolicy: defaultCredentialConfig(),
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
			store, err := NormalizeCredentialStore(cred.Store)
			if err != nil {
				return EffectiveConfig{}, errors.New(RedactDiagnostic(err.Error(), src))
			}
			eff.CredentialPolicy.Store = store
		}
		if strings.TrimSpace(cred.KeyringService) != "" {
			eff.CredentialPolicy.KeyringService = strings.TrimSpace(cred.KeyringService)
			eff.FieldSources["credential.keyring_service"] = eff.Location.Source
		}
		if strings.TrimSpace(cred.KeyringAccount) != "" {
			eff.CredentialPolicy.KeyringAccount = strings.TrimSpace(cred.KeyringAccount)
			eff.FieldSources["credential.keyring_account"] = eff.Location.Source
		}
	}
	if err := applyEnvOverrides(src, &eff); err != nil {
		return EffectiveConfig{}, errors.New(RedactDiagnostic(err.Error(), src))
	}
	if overrides.CachePath == "" {
		if err := applyRepoLocalCache(src, &eff); err != nil {
			return EffectiveConfig{}, errors.New(RedactDiagnostic(err.Error(), src))
		}
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
	fmt.Fprintf(&b, "cache_mode: %s\n", eff.Config.CacheMode)
	if eff.RepoRoot != "" {
		fmt.Fprintf(&b, "repo_root: %s\n", eff.RepoRoot)
	}
	if eff.RepoLocalConfigPath != "" {
		fmt.Fprintf(&b, "repo_local_config_path: %s\n", eff.RepoLocalConfigPath)
	}
	fmt.Fprintf(&b, "service_runtime_dir: %s\n", eff.Config.Service.RuntimeDir)
	fmt.Fprintf(&b, "rag_default_profile: %s\n", eff.Config.RAG.DefaultProfile)
	fmt.Fprintf(&b, "rag_model_store_path: %s\n", eff.Config.RAG.ModelStorePath)
	providerName := activeRAGProviderName(eff.Config)
	fmt.Fprintf(&b, "rag_active_provider: %s\n", providerName)
	if provider, ok := eff.Config.RAG.Providers[providerName]; ok {
		fmt.Fprintf(&b, "rag_provider_endpoint: %s\n", provider.Endpoint)
	}
	fmt.Fprintf(&b, "gitcode_base_url_source: %s\n", eff.FieldSources["gitcode_base_url"])
	fmt.Fprintf(&b, "credential_store_mode: %s\n", eff.CredentialPolicy.Store)
	fmt.Fprintf(&b, "credential_keyring_service: %s\n", eff.CredentialPolicy.KeyringService)
	fmt.Fprintf(&b, "credential_keyring_account: %s\n", eff.CredentialPolicy.KeyringAccount)
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
	if status.KeyringService != "" {
		fmt.Fprintf(&b, "credential_keyring_service: %s\n", status.KeyringService)
	}
	if status.KeyringAccount != "" {
		fmt.Fprintf(&b, "credential_keyring_account: %s\n", status.KeyringAccount)
	}
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

func defaultCredentialConfig() CredentialConfig {
	return CredentialConfig{Store: "auto", KeyringService: keyringService, KeyringAccount: keyringUser}
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
		var cred CredentialConfig
		if cfg.Credential != nil {
			cred = *cfg.Credential
		}
		return cfg, cred, nil
	}
	return parseYAMLConfig(data, loc.Path)
}

type yamlSection struct {
	indent int
	name   string
}

func parseYAMLConfig(data []byte, path string) (fileConfig, CredentialConfig, error) {
	var cfg fileConfig
	var cred CredentialConfig
	var stack []yamlSection
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		if strings.HasSuffix(line, ":") {
			stack = append(stack, yamlSection{indent: indent, name: strings.TrimSpace(strings.TrimSuffix(line, ":"))})
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: malformed config file %s", path)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
		section := yamlSectionPath(stack)
		if section == "mcp.tools" && key == "access" {
			cfg.MCPToolAccess = &value
			continue
		}
		if section == "cache" && key == "mode" {
			cfg.CacheMode = &value
			continue
		}
		if section == "credential" && key == "store" {
			cred.Store = value
			continue
		}
		if section == "credential" && key == "keyring_service" {
			cred.KeyringService = value
			continue
		}
		if section == "credential" && key == "keyring_account" {
			cred.KeyringAccount = value
			continue
		}
		if err := setYAMLRAGValue(&cfg, section, key, value); err != nil {
			return fileConfig{}, CredentialConfig{}, err
		}
		if isRAGYAMLSection(section) || section == "service" {
			continue
		}
		if section != "" {
			continue
		}
		switch key {
		case "cache_path":
			cfg.CachePath = &value
		case "lock_path":
			cfg.LockPath = &value
		case "cache_mode":
			cfg.CacheMode = &value
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
		case "rate_limit_rps":
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: invalid rate_limit_rps %q: %w", value, err)
			}
			cfg.RateLimitRPS = &n
		case "rate_limit_burst":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fileConfig{}, CredentialConfig{}, fmt.Errorf("config: invalid rate_limit_burst %q: %w", value, err)
			}
			cfg.RateLimitBurst = &n
		case "format":
			cfg.Format = &value
		}
	}
	return cfg, cred, nil
}

func yamlSectionPath(stack []yamlSection) string {
	names := make([]string, 0, len(stack))
	for _, section := range stack {
		names = append(names, section.name)
	}
	return strings.Join(names, ".")
}

func setYAMLRAGValue(cfg *fileConfig, section, key, value string) error {
	switch {
	case section == "service":
		if cfg.Service == nil {
			cfg.Service = &serviceFileConfig{}
		}
		if key == "runtime_dir" {
			cfg.Service.RuntimeDir = &value
		}
	case section == "rag":
		rag := ensureRAGFile(cfg)
		switch key {
		case "model_store_path":
			rag.ModelStorePath = &value
		case "default_profile":
			rag.DefaultProfile = &value
		}
	case section == "rag.indexing":
		rag := ensureRAGFile(cfg)
		if rag.Indexing == nil {
			rag.Indexing = &ragIndexingFileConfig{}
		}
		n, err := parseOptionalIntYAML(section, key, value)
		if err != nil {
			return err
		}
		switch key {
		case "profile":
			rag.Indexing.Profile = &value
		case "chunk_tokens":
			rag.Indexing.ChunkTokens = n
		case "overlap":
			rag.Indexing.Overlap = n
		case "batch_size":
			rag.Indexing.BatchSize = n
		}
	case section == "rag.search":
		rag := ensureRAGFile(cfg)
		if rag.Search == nil {
			rag.Search = &ragSearchFileConfig{}
		}
		switch key {
		case "profile":
			rag.Search.Profile = &value
		case "top_k":
			n, err := parseOptionalIntYAML(section, key, value)
			if err != nil {
				return err
			}
			rag.Search.TopK = n
		case "hybrid":
			v, err := parseBoolYAML(section, key, value)
			if err != nil {
				return err
			}
			rag.Search.Hybrid = &v
		}
	case strings.HasPrefix(section, "rag.providers."):
		return setYAMLRAGProviderValue(cfg, section, key, value)
	case strings.HasPrefix(section, "rag.profiles."):
		return setYAMLRAGProfileValue(cfg, section, key, value)
	}
	return nil
}

func setYAMLRAGProviderValue(cfg *fileConfig, section, key, value string) error {
	parts := strings.Split(section, ".")
	if len(parts) < 3 {
		return nil
	}
	rag := ensureRAGFile(cfg)
	if rag.Providers == nil {
		rag.Providers = map[string]ragProviderFileConfig{}
	}
	name := parts[2]
	provider := rag.Providers[name]
	if len(parts) == 3 {
		switch key {
		case "type":
			provider.Type = &value
		case "endpoint":
			provider.Endpoint = &value
		case "executable":
			provider.Executable = &value
		case "startup":
			provider.Startup = &value
		case "autostart":
			v, err := parseBoolYAML(section, key, value)
			if err != nil {
				return err
			}
			provider.Autostart = &v
		case "install_hints":
			provider.InstallHints = splitYAMLList(value)
		case "timeout":
			provider.Timeout = &value
		}
	} else if len(parts) == 4 && parts[3] == "env" {
		if provider.Env == nil {
			provider.Env = map[string]string{}
		}
		provider.Env[key] = value
	} else if len(parts) == 4 && parts[3] == "model_storage" {
		if provider.ModelStorage == nil {
			provider.ModelStorage = &ragModelStorageFileConfig{}
		}
		switch key {
		case "mode":
			provider.ModelStorage.Mode = &value
		case "path":
			provider.ModelStorage.Path = &value
		case "env":
			provider.ModelStorage.Env = &value
		}
	}
	rag.Providers[name] = provider
	return nil
}

func setYAMLRAGProfileValue(cfg *fileConfig, section, key, value string) error {
	parts := strings.Split(section, ".")
	if len(parts) != 3 {
		return nil
	}
	rag := ensureRAGFile(cfg)
	if rag.Profiles == nil {
		rag.Profiles = map[string]ragProfileFileConfig{}
	}
	name := parts[2]
	profile := rag.Profiles[name]
	n, err := parseOptionalIntYAML(section, key, value)
	if err != nil {
		return err
	}
	switch key {
	case "provider":
		profile.Provider = &value
	case "model":
		profile.Model = &value
	case "dimensions":
		profile.Dimensions = n
	case "max_input_tokens":
		profile.MaxInputTokens = n
	case "batch_size":
		profile.BatchSize = n
	}
	rag.Profiles[name] = profile
	return nil
}

func ensureRAGFile(cfg *fileConfig) *ragFileConfig {
	if cfg.RAG == nil {
		cfg.RAG = &ragFileConfig{}
	}
	return cfg.RAG
}

func isRAGYAMLSection(section string) bool {
	return section == "rag" || strings.HasPrefix(section, "rag.")
}

func parseOptionalIntYAML(section, key, value string) (*int, error) {
	switch key {
	case "chunk_tokens", "overlap", "batch_size", "top_k", "dimensions", "max_input_tokens":
		n, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("config: invalid %s.%s %q: %w", section, key, value, err)
		}
		return &n, nil
	default:
		return nil, nil
	}
}

func parseBoolYAML(section, key, value string) (bool, error) {
	v, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("config: invalid %s.%s %q: %w", section, key, value, err)
	}
	return v, nil
}

func splitYAMLList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == '|'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func defaultFieldSources() map[string]string {
	return map[string]string{
		"cache_path":                    "default",
		"lock_path":                     "default",
		"cache_mode":                    "default",
		"gitcode_base_url":              "default",
		"default_timeout":               "default",
		"max_response_size":             "default",
		"max_retries":                   "default",
		"rate_limit_rps":                "default",
		"rate_limit_burst":              "default",
		"format":                        "default",
		"mcp_tool_access":               "default",
		"credential.keyring_service":    "default",
		"credential.keyring_account":    "default",
		"service.runtime_dir":           "default",
		"rag.model_store_path":          "default",
		"rag.default_profile":           "default",
		"rag.providers.ollama.endpoint": "default",
		"rag.indexing.profile":          "default",
		"rag.search.profile":            "default",
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
	if file.CacheMode != nil {
		sources["cache_mode"] = source
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
	if file.RateLimitRPS != nil {
		sources["rate_limit_rps"] = source
	}
	if file.RateLimitBurst != nil {
		sources["rate_limit_burst"] = source
	}
	if file.Format != nil {
		sources["format"] = source
	}
	if file.MCPToolAccess != nil || (file.MCP != nil && strings.TrimSpace(file.MCP.Tools.Access) != "") {
		sources["mcp_tool_access"] = source
	}
	applyServiceFileSources(sources, file.Service, source)
	applyRAGFileSources(sources, file.RAG, source)
}

func applyServiceFileSources(sources map[string]string, file *serviceFileConfig, source string) {
	if file == nil {
		return
	}
	if file.RuntimeDir != nil {
		sources["service.runtime_dir"] = source
	}
}

func applyRAGFileSources(sources map[string]string, file *ragFileConfig, source string) {
	if file == nil {
		return
	}
	if file.ModelStorePath != nil {
		sources["rag.model_store_path"] = source
	}
	if file.DefaultProfile != nil {
		sources["rag.default_profile"] = source
		sources["rag.indexing.profile"] = source
		sources["rag.search.profile"] = source
	}
	for name, provider := range file.Providers {
		prefix := "rag.providers." + name + "."
		if provider.Type != nil {
			sources[prefix+"type"] = source
		}
		if provider.Endpoint != nil {
			sources[prefix+"endpoint"] = source
		}
		if provider.Executable != nil {
			sources[prefix+"executable"] = source
		}
		if provider.Startup != nil {
			sources[prefix+"startup"] = source
		}
		if provider.Autostart != nil {
			sources[prefix+"autostart"] = source
		}
		if provider.Timeout != nil {
			sources[prefix+"timeout"] = source
		}
		if provider.ModelStorage != nil {
			if provider.ModelStorage.Mode != nil {
				sources[prefix+"model_storage.mode"] = source
			}
			if provider.ModelStorage.Path != nil {
				sources[prefix+"model_storage.path"] = source
			}
			if provider.ModelStorage.Env != nil {
				sources[prefix+"model_storage.env"] = source
			}
		}
		for key := range provider.Env {
			sources[prefix+"env."+key] = source
		}
	}
	for name, profile := range file.Profiles {
		prefix := "rag.profiles." + name + "."
		if profile.Provider != nil {
			sources[prefix+"provider"] = source
		}
		if profile.Model != nil {
			sources[prefix+"model"] = source
		}
		if profile.Dimensions != nil {
			sources[prefix+"dimensions"] = source
		}
		if profile.MaxInputTokens != nil {
			sources[prefix+"max_input_tokens"] = source
		}
		if profile.BatchSize != nil {
			sources[prefix+"batch_size"] = source
		}
	}
	if file.Indexing != nil {
		if file.Indexing.Profile != nil {
			sources["rag.indexing.profile"] = source
		}
		if file.Indexing.ChunkTokens != nil {
			sources["rag.indexing.chunk_tokens"] = source
		}
		if file.Indexing.Overlap != nil {
			sources["rag.indexing.overlap"] = source
		}
		if file.Indexing.BatchSize != nil {
			sources["rag.indexing.batch_size"] = source
		}
	}
	if file.Search != nil {
		if file.Search.Profile != nil {
			sources["rag.search.profile"] = source
		}
		if file.Search.TopK != nil {
			sources["rag.search.top_k"] = source
		}
		if file.Search.Hybrid != nil {
			sources["rag.search.hybrid"] = source
		}
	}
}

func applyRepoLocalCache(src Source, eff *EffectiveConfig) error {
	if eff.CachePathSource != "default" {
		if eff.Config.CacheMode == "" {
			eff.Config.CacheMode = CacheModeGlobal
		}
		return nil
	}
	root, repoConfigPath, repoFile, err := discoverRepoLocalConfig(src)
	if err != nil {
		return err
	}
	eff.RepoRoot = root
	eff.RepoLocalConfigPath = repoConfigPath
	repoMode := ""
	if repoFile.CacheMode != nil {
		mode, err := NormalizeCacheMode(*repoFile.CacheMode)
		if err != nil {
			return err
		}
		repoMode = mode
	}
	mode := eff.Config.CacheMode
	modeSource := eff.FieldSources["cache_mode"]
	if mode == "" {
		mode = CacheModeGlobal
	}
	if repoMode != "" && eff.CachePathSource == "default" && modeSource == "default" {
		mode = repoMode
		modeSource = "repo-local:" + repoConfigPath
	}
	if err := applyRepoLocalRAG(root, repoConfigPath, repoFile, eff); err != nil {
		return err
	}
	eff.Config.CacheMode = mode
	eff.FieldSources["cache_mode"] = modeSource
	if mode != CacheModeRepoLocal || root == "" || eff.CachePathSource != "default" {
		return nil
	}
	cachePath := filepath.Join(root, ".gitcode", "mcp", "cache.db")
	eff.Config.CachePath = cachePath
	eff.Config.LockPath = cachePath + ".lock"
	eff.CachePathSource = modeSource
	eff.FieldSources["cache_path"] = modeSource
	eff.FieldSources["lock_path"] = modeSource
	return nil
}

func applyRepoLocalRAG(root, repoConfigPath string, repoFile fileConfig, eff *EffectiveConfig) error {
	if repoConfigPath == "" || repoFile.RAG == nil && repoFile.Service == nil {
		return nil
	}
	if err := validateRepoLocalRuntimeStorage(root, repoConfigPath, repoFile); err != nil {
		return err
	}
	var err error
	eff.Config, err = mergeRAGFile(eff.Config, repoFile.RAG)
	if err != nil {
		return err
	}
	applyRAGFileSources(eff.FieldSources, repoFile.RAG, "repo-local:"+repoConfigPath)
	return nil
}

func validateRepoLocalRuntimeStorage(root, repoConfigPath string, file fileConfig) error {
	if file.Service != nil && file.Service.RuntimeDir != nil {
		return fmt.Errorf("config: repo-local config %s cannot set service.runtime_dir; configure service runtime globally or with %s", repoConfigPath, EnvServiceRuntimeDir)
	}
	if file.RAG != nil && file.RAG.ModelStorePath != nil {
		return fmt.Errorf("config: repo-local config %s cannot set rag.model_store_path; configure model storage globally or with %s", repoConfigPath, EnvRAGModelStore)
	}
	if file.RAG != nil {
		for name, provider := range file.RAG.Providers {
			if provider.ModelStorage != nil && provider.ModelStorage.Path != nil {
				return fmt.Errorf("config: repo-local config %s cannot set rag.providers.%s.model_storage.path; configure provider model storage globally", repoConfigPath, name)
			}
			if provider.Env != nil {
				for key, value := range provider.Env {
					if strings.TrimSpace(key) == "OLLAMA_MODELS" && pathUnsafeForRepoLocal(root, value) {
						return fmt.Errorf("config: repo-local config %s cannot set rag.providers.%s.env.%s to a repo-local path; configure provider model storage globally", repoConfigPath, name, key)
					}
				}
			}
		}
	}
	return nil
}

func pathUnsafeForRepoLocal(root, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		return true
	}
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func discoverRepoLocalConfig(src Source) (string, string, fileConfig, error) {
	cwd, ok := workingDir(src)
	if !ok || strings.TrimSpace(cwd) == "" {
		return "", "", fileConfig{}, nil
	}
	root := findGitRoot(src, cwd)
	if root == "" {
		return "", "", fileConfig{}, nil
	}
	path := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
	data, err := src.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return root, path, fileConfig{}, nil
		}
		return "", "", fileConfig{}, fmt.Errorf("config: cannot read repo-local config file %s: %w", path, err)
	}
	cfg, _, err := parseYAMLConfig(data, path)
	if err != nil {
		return "", "", fileConfig{}, err
	}
	return root, path, cfg, nil
}

func workingDir(src Source) (string, bool) {
	if wd, ok := src.(WorkingDirSource); ok {
		dir, err := wd.WorkingDir()
		if err == nil {
			return filepath.Clean(dir), true
		}
	}
	return "", false
}

func findGitRoot(src Source, start string) string {
	dir := filepath.Clean(start)
	for {
		if isGitRoot(src, dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isGitRoot(src Source, dir string) bool {
	stat, ok := src.(StatSource)
	if !ok {
		return false
	}
	_, err := stat.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func applyEnvOverrides(src Source, eff *EffectiveConfig) error {
	if dir := src.Env(EnvMCPCacheDir); dir != "" {
		eff.Config.CachePath = filepath.Join(dir, "cache.db")
		eff.Config.LockPath = eff.Config.CachePath + ".lock"
		eff.Config.CacheMode = CacheModeGlobal
		eff.FieldSources["cache_path"] = "env:" + EnvMCPCacheDir
		eff.FieldSources["lock_path"] = "env:" + EnvMCPCacheDir
		eff.FieldSources["cache_mode"] = "env:" + EnvMCPCacheDir
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
	if service := strings.TrimSpace(src.Env(EnvKeyringService)); service != "" {
		eff.CredentialPolicy.KeyringService = service
		eff.FieldSources["credential.keyring_service"] = "env:" + EnvKeyringService
	}
	if account := strings.TrimSpace(src.Env(EnvKeyringAccount)); account != "" {
		eff.CredentialPolicy.KeyringAccount = account
		eff.FieldSources["credential.keyring_account"] = "env:" + EnvKeyringAccount
	}
	if runtimeDir := strings.TrimSpace(src.Env(EnvServiceRuntimeDir)); runtimeDir != "" {
		eff.Config.Service.RuntimeDir = runtimeDir
		eff.FieldSources["service.runtime_dir"] = "env:" + EnvServiceRuntimeDir
	}
	if profile := strings.TrimSpace(src.Env(EnvRAGProfile)); profile != "" {
		setDefaultRAGProfile(&eff.Config, profile)
		eff.FieldSources["rag.default_profile"] = "env:" + EnvRAGProfile
		eff.FieldSources["rag.indexing.profile"] = "env:" + EnvRAGProfile
		eff.FieldSources["rag.search.profile"] = "env:" + EnvRAGProfile
	}
	if modelStore := strings.TrimSpace(src.Env(EnvRAGModelStore)); modelStore != "" {
		eff.Config.RAG.ModelStorePath = modelStore
		eff.FieldSources["rag.model_store_path"] = "env:" + EnvRAGModelStore
	}
	if endpoint := strings.TrimSpace(src.Env(EnvRAGProviderEndpoint)); endpoint != "" {
		providerName := activeRAGProviderName(eff.Config)
		if eff.Config.RAG.Providers == nil {
			eff.Config.RAG.Providers = map[string]RAGProviderConfig{}
		}
		provider := eff.Config.RAG.Providers[providerName]
		provider.Endpoint = endpoint
		eff.Config.RAG.Providers[providerName] = provider
		eff.FieldSources["rag.providers."+providerName+".endpoint"] = "env:" + EnvRAGProviderEndpoint
	}
	return nil
}

func applyCommandOverrideSources(eff *EffectiveConfig, overrides Overrides, beforeCache string) {
	if overrides.CachePath != "" && eff.Config.CachePath != beforeCache {
		eff.FieldSources["cache_path"] = "command"
		eff.FieldSources["cache_mode"] = "command"
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
	if overrides.RateLimitRPS != nil {
		eff.FieldSources["rate_limit_rps"] = "command"
	}
	if overrides.RateLimitBurst != nil {
		eff.FieldSources["rate_limit_burst"] = "command"
	}
	if overrides.Format != "" {
		eff.FieldSources["format"] = "command"
	}
	if overrides.MCPToolAccess != "" {
		eff.FieldSources["mcp_tool_access"] = "command"
	}
	if overrides.CacheMode != "" {
		eff.FieldSources["cache_mode"] = "command"
	}
}

func missingCredentialStatus(store string) CredentialStatus {
	if store == "" {
		store = "auto"
	}
	return CredentialStatus{Source: "missing", Present: false, StoreMode: store, ErrorClass: "token-missing", Remediation: "Set GITCODE_TOKEN or configure a credential store."}
}

func NormalizeCredentialStore(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "auto":
		return "auto", nil
	case "env":
		return "env", nil
	case "keyring", "keychain":
		return "keyring", nil
	default:
		return "", fmt.Errorf("config: invalid credential.store %q: expected auto, env, keyring, or keychain", value)
	}
}

func providerStatusSource(provider CredentialProvider, status CredentialStatus) string {
	if _, ok := provider.(EnvCredentialProvider); ok {
		return "env:" + EnvToken
	}
	switch provider.(type) {
	case KeychainCredentialProvider, *KeychainCredentialProvider:
		return "keyring"
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
	return strings.Join([]string{
		"gitcode_base_url: https://api.gitcode.com/api/v5",
		"credential:",
		"  store: auto",
		"service:",
		"  # Override with GITCODE_MCP_SERVICE_RUNTIME_DIR when the daemon runtime should live elsewhere.",
		"  # runtime_dir: /Volumes/fast/gitcode-mcp/runtime",
		"rag:",
		"  # Override with GITCODE_MCP_RAG_MODEL_STORE for gitcode-mcp-managed model storage.",
		"  # model_store_path: /Volumes/models/gitcode-mcp",
		"  default_profile: qwen3-ollama-0_6b-512",
		"  providers:",
		"    ollama:",
		"      endpoint: http://127.0.0.1:11434",
		"      executable: ollama",
		"      startup: managed",
		"      autostart: true",
		"      model_storage:",
		"        mode: provider-owned",
		"        env: OLLAMA_MODELS",
		"  profiles:",
		"    qwen3-ollama-0_6b-512:",
		"      provider: ollama",
		"      model: qwen3-embedding:0.6b",
		"      dimensions: 512",
		"      max_input_tokens: 512",
		"",
	}, "\n")
}
