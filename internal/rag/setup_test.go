package rag

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
)

type fakeRuntime struct {
	executablePath    string
	live              bool
	models            []string
	pullErr           error
	smokeErr          error
	pullCalls         int
	pullTimeout       time.Duration
	modelOnPullErr    bool
	smokeCalls        int
	startCalls        int
	startedExecutable string
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

func (r *fakeRuntime) Start(_ context.Context, provider config.RAGProviderConfig) (string, error) {
	r.startCalls++
	r.startedExecutable = provider.Executable
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

func TestSetupLiveEndpointWithUnavailableExecutable(t *testing.T) {
	cfg := config.Default()
	for _, dryRun := range []bool{true, false} {
		runtime := &fakeRuntime{live: true, models: []string{cfg.RAG.Profiles[config.DefaultRAGProfile].Model}}
		result, err := Setup(context.Background(), SetupRequest{Config: cfg, Runtime: runtime, DryRun: dryRun, Yes: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "provider_executable_unavailable" || result.ProviderInstalled || !result.ProviderLive || !result.ModelAvailable {
			t.Fatalf("readiness must distinguish executable lookup from live endpoint: status=%s installed=%t live=%t model=%t", result.Status, result.ProviderInstalled, result.ProviderLive, result.ModelAvailable)
		}
		if runtime.startCalls != 0 || runtime.pullCalls != 0 || runtime.smokeCalls != 0 {
			t.Fatal("unresolved executable must not trigger setup mutations")
		}
	}
}

func TestProviderExecutableDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, goos, executable, available, want string
		lookupErr                               error
		calls                                   []string
	}{
		{name: "restricted PATH Apple Silicon Homebrew", goos: "darwin", executable: "ollama", available: "/opt/homebrew/bin/ollama", want: "/opt/homebrew/bin/ollama", calls: []string{"ollama", "/opt/homebrew/bin/ollama"}},
		{name: "restricted PATH Intel Homebrew", goos: "darwin", executable: "ollama", available: "/usr/local/bin/ollama", want: "/usr/local/bin/ollama", calls: []string{"ollama", "/opt/homebrew/bin/ollama", "/usr/local/bin/ollama"}},
		{name: "PATH has precedence", goos: "darwin", executable: "ollama", available: "ollama", want: "/configured/bin/ollama", calls: []string{"ollama"}},
		{name: "explicit absolute path", goos: "darwin", executable: "/configured/bin/ollama", available: "/configured/bin/ollama", want: "/configured/bin/ollama", calls: []string{"/configured/bin/ollama"}},
		{name: "missing explicit path does not fall back", goos: "darwin", executable: "/configured/bin/ollama", available: "/opt/homebrew/bin/ollama", calls: []string{"/configured/bin/ollama"}},
		{name: "custom name does not fall back", goos: "darwin", executable: "custom-provider", available: "/opt/homebrew/bin/ollama", calls: []string{"custom-provider"}},
		{name: "relative path does not fall back", goos: "darwin", executable: "bin/ollama", available: "/opt/homebrew/bin/ollama", calls: []string{"bin/ollama"}},
		{name: "other OS does not fall back", goos: "linux", executable: "ollama", available: "/opt/homebrew/bin/ollama", calls: []string{"ollama"}},
		{name: "ErrDot does not fall back", goos: "darwin", executable: "ollama", lookupErr: exec.ErrDot, available: "/opt/homebrew/bin/ollama", calls: []string{"ollama"}},
		{name: "missing everywhere", goos: "darwin", executable: "ollama", calls: []string{"ollama", "/opt/homebrew/bin/ollama", "/usr/local/bin/ollama"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			lookup := func(executable string) (string, error) {
				calls = append(calls, executable)
				if executable == tc.available {
					if executable == "ollama" {
						return tc.want, nil
					}
					return executable, nil
				}
				if tc.lookupErr != nil {
					return "", tc.lookupErr
				}
				return "", exec.ErrNotFound
			}
			path, err := resolveProviderExecutable(tc.executable, tc.goos, lookup)
			if path != tc.want || (err != nil) != (tc.want == "") || !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("unexpected bounded executable discovery result: path=%q err=%v calls=%v", path, err, calls)
			}
		})
	}
}

func TestSetupManagedStartUsesResolvedExecutable(t *testing.T) {
	cfg := config.Default()
	runtime := &fakeRuntime{executablePath: "/opt/homebrew/bin/ollama", models: []string{cfg.RAG.Profiles[config.DefaultRAGProfile].Model}}
	result, err := Setup(context.Background(), SetupRequest{Config: cfg, Runtime: runtime, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || runtime.startCalls != 1 || runtime.startedExecutable != runtime.executablePath {
		t.Fatal("managed start did not use the resolved executable")
	}
	if cfg.RAG.Providers["ollama"].Executable != "ollama" {
		t.Fatal("setup mutated executable configuration")
	}
}
