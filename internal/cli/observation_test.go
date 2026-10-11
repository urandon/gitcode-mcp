package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

type observationCLISource struct {
	*repoInitLocalSource
	reads int
}

func (s *observationCLISource) ReadFile(path string) ([]byte, error) {
	s.reads++
	return s.repoInitLocalSource.ReadFile(path)
}

func TestObservationCLIRejectsBeforeConfigOrBootstrap(t *testing.T) {
	root := t.TempDir()
	src := &observationCLISource{repoInitLocalSource: &repoInitLocalSource{env: map[string]string{}, cwd: root, homeDir: root, configDir: root, cacheDir: root}}
	for _, args := range [][]string{
		{"service", "observe", "--live"}, {"service", "observe", "synthetic-private-coordinate"}, {"service", "observe", "--repo", "synthetic-private-coordinate"},
		{"service", "observe", "--cache-path", "synthetic-private-path"}, {"service", "observe", "--format", "json", "--format", "json"},
		{"service", "metrics", "--history-minutes", "0"}, {"service", "metrics", "--history-minutes", "61"}, {"service", "metrics", "--name", "synthetic-private-name"},
		{"service", "events", "--limit", "0"}, {"service", "events", "--limit", "201"}, {"service", "events", "--limit", "50", "--limit", "20"},
		{"service", "events", "--source-ref", "synthetic-private-path"}, {"service", "events", "--outcome", "succeeded", "--failure-class", "timeout"},
		{"service", "events", "--since", "2026-01-01T00:00:00Z"},
	} {
		var out, errOut bytes.Buffer
		code := executeWithFactoryAndDepsContext(context.Background(), args, &out, &errOut, func(context.Context, string) (queryService, func() error, error) {
			t.Fatal("observation initialized a cache/provider service")
			return nil, nil, nil
		}, localCommandDeps{Source: src})
		if code == 0 || out.Len() != 0 || strings.Contains(errOut.String(), "synthetic-private") {
			t.Fatal("invalid observation CLI accepted or leaked input")
		}
	}
	if src.reads != 0 {
		t.Fatal("invalid observation CLI read configuration")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid observation CLI created local state")
	}
}

func TestObservationCLIMissingDaemonAndHelpDoNotCreateCache(t *testing.T) {
	root := t.TempDir()
	src := &observationCLISource{repoInitLocalSource: &repoInitLocalSource{env: map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "synthetic-private-absent"}, cwd: root, homeDir: filepath.Join(root, "h"), configDir: filepath.Join(root, "f"), cacheDir: filepath.Join(root, "c")}}
	for _, sub := range []string{"observe", "metrics", "events"} {
		var out, errOut bytes.Buffer
		code := ExecuteWithSource([]string{"service", sub, "--format", "json"}, &out, &errOut, src)
		if code == 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "service_unavailable") || strings.Contains(errOut.String(), "synthetic-private") {
			t.Fatal("missing daemon error missing or unsafe")
		}
		out.Reset()
		errOut.Reset()
		before := src.reads
		code = ExecuteWithSource([]string{"service", sub, "--help"}, &out, &errOut, src)
		if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "Read-only") || src.reads != before {
			t.Fatal("observation help performed bootstrap")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("observation read created cache, runtime, or service definition")
	}
}

func TestObservationCLIActualIPCParityWithoutCacheFactory(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := observability.New(observability.Config{Directory: t.TempDir(), ManagedOutput: true})
	if !c.Close(context.Background(), true) {
		t.Fatal("collector fixture did not close")
	}
	m := observability.NewMetrics(c.BootID(), at)
	if !m.SampleAt(at, map[string]observability.ProbeValue{"runtime_heap": observability.Available(42)}) {
		t.Fatal("sample fixture unavailable")
	}
	r := &observability.Reader{Collector: c, Metrics: m, Now: func() time.Time { return at }}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("IPC fixture unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (servicectl.RPCServer{Observability: r}).Serve(ctx, listener) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("CLI IPC fixture did not join")
		}
	}()
	root := t.TempDir()
	src := &observationCLISource{repoInitLocalSource: &repoInitLocalSource{env: map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "tcp", "GITCODE_MCP_SERVICE_ADDRESS": listener.Addr().String()}, cwd: root, homeDir: root, configDir: root, cacheDir: root}}
	wantSummary, err := r.Summary(ctx)
	if err != nil {
		t.Fatal("summary fixture unavailable")
	}
	wantMetrics, err := r.MetricSnapshot(ctx, observability.MetricsRequest{Name: "runtime_heap", HistoryMinutes: 3})
	if err != nil {
		t.Fatal("metrics fixture unavailable")
	}
	wantEvents, err := r.Events(ctx, observability.EventsRequest{Limit: 50})
	if err != nil {
		t.Fatal("events fixture unavailable")
	}
	for _, test := range []struct {
		args []string
		want any
	}{
		{[]string{"service", "observe", "--format", "json"}, wantSummary},
		{[]string{"service", "metrics", "--name", "runtime_heap", "--history-minutes", "3", "--format", "json"}, wantMetrics},
		{[]string{"service", "events", "--format", "json"}, wantEvents},
	} {
		var out, errOut bytes.Buffer
		code := executeWithFactoryAndDepsContext(ctx, test.args, &out, &errOut, func(context.Context, string) (queryService, func() error, error) {
			t.Fatal("CLI observation opened cache/provider")
			return nil, nil, nil
		}, localCommandDeps{Source: src})
		want, _ := json.Marshal(test.want)
		var compact bytes.Buffer
		if code != 0 || errOut.Len() != 0 || json.Compact(&compact, out.Bytes()) != nil || compact.String() != string(want) {
			t.Fatal("CLI observation diverged from shared reader")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("CLI observation created local state")
	}
}
