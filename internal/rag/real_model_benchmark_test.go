package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

const defaultBenchmarkCorpusChunks = 5758376

type realModelBenchmarkReport struct {
	SchemaVersion    int                      `json:"schema_version"`
	CapturedAt       time.Time                `json:"captured_at"`
	Machine          benchmarkMachine         `json:"machine"`
	Provider         benchmarkProviderInfo    `json:"provider"`
	Profile          benchmarkProfile         `json:"profile"`
	CorpusChunks     int                      `json:"corpus_chunks"`
	ChunksPerCase    int                      `json:"chunks_per_case"`
	WarmupDurationMS float64                  `json:"warmup_duration_ms"`
	Cases            []realModelBenchmarkCase `json:"cases"`
	Failures         int                      `json:"failures"`
}

type benchmarkMachine struct {
	Label       string `json:"label,omitempty"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	CPU         string `json:"cpu,omitempty"`
	LogicalCPUs int    `json:"logical_cpus"`
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
}

type benchmarkProviderInfo struct {
	Type            string `json:"type"`
	Endpoint        string `json:"endpoint"`
	Version         string `json:"version,omitempty"`
	Model           string `json:"model"`
	ModelRevision   string `json:"model_revision"`
	LoadedBytes     int64  `json:"loaded_bytes,omitempty"`
	LoadedVRAMBytes int64  `json:"loaded_vram_bytes,omitempty"`
}

type benchmarkProfile struct {
	Dimensions int    `json:"dimensions"`
	DType      string `json:"dtype"`
	Normalize  string `json:"normalization"`
}

type realModelBenchmarkCase struct {
	InputBytes                int     `json:"input_bytes"`
	BatchSize                 int     `json:"batch_size"`
	Chunks                    int     `json:"chunks"`
	ProviderCalls             int     `json:"provider_calls"`
	ProviderFailures          int     `json:"provider_failures"`
	BatchLatencyP50MS         float64 `json:"batch_latency_p50_ms"`
	BatchLatencyP95MS         float64 `json:"batch_latency_p95_ms"`
	ElapsedSeconds            float64 `json:"elapsed_seconds"`
	ChunksPerSecond           float64 `json:"chunks_per_second"`
	FirstHalfChunksPerSecond  float64 `json:"first_half_chunks_per_second"`
	SecondHalfChunksPerSecond float64 `json:"second_half_chunks_per_second"`
	SustainedRatio            float64 `json:"sustained_ratio"`
	SustainedHalvesMeasured   bool    `json:"sustained_halves_measured"`
	FullCorpusETAHours        float64 `json:"full_corpus_eta_hours"`
	ProgressEvents            int     `json:"progress_events"`
	SQLiteGrowthBytes         int64   `json:"sqlite_growth_bytes"`
	GoHeapPeakBytes           uint64  `json:"go_heap_peak_bytes"`
	EmbeddedChunks            int     `json:"embedded_chunks"`
	FailedChunks              int     `json:"failed_chunks"`
}

type timedEmbeddingProvider struct {
	EmbeddingProvider
	mu        sync.Mutex
	latencies []time.Duration
	failures  int
}

func (p *timedEmbeddingProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	started := time.Now()
	response, err := p.EmbeddingProvider.Embed(ctx, req)
	p.mu.Lock()
	p.latencies = append(p.latencies, time.Since(started))
	if err != nil {
		p.failures++
	}
	p.mu.Unlock()
	return response, err
}

func (p *timedEmbeddingProvider) snapshot() ([]time.Duration, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Duration(nil), p.latencies...), p.failures
}

func TestRealModelBenchmarkUtilities(t *testing.T) {
	for _, size := range []int{1024, 4096} {
		first := uniqueBenchmarkInput(size, 1, 8)
		second := uniqueBenchmarkInput(size, 2, 8)
		if len(first) != size || len(second) != size || first == second {
			t.Fatalf("size=%d first_len=%d second_len=%d equal=%t", size, len(first), len(second), first == second)
		}
	}
	latencies := []time.Duration{40 * time.Millisecond, 10 * time.Millisecond, 30 * time.Millisecond, 20 * time.Millisecond}
	if got := percentileMilliseconds(latencies, 0.50); got != 20 {
		t.Fatalf("p50=%v, want 20", got)
	}
	if got := percentileMilliseconds(latencies, 0.95); got != 40 {
		t.Fatalf("p95=%v, want 40", got)
	}
}

func TestOptionalOllamaRealModelBenchmark(t *testing.T) {
	if os.Getenv("GITCODE_MCP_RAG_REAL_BENCHMARK") != "1" {
		t.Skip("set GITCODE_MCP_RAG_REAL_BENCHMARK=1 to run the local Ollama end-to-end benchmark")
	}
	endpoint := firstNonEmpty(os.Getenv("GITCODE_MCP_RAG_PROVIDER_ENDPOINT"), "http://127.0.0.1:11434")
	model := firstNonEmpty(os.Getenv("GITCODE_MCP_RAG_REAL_MODEL"), "qwen3-embedding:0.6b")
	dimensions := benchmarkEnvInt(t, "GITCODE_MCP_RAG_REAL_DIMENSIONS", 1024, 1)
	chunksPerCase := benchmarkEnvInt(t, "GITCODE_MCP_RAG_BENCHMARK_CHUNKS_PER_CASE", 256, 32)
	corpusChunks := benchmarkEnvInt(t, "GITCODE_MCP_RAG_BENCHMARK_CORPUS_CHUNKS", defaultBenchmarkCorpusChunks, 1)
	if chunksPerCase%32 != 0 {
		t.Fatalf("GITCODE_MCP_RAG_BENCHMARK_CHUNKS_PER_CASE=%d must be divisible by 32", chunksPerCase)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	warmup, err := newBenchmarkOllamaProvider(endpoint, model, dimensions, 32)
	if err != nil {
		t.Fatal(err)
	}
	info, err := warmup.ModelInfo(ctx)
	if err != nil {
		skipIfProviderUnavailable(t, err)
		t.Fatalf("ModelInfo returned error: %v", err)
	}
	warmupStarted := time.Now()
	if _, err := warmup.Embed(ctx, EmbedRequest{Inputs: []string{"gitcode-mcp benchmark warmup"}}); err != nil {
		skipIfProviderUnavailable(t, err)
		t.Fatalf("warmup embedding failed: %v", err)
	}
	warmupDuration := time.Since(warmupStarted)

	report := realModelBenchmarkReport{
		SchemaVersion:    1,
		CapturedAt:       time.Now().UTC(),
		Machine:          readBenchmarkMachine(),
		Provider:         readBenchmarkProviderInfo(ctx, warmup, endpoint, model, info.Revision),
		Profile:          benchmarkProfile{Dimensions: dimensions, DType: DefaultEmbeddingDType, Normalize: DefaultEmbeddingNormalization},
		CorpusChunks:     corpusChunks,
		ChunksPerCase:    chunksPerCase,
		WarmupDurationMS: milliseconds(warmupDuration),
	}
	for _, inputBytes := range []int{1024, 4096} {
		for _, batchSize := range []int{1, 8, 16, 32} {
			result := runRealModelBenchmarkCase(t, ctx, endpoint, model, dimensions, inputBytes, batchSize, chunksPerCase, corpusChunks)
			report.Cases = append(report.Cases, result)
			report.Failures += result.ProviderFailures + result.FailedChunks
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if output := strings.TrimSpace(os.Getenv("GITCODE_MCP_RAG_BENCHMARK_OUTPUT")); output != "" {
		if err := os.WriteFile(output, data, 0o600); err != nil {
			t.Fatalf("write benchmark output: %v", err)
		}
	}
	t.Logf("public-safe benchmark report:\n%s", data)
	if report.Failures != 0 {
		t.Fatalf("benchmark completed with %d failures", report.Failures)
	}
}

func runRealModelBenchmarkCase(t *testing.T, ctx context.Context, endpoint, model string, dimensions, inputBytes, batchSize, chunks, corpusChunks int) realModelBenchmarkCase {
	t.Helper()
	caseDir := t.TempDir()
	dbPath := filepath.Join(caseDir, "benchmark.db")
	store, err := cache.NewSQLiteStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("create benchmark store: %v", err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "benchmark", Owner: "public", Name: "benchmark", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("add benchmark repository: %v", err)
	}
	chunkRows := make([]cache.Chunk, 0, chunks)
	for index := 0; index < chunks; index++ {
		text := uniqueBenchmarkInput(inputBytes, index, batchSize)
		hash := sha256.Sum256([]byte(text))
		chunkRows = append(chunkRows, cache.Chunk{
			RepoID: "benchmark", ID: fmt.Sprintf("chunk-%04d", index), SourceID: "BENCH-1", RecordID: "BENCH-1",
			ContentHash: hex.EncodeToString(hash[:]), ByteStart: index * inputBytes, ByteEnd: (index + 1) * inputBytes,
			LineStart: index + 1, LineEnd: index + 1, Text: text, NormalizedText: text, Policy: DefaultChunkPolicyID,
		})
	}
	mustUpsertVectorChunks(t, ctx, store, "benchmark", chunkRows)
	beforeBytes := sqliteFilesSize(dbPath)

	provider, err := newBenchmarkOllamaProvider(endpoint, model, dimensions, batchSize)
	if err != nil {
		t.Fatal(err)
	}
	timed := &timedEmbeddingProvider{EmbeddingProvider: provider}
	progressCh := make(chan service.ProgressEvent, chunks+16)
	progressDone := make(chan struct{})
	var progressEvents int
	var halfwayAt time.Time
	started := time.Now()
	go func() {
		defer close(progressDone)
		for event := range progressCh {
			progressEvents++
			if halfwayAt.IsZero() && event.RecordsFetched >= chunks/2 {
				halfwayAt = time.Now()
			}
		}
	}()
	stopMemory := startHeapSampler()
	result, runErr := NewRAGIndexer(store, timed, RAGIndexerOptions{}).Run(ctx, IndexRequest{
		RepoID: "benchmark", ProfileID: fmt.Sprintf("benchmark-%d-%d", inputBytes, batchSize),
		ChunkPolicyID: DefaultChunkPolicyID, BatchSize: batchSize, ProgressChan: progressCh,
	})
	elapsed := time.Since(started)
	peakHeap := stopMemory()
	close(progressCh)
	<-progressDone
	if runErr != nil {
		t.Fatalf("index %d-byte batch=%d: %v", inputBytes, batchSize, runErr)
	}
	embeddings, err := store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{RepoID: "benchmark", NamespaceID: result.NamespaceID})
	if err != nil {
		t.Fatalf("list benchmark embeddings: %v", err)
	}
	if len(embeddings) != chunks || result.EmbeddedChunks != chunks || result.FailedChunks != 0 {
		t.Fatalf("index result=%#v stored=%d, want %d", result, len(embeddings), chunks)
	}
	latencies, providerFailures := timed.snapshot()
	var firstThroughput, secondThroughput, sustainedRatio float64
	halvesMeasured := batchSize <= chunks/2 && !halfwayAt.IsZero()
	if halvesMeasured {
		firstHalfSeconds := halfwayAt.Sub(started).Seconds()
		secondHalfSeconds := elapsed.Seconds() - firstHalfSeconds
		if firstHalfSeconds > 0 && secondHalfSeconds > 0 {
			firstThroughput = float64(chunks/2) / firstHalfSeconds
			secondThroughput = float64(chunks-chunks/2) / secondHalfSeconds
			sustainedRatio = secondThroughput / firstThroughput
		} else {
			halvesMeasured = false
		}
	}
	throughput := float64(chunks) / elapsed.Seconds()
	caseResult := realModelBenchmarkCase{
		InputBytes: inputBytes, BatchSize: batchSize, Chunks: chunks,
		ProviderCalls: len(latencies), ProviderFailures: providerFailures,
		BatchLatencyP50MS: percentileMilliseconds(latencies, 0.50), BatchLatencyP95MS: percentileMilliseconds(latencies, 0.95),
		ElapsedSeconds: round(elapsed.Seconds(), 3), ChunksPerSecond: round(throughput, 3),
		FirstHalfChunksPerSecond: round(firstThroughput, 3), SecondHalfChunksPerSecond: round(secondThroughput, 3),
		SustainedRatio: round(sustainedRatio, 3), SustainedHalvesMeasured: halvesMeasured, FullCorpusETAHours: round(float64(corpusChunks)/throughput/3600, 2),
		ProgressEvents: progressEvents, SQLiteGrowthBytes: sqliteFilesSize(dbPath) - beforeBytes, GoHeapPeakBytes: peakHeap,
		EmbeddedChunks: result.EmbeddedChunks, FailedChunks: result.FailedChunks,
	}
	t.Logf("benchmark case input=%d batch=%d throughput=%.3f chunks/s p95=%.3f ms eta=%.2f h", inputBytes, batchSize, caseResult.ChunksPerSecond, caseResult.BatchLatencyP95MS, caseResult.FullCorpusETAHours)
	return caseResult
}

func newBenchmarkOllamaProvider(endpoint, model string, dimensions, batchSize int) (*OllamaProvider, error) {
	return NewOllamaProvider(EmbeddingProviderProfile{
		ProfileID: fmt.Sprintf("real-benchmark-%d", batchSize), ProviderID: "ollama", ProviderType: "ollama",
		Endpoint: endpoint, Model: model, Dimensions: dimensions, BatchSize: batchSize, MaxInputTokens: 4096,
		Timeout: 2 * time.Minute,
	}, ProviderOptions{HTTPClient: &http.Client{Timeout: 2 * time.Minute}, MaxRetries: 1})
}

func uniqueBenchmarkInput(size, index, batchSize int) string {
	prefix := fmt.Sprintf("gitcode-mcp public benchmark input=%04d batch=%02d ", index, batchSize)
	if len(prefix) >= size {
		return prefix[:size]
	}
	vocabulary := []string{
		"cache", "issue", "pull", "request", "comment", "review", "discussion", "sync", "provider", "embedding",
		"vector", "sqlite", "checkpoint", "retry", "batch", "namespace", "source", "record", "chunk", "progress",
		"service", "daemon", "index", "search", "hybrid", "metadata", "revision", "frontier", "coverage", "deterministic",
	}
	random := rand.New(rand.NewSource(int64(index*1009 + batchSize*9176 + size)))
	var builder strings.Builder
	builder.Grow(size)
	builder.WriteString(prefix)
	for builder.Len() < size {
		word := vocabulary[random.Intn(len(vocabulary))] + " "
		if remaining := size - builder.Len(); remaining < len(word) {
			builder.WriteString(word[:remaining])
		} else {
			builder.WriteString(word)
		}
	}
	return builder.String()
}

func benchmarkEnvInt(t *testing.T, name string, fallback, minimum int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		t.Fatalf("%s=%q must be an integer >= %d", name, raw, minimum)
	}
	return value
}

func percentileMilliseconds(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted))*percentile+0.999999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return round(milliseconds(sorted[index]), 3)
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func round(value float64, places int) float64 {
	power := 1.0
	for i := 0; i < places; i++ {
		power *= 10
	}
	return float64(int64(value*power+0.5)) / power
}

func startHeapSampler() func() uint64 {
	done := make(chan struct{})
	result := make(chan uint64, 1)
	go func() {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		var peak uint64
		for {
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			if stats.HeapInuse > peak {
				peak = stats.HeapInuse
			}
			select {
			case <-done:
				result <- peak
				return
			case <-ticker.C:
			}
		}
	}()
	return func() uint64 {
		close(done)
		return <-result
	}
}

func sqliteFilesSize(path string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

func readBenchmarkMachine() benchmarkMachine {
	machine := benchmarkMachine{
		Label: strings.TrimSpace(os.Getenv("GITCODE_MCP_RAG_BENCHMARK_MACHINE")),
		GOOS:  runtime.GOOS, GOARCH: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(),
	}
	if runtime.GOOS == "darwin" {
		machine.CPU = commandOutput("sysctl", "-n", "machdep.cpu.brand_string")
		if raw := commandOutput("sysctl", "-n", "hw.memsize"); raw != "" {
			machine.MemoryBytes, _ = strconv.ParseUint(raw, 10, 64)
		}
	} else if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						kilobytes, _ := strconv.ParseUint(fields[1], 10, 64)
						machine.MemoryBytes = kilobytes * 1024
					}
					break
				}
			}
		}
	}
	return machine
}

func commandOutput(name string, args ...string) string {
	data, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readBenchmarkProviderInfo(ctx context.Context, provider *OllamaProvider, endpoint, model, revision string) benchmarkProviderInfo {
	info := benchmarkProviderInfo{Type: "ollama", Endpoint: endpoint, Model: model, ModelRevision: revision}
	var version struct {
		Version string `json:"version"`
	}
	if err := provider.getJSON(ctx, "/api/version", &version); err == nil {
		info.Version = version.Version
	}
	var running struct {
		Models []struct {
			Name     string `json:"name"`
			Model    string `json:"model"`
			Size     int64  `json:"size"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := provider.getJSON(ctx, "/api/ps", &running); err == nil {
		for _, item := range running.Models {
			if item.Name == model || item.Model == model {
				info.LoadedBytes = item.Size
				info.LoadedVRAMBytes = item.SizeVRAM
				break
			}
		}
	}
	return info
}
