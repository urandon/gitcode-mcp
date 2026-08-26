package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultRAGProfile           = "qwen3-ollama-0_6b-1024"
	DefaultJobSuccessTTL        = 48 * time.Hour
	DefaultJobDiagnosticTTL     = 14 * 24 * time.Hour
	DefaultJobMaxTerminal       = 128
	DefaultJobMaxDiagnostic     = 32
	DefaultJobMaxProgressEvents = 256
)

const (
	EnvRAGProfile              = "GITCODE_MCP_RAG_PROFILE"
	EnvServiceRuntimeDir       = "GITCODE_MCP_SERVICE_RUNTIME_DIR"
	EnvRAGProviderEndpoint     = "GITCODE_MCP_RAG_PROVIDER_ENDPOINT"
	EnvRAGModelStore           = "GITCODE_MCP_RAG_MODEL_STORE"
	defaultRAGProvider         = "ollama"
	defaultRAGEmbeddingModel   = "qwen3-embedding:0.6b"
	defaultRAGProviderEndpoint = "http://127.0.0.1:11434"
)

type ServiceConfig struct {
	RuntimeDir   string                    `json:"runtime_dir,omitempty"`
	JobRetention ServiceJobRetentionConfig `json:"job_retention"`
}

type ServiceJobRetentionConfig struct {
	SuccessTTL        time.Duration `json:"success_ttl"`
	DiagnosticTTL     time.Duration `json:"diagnostic_ttl"`
	MaxTerminalJobs   int           `json:"max_terminal_jobs"`
	MaxDiagnosticJobs int           `json:"max_diagnostic_jobs"`
	MaxProgressEvents int           `json:"max_progress_events"`
}

type RAGConfig struct {
	ModelStorePath string                       `json:"model_store_path,omitempty"`
	DefaultProfile string                       `json:"default_profile"`
	Providers      map[string]RAGProviderConfig `json:"providers,omitempty"`
	Profiles       map[string]RAGProfileConfig  `json:"profiles,omitempty"`
	Indexing       RAGIndexingConfig            `json:"indexing"`
	Search         RAGSearchConfig              `json:"search"`
}

type RAGProviderConfig struct {
	Type         string                `json:"type,omitempty"`
	DataBoundary string                `json:"data_boundary,omitempty"`
	Endpoint     string                `json:"endpoint,omitempty"`
	Executable   string                `json:"executable,omitempty"`
	Startup      string                `json:"startup,omitempty"`
	Autostart    bool                  `json:"autostart"`
	Env          map[string]string     `json:"env,omitempty"`
	InstallHints []string              `json:"install_hints,omitempty"`
	Timeout      time.Duration         `json:"timeout,omitempty"`
	ModelStorage RAGModelStorageConfig `json:"model_storage,omitempty"`
}

type RAGModelStorageConfig struct {
	Mode string `json:"mode,omitempty"`
	Path string `json:"path,omitempty"`
	Env  string `json:"env,omitempty"`
}

type RAGProfileConfig struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Dimensions     int    `json:"dimensions,omitempty"`
	MaxInputTokens int    `json:"max_input_tokens,omitempty"`
	BatchSize      int    `json:"batch_size,omitempty"`
}

type RAGIndexingConfig struct {
	Profile     string `json:"profile,omitempty"`
	ChunkTokens int    `json:"chunk_tokens,omitempty"`
	Overlap     int    `json:"overlap,omitempty"`
	BatchSize   int    `json:"batch_size,omitempty"`
}

type RAGSearchConfig struct {
	Profile string `json:"profile,omitempty"`
	TopK    int    `json:"top_k,omitempty"`
	Hybrid  bool   `json:"hybrid"`
}

type serviceFileConfig struct {
	RuntimeDir   *string                        `json:"runtime_dir"`
	JobRetention *serviceJobRetentionFileConfig `json:"job_retention"`
}

type serviceJobRetentionFileConfig struct {
	SuccessTTL        *string `json:"success_ttl"`
	DiagnosticTTL     *string `json:"diagnostic_ttl"`
	MaxTerminalJobs   *int    `json:"max_terminal_jobs"`
	MaxDiagnosticJobs *int    `json:"max_diagnostic_jobs"`
	MaxProgressEvents *int    `json:"max_progress_events"`
}

type ragFileConfig struct {
	ModelStorePath *string                          `json:"model_store_path"`
	DefaultProfile *string                          `json:"default_profile"`
	Providers      map[string]ragProviderFileConfig `json:"providers"`
	Profiles       map[string]ragProfileFileConfig  `json:"profiles"`
	Indexing       *ragIndexingFileConfig           `json:"indexing"`
	Search         *ragSearchFileConfig             `json:"search"`
}

type ragProviderFileConfig struct {
	Type         *string                    `json:"type"`
	DataBoundary *string                    `json:"data_boundary"`
	Endpoint     *string                    `json:"endpoint"`
	Executable   *string                    `json:"executable"`
	Startup      *string                    `json:"startup"`
	Autostart    *bool                      `json:"autostart"`
	Env          map[string]string          `json:"env"`
	InstallHints []string                   `json:"install_hints"`
	Timeout      *string                    `json:"timeout"`
	ModelStorage *ragModelStorageFileConfig `json:"model_storage"`
}

type ragModelStorageFileConfig struct {
	Mode *string `json:"mode"`
	Path *string `json:"path"`
	Env  *string `json:"env"`
}

type ragProfileFileConfig struct {
	Provider       *string `json:"provider"`
	Model          *string `json:"model"`
	Dimensions     *int    `json:"dimensions"`
	MaxInputTokens *int    `json:"max_input_tokens"`
	BatchSize      *int    `json:"batch_size"`
}

type ragIndexingFileConfig struct {
	Profile     *string `json:"profile"`
	ChunkTokens *int    `json:"chunk_tokens"`
	Overlap     *int    `json:"overlap"`
	BatchSize   *int    `json:"batch_size"`
}

type ragSearchFileConfig struct {
	Profile *string `json:"profile"`
	TopK    *int    `json:"top_k"`
	Hybrid  *bool   `json:"hybrid"`
}

func defaultServiceConfig(cacheBaseDir string) ServiceConfig {
	return ServiceConfig{
		RuntimeDir: filepath.Join(cacheBaseDir, "runtime"),
		JobRetention: ServiceJobRetentionConfig{
			SuccessTTL: DefaultJobSuccessTTL, DiagnosticTTL: DefaultJobDiagnosticTTL,
			MaxTerminalJobs: DefaultJobMaxTerminal, MaxDiagnosticJobs: DefaultJobMaxDiagnostic,
			MaxProgressEvents: DefaultJobMaxProgressEvents,
		},
	}
}

func defaultRAGConfig(cacheBaseDir string) RAGConfig {
	return RAGConfig{
		ModelStorePath: filepath.Join(cacheBaseDir, "models"),
		DefaultProfile: DefaultRAGProfile,
		Providers: map[string]RAGProviderConfig{
			defaultRAGProvider: {
				Type:         defaultRAGProvider,
				DataBoundary: "local_network",
				Endpoint:     defaultRAGProviderEndpoint,
				Executable:   "ollama",
				Startup:      "managed",
				Autostart:    true,
				Env:          map[string]string{},
				InstallHints: []string{
					"Install Ollama from https://ollama.com/download.",
					"Set OLLAMA_MODELS or rag.providers.ollama.env.OLLAMA_MODELS to place provider-owned models on another disk.",
				},
				Timeout: 30 * time.Second,
				ModelStorage: RAGModelStorageConfig{
					Mode: "provider-owned",
					Env:  "OLLAMA_MODELS",
				},
			},
		},
		Profiles: map[string]RAGProfileConfig{
			DefaultRAGProfile: {
				Provider:       defaultRAGProvider,
				Model:          defaultRAGEmbeddingModel,
				Dimensions:     1024,
				MaxInputTokens: 512,
				BatchSize:      16,
			},
		},
		Indexing: RAGIndexingConfig{
			Profile:     DefaultRAGProfile,
			ChunkTokens: 512,
			Overlap:     64,
			BatchSize:   16,
		},
		Search: RAGSearchConfig{
			Profile: DefaultRAGProfile,
			TopK:    8,
			Hybrid:  true,
		},
	}
}

func mergeServiceFile(cfg Config, file *serviceFileConfig) (Config, error) {
	if file == nil {
		return cfg, nil
	}
	if file.RuntimeDir != nil {
		cfg.Service.RuntimeDir = strings.TrimSpace(*file.RuntimeDir)
	}
	if file.JobRetention == nil {
		return cfg, nil
	}
	retention := cfg.Service.JobRetention
	var err error
	if file.JobRetention.SuccessTTL != nil {
		retention.SuccessTTL, err = time.ParseDuration(strings.TrimSpace(*file.JobRetention.SuccessTTL))
		if err != nil {
			return Config{}, fmt.Errorf("config: invalid service.job_retention.success_ttl %q: %w", *file.JobRetention.SuccessTTL, err)
		}
	}
	if file.JobRetention.DiagnosticTTL != nil {
		retention.DiagnosticTTL, err = time.ParseDuration(strings.TrimSpace(*file.JobRetention.DiagnosticTTL))
		if err != nil {
			return Config{}, fmt.Errorf("config: invalid service.job_retention.diagnostic_ttl %q: %w", *file.JobRetention.DiagnosticTTL, err)
		}
	}
	if file.JobRetention.MaxTerminalJobs != nil {
		retention.MaxTerminalJobs = *file.JobRetention.MaxTerminalJobs
	}
	if file.JobRetention.MaxDiagnosticJobs != nil {
		retention.MaxDiagnosticJobs = *file.JobRetention.MaxDiagnosticJobs
	}
	if file.JobRetention.MaxProgressEvents != nil {
		retention.MaxProgressEvents = *file.JobRetention.MaxProgressEvents
	}
	if err := ValidateServiceJobRetention(retention); err != nil {
		return Config{}, err
	}
	cfg.Service.JobRetention = retention
	return cfg, nil
}

func ValidateServiceJobRetention(retention ServiceJobRetentionConfig) error {
	if retention.SuccessTTL < time.Minute || retention.SuccessTTL > 90*24*time.Hour {
		return fmt.Errorf("config: service.job_retention.success_ttl must be between 1m and 2160h")
	}
	if retention.DiagnosticTTL < retention.SuccessTTL || retention.DiagnosticTTL > 365*24*time.Hour {
		return fmt.Errorf("config: service.job_retention.diagnostic_ttl must be at least success_ttl and no more than 8760h")
	}
	if retention.MaxTerminalJobs < 1 || retention.MaxTerminalJobs > 4096 {
		return fmt.Errorf("config: service.job_retention.max_terminal_jobs must be between 1 and 4096")
	}
	if retention.MaxDiagnosticJobs < 1 || retention.MaxDiagnosticJobs > retention.MaxTerminalJobs {
		return fmt.Errorf("config: service.job_retention.max_diagnostic_jobs must be between 1 and max_terminal_jobs")
	}
	if retention.MaxProgressEvents < 1 || retention.MaxProgressEvents > 4096 {
		return fmt.Errorf("config: service.job_retention.max_progress_events must be between 1 and 4096")
	}
	return nil
}

func mergeRAGFile(cfg Config, file *ragFileConfig) (Config, error) {
	if file == nil {
		return cfg, nil
	}
	if file.ModelStorePath != nil {
		cfg.RAG.ModelStorePath = strings.TrimSpace(*file.ModelStorePath)
	}
	if file.DefaultProfile != nil {
		setDefaultRAGProfile(&cfg, strings.TrimSpace(*file.DefaultProfile))
	}
	if len(file.Providers) > 0 {
		if cfg.RAG.Providers == nil {
			cfg.RAG.Providers = map[string]RAGProviderConfig{}
		}
		for name, providerFile := range file.Providers {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			provider := cfg.RAG.Providers[name]
			merged, err := mergeRAGProviderFile(provider, providerFile)
			if err != nil {
				return Config{}, err
			}
			cfg.RAG.Providers[name] = merged
		}
	}
	if len(file.Profiles) > 0 {
		if cfg.RAG.Profiles == nil {
			cfg.RAG.Profiles = map[string]RAGProfileConfig{}
		}
		for name, profileFile := range file.Profiles {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			cfg.RAG.Profiles[name] = mergeRAGProfileFile(cfg.RAG.Profiles[name], profileFile)
		}
	}
	if file.Indexing != nil {
		cfg.RAG.Indexing = mergeRAGIndexingFile(cfg.RAG.Indexing, *file.Indexing)
	}
	if file.Search != nil {
		cfg.RAG.Search = mergeRAGSearchFile(cfg.RAG.Search, *file.Search)
	}
	return cfg, nil
}

func mergeRAGProviderFile(provider RAGProviderConfig, file ragProviderFileConfig) (RAGProviderConfig, error) {
	if file.Type != nil {
		provider.Type = strings.TrimSpace(*file.Type)
	}
	if file.DataBoundary != nil {
		boundary := strings.TrimSpace(*file.DataBoundary)
		switch boundary {
		case "local_process", "local_network", "remote", "unknown":
			provider.DataBoundary = boundary
		default:
			return RAGProviderConfig{}, fmt.Errorf("rag provider data_boundary must be local_process, local_network, remote, or unknown")
		}
	}
	if file.Endpoint != nil {
		provider.Endpoint = strings.TrimSpace(*file.Endpoint)
		if file.DataBoundary == nil {
			provider.DataBoundary = "unknown"
		}
	}
	if file.Executable != nil {
		provider.Executable = strings.TrimSpace(*file.Executable)
	}
	if file.Startup != nil {
		provider.Startup = strings.TrimSpace(*file.Startup)
	}
	if file.Autostart != nil {
		provider.Autostart = *file.Autostart
	}
	if file.Env != nil {
		if provider.Env == nil {
			provider.Env = map[string]string{}
		}
		for key, value := range file.Env {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			provider.Env[key] = strings.TrimSpace(value)
		}
	}
	if file.InstallHints != nil {
		provider.InstallHints = append([]string(nil), file.InstallHints...)
	}
	if file.Timeout != nil {
		timeout, err := time.ParseDuration(strings.TrimSpace(*file.Timeout))
		if err != nil {
			return RAGProviderConfig{}, fmt.Errorf("config: invalid rag provider timeout %q: %w", *file.Timeout, err)
		}
		provider.Timeout = timeout
	}
	if file.ModelStorage != nil {
		if file.ModelStorage.Mode != nil {
			provider.ModelStorage.Mode = strings.TrimSpace(*file.ModelStorage.Mode)
		}
		if file.ModelStorage.Path != nil {
			provider.ModelStorage.Path = strings.TrimSpace(*file.ModelStorage.Path)
		}
		if file.ModelStorage.Env != nil {
			provider.ModelStorage.Env = strings.TrimSpace(*file.ModelStorage.Env)
		}
	}
	return provider, nil
}

func mergeRAGProfileFile(profile RAGProfileConfig, file ragProfileFileConfig) RAGProfileConfig {
	if file.Provider != nil {
		profile.Provider = strings.TrimSpace(*file.Provider)
	}
	if file.Model != nil {
		profile.Model = strings.TrimSpace(*file.Model)
	}
	if file.Dimensions != nil {
		profile.Dimensions = *file.Dimensions
	}
	if file.MaxInputTokens != nil {
		profile.MaxInputTokens = *file.MaxInputTokens
	}
	if file.BatchSize != nil {
		profile.BatchSize = *file.BatchSize
	}
	return profile
}

func mergeRAGIndexingFile(indexing RAGIndexingConfig, file ragIndexingFileConfig) RAGIndexingConfig {
	if file.Profile != nil {
		indexing.Profile = strings.TrimSpace(*file.Profile)
	}
	if file.ChunkTokens != nil {
		indexing.ChunkTokens = *file.ChunkTokens
	}
	if file.Overlap != nil {
		indexing.Overlap = *file.Overlap
	}
	if file.BatchSize != nil {
		indexing.BatchSize = *file.BatchSize
	}
	return indexing
}

func mergeRAGSearchFile(search RAGSearchConfig, file ragSearchFileConfig) RAGSearchConfig {
	if file.Profile != nil {
		search.Profile = strings.TrimSpace(*file.Profile)
	}
	if file.TopK != nil {
		search.TopK = *file.TopK
	}
	if file.Hybrid != nil {
		search.Hybrid = *file.Hybrid
	}
	return search
}

func setDefaultRAGProfile(cfg *Config, profile string) {
	if profile == "" {
		return
	}
	cfg.RAG.DefaultProfile = profile
	cfg.RAG.Indexing.Profile = profile
	cfg.RAG.Search.Profile = profile
}

func activeRAGProviderName(cfg Config) string {
	profileName := strings.TrimSpace(cfg.RAG.DefaultProfile)
	if profileName != "" {
		if profile, ok := cfg.RAG.Profiles[profileName]; ok && strings.TrimSpace(profile.Provider) != "" {
			return strings.TrimSpace(profile.Provider)
		}
	}
	return defaultRAGProvider
}
