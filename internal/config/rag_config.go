package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultRAGProfile = "qwen3-ollama-0_6b-512"
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
	RuntimeDir string `json:"runtime_dir,omitempty"`
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
	RuntimeDir *string `json:"runtime_dir"`
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
	return ServiceConfig{RuntimeDir: filepath.Join(cacheBaseDir, "runtime")}
}

func defaultRAGConfig(cacheBaseDir string) RAGConfig {
	return RAGConfig{
		ModelStorePath: filepath.Join(cacheBaseDir, "models"),
		DefaultProfile: DefaultRAGProfile,
		Providers: map[string]RAGProviderConfig{
			defaultRAGProvider: {
				Type:       defaultRAGProvider,
				Endpoint:   defaultRAGProviderEndpoint,
				Executable: "ollama",
				Startup:    "managed",
				Autostart:  true,
				Env:        map[string]string{},
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
				Dimensions:     512,
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

func mergeServiceFile(cfg Config, file *serviceFileConfig) Config {
	if file == nil {
		return cfg
	}
	if file.RuntimeDir != nil {
		cfg.Service.RuntimeDir = strings.TrimSpace(*file.RuntimeDir)
	}
	return cfg
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
	if file.Endpoint != nil {
		provider.Endpoint = strings.TrimSpace(*file.Endpoint)
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
