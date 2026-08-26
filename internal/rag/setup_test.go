package rag

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
)

type fakeRuntime struct {
	executablePath string
	live           bool
	models         []string
	pullErr        error
	smokeErr       error
	pullCalls      int
	pullTimeout    time.Duration
	modelOnPullErr bool
	smokeCalls     int
	startCalls     int
}

func (r *fakeRuntime) LookPath(string) (string, error) {
	if r.executablePath == "" {
		return "", errors.New("not found")
	}
	return r.executablePath, nil
}

func (r *fakeRuntime) IsLive(context.Context, string, time.Duration) (bool, string) {
	if r.live {
		return true, ""
	}
	return false, "not live"
}

func (r *fakeRuntime) ListModels(context.Context, string, time.Duration) ([]string, error) {
	return append([]string(nil), r.models...), nil
}

func (r *fakeRuntime) PullModel(_ context.Context, _, model string, timeout time.Duration) error {
	r.pullCalls++
	r.pullTimeout = timeout
	if r.pullErr != nil {
		if r.modelOnPullErr {
			r.models = append(r.models, model)
		}
		return r.pullErr
	}
	r.models = append(r.models, model)
	return nil
}

func (r *fakeRuntime) EmbeddingSmoke(context.Context, string, string, time.Duration) error {
	r.smokeCalls++
	return r.smokeErr
}

func (r *fakeRuntime) Start(context.Context, config.RAGProviderConfig) (string, error) {
	r.startCalls++
	r.live = true
	return "started", nil
}

func TestSetupScenarios(t *testing.T) {
	cfg := config.Default()

	t.Run("missing provider is actionable", func(t *testing.T) {
		runtime := &fakeRuntime{}
		result, err := Setup(context.Background(), SetupRequest{Config: cfg, Runtime: runtime})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "missing_provider" || len(result.Actions) == 0 || result.ProviderInstalled {
			t.Fatalf("result=%#v", result)
		}
	})

	t.Run("dry-run plans provider start without mutation", func(t *testing.T) {
		runtime := &fakeRuntime{executablePath: "/usr/local/bin/ollama"}
		result, err := Setup(context.Background(), SetupRequest{Config: cfg, Runtime: runtime, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "provider_not_running" || runtime.startCalls != 0 {
			t.Fatalf("result=%#v runtime=%#v", result, runtime)
		}
	})

	t.Run("missing model requires confirmation", func(t *testing.T) {
		runtime := &fakeRuntime{executablePath: "/usr/local/bin/ollama", live: true}
		result, err := Setup(context.Background(), SetupRequest{Config: cfg, Runtime: runtime})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "missing_model" || runtime.pullCalls != 0 || result.PullAttempted {
			t.Fatalf("result=%#v runtime=%#v", result, runtime)
		}
	})

	t.Run("yes pulls model and runs smoke", func(t *testing.T) {
		runtime := &fakeRuntime{executablePath: "/usr/local/bin/ollama", live: true}
		var progress []SetupProgress
		result, err := Setup(context.Background(), SetupRequest{Config: cfg, Runtime: runtime, Yes: true, Progress: func(event SetupProgress) {
			progress = append(progress, event)
		}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "ready" || !result.PullAttempted || runtime.pullCalls != 1 || runtime.smokeCalls != 1 || !result.ModelAvailable || runtime.pullTimeout < minimumModelPullTimeout {
			t.Fatalf("result=%#v runtime=%#v", result, runtime)
		}
		if len(progress) != 2 || progress[0].Phase != "model_pull_started" || progress[1].Phase != "model_pull_finished" {
			t.Fatalf("progress=%#v", progress)
		}
		if len(result.NextActions) == 0 {
			t.Fatalf("missing next actions: %#v", result)
		}
	})

	t.Run("pull transport failure recovers when model became available", func(t *testing.T) {
		runtime := &fakeRuntime{executablePath: "/usr/local/bin/ollama", live: true, pullErr: context.DeadlineExceeded, modelOnPullErr: true}
		result, err := Setup(context.Background(), SetupRequest{Config: cfg, Runtime: runtime, Yes: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "ready" || !result.ModelAvailable || result.EmbeddingSmoke != "ok" {
			t.Fatalf("result=%#v runtime=%#v", result, runtime)
		}
	})
}
