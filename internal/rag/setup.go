package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"gitcode-mcp/internal/config"
)

type Runtime interface {
	LookPath(string) (string, error)
	IsLive(context.Context, string, time.Duration) (bool, string)
	ListModels(context.Context, string, time.Duration) ([]string, error)
	PullModel(context.Context, string, string, time.Duration) error
	EmbeddingSmoke(context.Context, string, string, time.Duration) error
	Start(context.Context, config.RAGProviderConfig) (string, error)
}

type OSRuntime struct{}

type SetupRequest struct {
	Config  config.Config
	Profile string
	Yes     bool
	DryRun  bool
	Runtime Runtime
}

type SetupResult struct {
	Status              string   `json:"status"`
	Profile             string   `json:"profile"`
	Provider            string   `json:"provider"`
	ProviderType        string   `json:"provider_type"`
	Endpoint            string   `json:"endpoint"`
	Executable          string   `json:"executable,omitempty"`
	ExecutablePath      string   `json:"executable_path,omitempty"`
	Autostart           bool     `json:"autostart"`
	Model               string   `json:"model"`
	ModelAvailable      bool     `json:"model_available"`
	ModelStorePath      string   `json:"model_store_path,omitempty"`
	ProviderModelEnv    string   `json:"provider_model_env,omitempty"`
	ProviderModelPath   string   `json:"provider_model_path,omitempty"`
	ProviderInstalled   bool     `json:"provider_installed"`
	ProviderLive        bool     `json:"provider_live"`
	PullAttempted       bool     `json:"pull_attempted"`
	EmbeddingSmoke      string   `json:"embedding_smoke"`
	Actions             []string `json:"actions,omitempty"`
	Diagnostics         []string `json:"diagnostics,omitempty"`
	InstallInstructions []string `json:"install_instructions,omitempty"`
}

func Setup(ctx context.Context, req SetupRequest) (SetupResult, error) {
	runtime := req.Runtime
	if runtime == nil {
		runtime = OSRuntime{}
	}
	profileName := strings.TrimSpace(req.Profile)
	if profileName == "" {
		profileName = strings.TrimSpace(req.Config.RAG.DefaultProfile)
	}
	if profileName == "" {
		profileName = config.DefaultRAGProfile
	}
	profile, ok := req.Config.RAG.Profiles[profileName]
	if !ok {
		return SetupResult{}, fmt.Errorf("rag setup: profile %q is not configured", profileName)
	}
	providerName := strings.TrimSpace(profile.Provider)
	if providerName == "" {
		return SetupResult{}, fmt.Errorf("rag setup: profile %q has no provider", profileName)
	}
	provider, ok := req.Config.RAG.Providers[providerName]
	if !ok {
		return SetupResult{}, fmt.Errorf("rag setup: provider %q is not configured", providerName)
	}
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	result := SetupResult{
		Status:              "checking",
		Profile:             profileName,
		Provider:            providerName,
		ProviderType:        firstNonEmpty(provider.Type, providerName),
		Endpoint:            provider.Endpoint,
		Executable:          provider.Executable,
		Autostart:           provider.Autostart,
		Model:               profile.Model,
		ModelStorePath:      req.Config.RAG.ModelStorePath,
		ProviderModelEnv:    provider.ModelStorage.Env,
		ProviderModelPath:   providerModelPath(provider),
		EmbeddingSmoke:      "skipped",
		InstallInstructions: append([]string(nil), provider.InstallHints...),
	}
	executable := strings.TrimSpace(provider.Executable)
	if executable != "" {
		path, err := runtime.LookPath(executable)
		if err == nil && strings.TrimSpace(path) != "" {
			result.ProviderInstalled = true
			result.ExecutablePath = path
		} else {
			result.Diagnostics = append(result.Diagnostics, "provider executable not found: "+executable)
			result.Actions = append(result.Actions, "install provider runtime")
			result.Status = "missing_provider"
			return result, nil
		}
	}
	live, liveMessage := runtime.IsLive(ctx, provider.Endpoint, timeout)
	result.ProviderLive = live
	if !live {
		if liveMessage != "" {
			result.Diagnostics = append(result.Diagnostics, liveMessage)
		}
		if provider.Autostart && providerStartupManaged(provider) {
			if req.DryRun {
				result.Actions = append(result.Actions, "start provider runtime")
				result.Status = "provider_not_running"
				return result, nil
			}
			startMessage, err := runtime.Start(ctx, provider)
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, "provider autostart failed: "+err.Error())
				result.Actions = append(result.Actions, "start provider runtime")
				result.Status = "provider_not_running"
				return result, nil
			}
			if startMessage != "" {
				result.Diagnostics = append(result.Diagnostics, startMessage)
			}
			live, liveMessage = runtime.IsLive(ctx, provider.Endpoint, timeout)
			result.ProviderLive = live
		}
	}
	if !result.ProviderLive {
		if liveMessage != "" {
			result.Diagnostics = append(result.Diagnostics, liveMessage)
		}
		result.Actions = append(result.Actions, "start provider runtime")
		result.Status = "provider_not_running"
		return result, nil
	}
	models, err := runtime.ListModels(ctx, provider.Endpoint, timeout)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, "model list failed: "+err.Error())
		result.Status = "provider_error"
		return result, nil
	}
	result.ModelAvailable = containsModel(models, profile.Model)
	if !result.ModelAvailable {
		result.Actions = append(result.Actions, "pull model "+profile.Model)
		if req.DryRun || !req.Yes {
			result.Status = "missing_model"
			return result, nil
		}
		result.PullAttempted = true
		if err := runtime.PullModel(ctx, provider.Endpoint, profile.Model, timeout); err != nil {
			result.Diagnostics = append(result.Diagnostics, "model pull failed: "+err.Error())
			result.Status = "model_pull_failed"
			return result, nil
		}
		models, err = runtime.ListModels(ctx, provider.Endpoint, timeout)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, "model list after pull failed: "+err.Error())
			result.Status = "provider_error"
			return result, nil
		}
		result.ModelAvailable = containsModel(models, profile.Model)
	}
	if !result.ModelAvailable {
		result.Status = "missing_model"
		return result, nil
	}
	if req.DryRun {
		result.Status = "ready"
		return result, nil
	}
	if err := runtime.EmbeddingSmoke(ctx, provider.Endpoint, profile.Model, timeout); err != nil {
		result.EmbeddingSmoke = "failed"
		result.Diagnostics = append(result.Diagnostics, "embedding smoke failed: "+err.Error())
		result.Status = "smoke_failed"
		return result, nil
	}
	result.EmbeddingSmoke = "ok"
	result.Status = "ready"
	return result, nil
}

func (OSRuntime) LookPath(executable string) (string, error) {
	return exec.LookPath(executable)
}

func (OSRuntime) IsLive(ctx context.Context, endpoint string, timeout time.Duration) (bool, string) {
	if strings.TrimSpace(endpoint) == "" {
		return false, "provider endpoint is not configured"
	}
	var payload map[string]any
	err := getJSON(ctx, endpoint+"/api/tags", timeout, &payload)
	if err != nil {
		return false, "provider endpoint is not reachable: " + err.Error()
	}
	return true, ""
}

func (OSRuntime) ListModels(ctx context.Context, endpoint string, timeout time.Duration) ([]string, error) {
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, endpoint+"/api/tags", timeout, &payload); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
		if strings.TrimSpace(model.Name) != "" {
			models = append(models, strings.TrimSpace(model.Name))
		}
	}
	return models, nil
}

func (OSRuntime) PullModel(ctx context.Context, endpoint, model string, timeout time.Duration) error {
	body := map[string]any{"name": model, "stream": false}
	return postJSON(ctx, endpoint+"/api/pull", timeout, body, nil)
}

func (OSRuntime) EmbeddingSmoke(ctx context.Context, endpoint, model string, timeout time.Duration) error {
	body := map[string]any{"model": model, "prompt": "gitcode-mcp readiness"}
	var payload struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := postJSON(ctx, endpoint+"/api/embeddings", timeout, body, &payload); err != nil {
		return err
	}
	if len(payload.Embedding) == 0 {
		return errors.New("empty embedding")
	}
	return nil
}

func (OSRuntime) Start(ctx context.Context, provider config.RAGProviderConfig) (string, error) {
	executable := strings.TrimSpace(provider.Executable)
	if executable == "" {
		return "", errors.New("provider executable is not configured")
	}
	cmd := exec.CommandContext(ctx, executable, "serve")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = os.Environ()
	for key, value := range provider.Env {
		if strings.TrimSpace(key) != "" {
			cmd.Env = append(cmd.Env, strings.TrimSpace(key)+"="+value)
		}
	}
	if strings.TrimSpace(provider.ModelStorage.Env) != "" && strings.TrimSpace(provider.ModelStorage.Path) != "" {
		cmd.Env = append(cmd.Env, strings.TrimSpace(provider.ModelStorage.Env)+"="+strings.TrimSpace(provider.ModelStorage.Path))
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return fmt.Sprintf("started provider process pid=%d", cmd.Process.Pid), nil
}

func getJSON(ctx context.Context, url string, timeout time.Duration, target any) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func postJSON(ctx context.Context, url string, timeout time.Duration, body any, target any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if target == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func containsModel(models []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, model := range models {
		if strings.TrimSpace(model) == want {
			return true
		}
	}
	return false
}

func providerModelPath(provider config.RAGProviderConfig) string {
	if strings.TrimSpace(provider.ModelStorage.Path) != "" {
		return strings.TrimSpace(provider.ModelStorage.Path)
	}
	if provider.ModelStorage.Env == "" {
		return ""
	}
	return provider.Env[provider.ModelStorage.Env]
}

func providerStartupManaged(provider config.RAGProviderConfig) bool {
	startup := strings.TrimSpace(provider.Startup)
	return startup == "" || startup == "managed"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
