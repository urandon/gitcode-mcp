package rag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

const (
	ProviderFailureUnavailable         = "unavailable"
	ProviderFailureModelMissing        = "model_missing"
	ProviderFailureTimeout             = "timeout"
	ProviderFailureDimensionMismatch   = "dimension_mismatch"
	ProviderFailureUnsupportedResponse = "unsupported_response"

	DefaultEmbeddingDType              = "float32"
	DefaultEmbeddingNormalization      = "l2"
	DefaultChunkPolicyID               = "heading"
	DefaultDocumentInstructionID       = "doc-default"
	DefaultQueryInstructionID          = "query-default"
	DefaultLanguagePolicyID            = "ru-zh-en-v1"
	defaultEmbeddingProviderMaxRetries = 1
)

type EmbeddingProvider interface {
	Profile() EmbeddingProviderProfile
	ModelInfo(context.Context) (EmbeddingModelInfo, error)
	Embed(context.Context, EmbedRequest) (EmbedResponse, error)
	NamespaceIdentity(context.Context, NamespaceRequest) (cache.EmbeddingNamespaceIdentity, error)
}

type EmbeddingProviderProfile struct {
	ProfileID      string
	ProviderID     string
	ProviderType   string
	Endpoint       string
	Model          string
	Dimensions     int
	BatchSize      int
	MaxInputTokens int
	Timeout        time.Duration
}

type EmbeddingModelInfo struct {
	Model    string
	Revision string
}

type EmbedRequest struct {
	Inputs []string
}

type EmbedResponse struct {
	Model      string
	Revision   string
	Dimensions int
	Embeddings [][]float32
}

type NamespaceRequest struct {
	RepoID                string
	ChunkPolicyID         string
	LanguagePolicyID      string
	DocumentInstructionID string
	QueryInstructionID    string
}

type ProviderOptions struct {
	HTTPClient *http.Client
	MaxRetries int
}

type ProviderError struct {
	Class   string
	Message string
	Err     error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProviderError) DiagnosticCode() string {
	if e == nil {
		return ""
	}
	return e.Class
}

func NewEmbeddingProviderFromConfig(cfg config.Config, profileName string, opts ProviderOptions) (EmbeddingProvider, error) {
	profileID, profile, providerID, provider, err := resolveEmbeddingProfile(cfg, profileName)
	if err != nil {
		return nil, err
	}
	profileInfo := embeddingProviderProfile(profileID, profile, providerID, provider)
	switch strings.TrimSpace(profileInfo.ProviderType) {
	case "fake":
		return NewFakeProvider(profileInfo)
	case "ollama":
		return NewOllamaProvider(profileInfo, opts)
	default:
		return nil, fmt.Errorf("rag provider: unsupported provider type %q", profileInfo.ProviderType)
	}
}

func EnsureEmbeddingNamespace(ctx context.Context, store embeddingNamespaceStore, provider EmbeddingProvider, req NamespaceRequest) (cache.EmbeddingNamespace, error) {
	identity, err := provider.NamespaceIdentity(ctx, req)
	if err != nil {
		return cache.EmbeddingNamespace{}, err
	}
	if namespace, ok, err := store.ResolveEmbeddingNamespace(ctx, identity); err != nil {
		return cache.EmbeddingNamespace{}, err
	} else if ok {
		return namespace, nil
	}
	now := time.Now().UTC()
	return store.UpsertEmbeddingNamespace(ctx, cache.EmbeddingNamespace{EmbeddingNamespaceIdentity: identity, CreatedAt: now, UpdatedAt: now})
}

type embeddingNamespaceStore interface {
	ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error)
	UpsertEmbeddingNamespace(context.Context, cache.EmbeddingNamespace) (cache.EmbeddingNamespace, error)
}

type FakeProvider struct {
	profile  EmbeddingProviderProfile
	revision string
}

func NewFakeProvider(profile EmbeddingProviderProfile) (*FakeProvider, error) {
	if err := validateEmbeddingProviderProfile(profile); err != nil {
		return nil, err
	}
	if strings.TrimSpace(profile.ProviderType) == "" {
		profile.ProviderType = "fake"
	}
	return &FakeProvider{profile: profile, revision: "fake-deterministic-v1"}, nil
}

func (p *FakeProvider) Profile() EmbeddingProviderProfile { return p.profile }

func (p *FakeProvider) ModelInfo(context.Context) (EmbeddingModelInfo, error) {
	return EmbeddingModelInfo{Model: p.profile.Model, Revision: p.revision}, nil
}

func (p *FakeProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	if err := ctx.Err(); err != nil {
		return EmbedResponse{}, classifyProviderError("fake embedding cancelled", err)
	}
	if len(req.Inputs) == 0 {
		return EmbedResponse{}, providerError(ProviderFailureUnsupportedResponse, "embedding input is empty", nil)
	}
	embeddings := make([][]float32, 0, len(req.Inputs))
	for _, input := range req.Inputs {
		embeddings = append(embeddings, deterministicEmbedding(input, p.profile.Dimensions))
	}
	return EmbedResponse{Model: p.profile.Model, Revision: p.revision, Dimensions: p.profile.Dimensions, Embeddings: embeddings}, nil
}

func (p *FakeProvider) NamespaceIdentity(ctx context.Context, req NamespaceRequest) (cache.EmbeddingNamespaceIdentity, error) {
	info, err := p.ModelInfo(ctx)
	if err != nil {
		return cache.EmbeddingNamespaceIdentity{}, err
	}
	return namespaceIdentity(p.profile, info, req)
}

type OllamaProvider struct {
	profile    EmbeddingProviderProfile
	httpClient *http.Client
	maxRetries int
}

func NewOllamaProvider(profile EmbeddingProviderProfile, opts ProviderOptions) (*OllamaProvider, error) {
	if err := validateEmbeddingProviderProfile(profile); err != nil {
		return nil, err
	}
	if _, err := url.ParseRequestURI(profile.Endpoint); err != nil {
		return nil, fmt.Errorf("rag provider: invalid ollama endpoint %q: %w", profile.Endpoint, err)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: profile.Timeout}
	}
	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries == 0 {
		maxRetries = defaultEmbeddingProviderMaxRetries
	}
	return &OllamaProvider{profile: profile, httpClient: client, maxRetries: maxRetries}, nil
}

func (p *OllamaProvider) Profile() EmbeddingProviderProfile { return p.profile }

func (p *OllamaProvider) ModelInfo(ctx context.Context) (EmbeddingModelInfo, error) {
	models, err := p.listModels(ctx)
	if err != nil {
		return EmbeddingModelInfo{}, err
	}
	for _, model := range models {
		if model.Name == p.profile.Model {
			return EmbeddingModelInfo{Model: model.Name, Revision: model.Digest}, nil
		}
	}
	return EmbeddingModelInfo{}, providerError(ProviderFailureModelMissing, "embedding model is not available: "+p.profile.Model, nil)
}

func (p *OllamaProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	if len(req.Inputs) == 0 {
		return EmbedResponse{}, providerError(ProviderFailureUnsupportedResponse, "embedding input is empty", nil)
	}
	info, err := p.ModelInfo(ctx)
	if err != nil {
		return EmbedResponse{}, err
	}
	embeddings := make([][]float32, 0, len(req.Inputs))
	for start := 0; start < len(req.Inputs); start += p.profile.BatchSize {
		end := start + p.profile.BatchSize
		if end > len(req.Inputs) {
			end = len(req.Inputs)
		}
		for _, input := range req.Inputs[start:end] {
			vector, err := p.embedOne(ctx, input)
			if err != nil {
				return EmbedResponse{}, err
			}
			if len(vector) != p.profile.Dimensions {
				return EmbedResponse{}, providerError(ProviderFailureDimensionMismatch, fmt.Sprintf("embedding dimensions = %d, want %d", len(vector), p.profile.Dimensions), nil)
			}
			embeddings = append(embeddings, vector)
		}
	}
	return EmbedResponse{Model: info.Model, Revision: info.Revision, Dimensions: p.profile.Dimensions, Embeddings: embeddings}, nil
}

func (p *OllamaProvider) NamespaceIdentity(ctx context.Context, req NamespaceRequest) (cache.EmbeddingNamespaceIdentity, error) {
	info, err := p.ModelInfo(ctx)
	if err != nil {
		return cache.EmbeddingNamespaceIdentity{}, err
	}
	return namespaceIdentity(p.profile, info, req)
}

type ollamaModel struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

func (p *OllamaProvider) listModels(ctx context.Context) ([]ollamaModel, error) {
	var payload struct {
		Models []ollamaModel `json:"models"`
	}
	if err := p.getJSON(ctx, "/api/tags", &payload); err != nil {
		return nil, err
	}
	return payload.Models, nil
}

func (p *OllamaProvider) embedOne(ctx context.Context, input string) ([]float32, error) {
	body := map[string]any{"model": p.profile.Model, "prompt": input}
	var payload struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := p.postJSON(ctx, "/api/embeddings", body, &payload); err != nil {
		return nil, err
	}
	if len(payload.Embedding) == 0 {
		return nil, providerError(ProviderFailureUnsupportedResponse, "ollama returned an empty embedding", nil)
	}
	vector := make([]float32, len(payload.Embedding))
	for i, value := range payload.Embedding {
		vector[i] = float32(value)
	}
	return vector, nil
}

func (p *OllamaProvider) getJSON(ctx context.Context, path string, target any) error {
	return p.doJSON(ctx, http.MethodGet, path, nil, target)
}

func (p *OllamaProvider) postJSON(ctx context.Context, path string, body any, target any) error {
	return p.doJSON(ctx, http.MethodPost, path, body, target)
}

func (p *OllamaProvider) doJSON(ctx context.Context, method, path string, body any, target any) error {
	var last error
	attempts := 1 + p.maxRetries
	for attempt := 0; attempt < attempts; attempt++ {
		err := p.doJSONOnce(ctx, method, path, body, target)
		if err == nil {
			return nil
		}
		last = err
		var providerErr *ProviderError
		if errors.As(err, &providerErr) && providerErr.Class != ProviderFailureUnavailable && providerErr.Class != ProviderFailureTimeout {
			return err
		}
		if ctx.Err() != nil {
			return classifyProviderError("embedding provider request failed", ctx.Err())
		}
	}
	return last
}

func (p *OllamaProvider) doJSONOnce(ctx context.Context, method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return providerError(ProviderFailureUnsupportedResponse, "failed to encode provider request", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.profile.Endpoint, "/")+path, reader)
	if err != nil {
		return providerError(ProviderFailureUnavailable, "failed to build provider request", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return classifyProviderError("embedding provider request failed", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return classifyProviderError("embedding provider response read failed", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyHTTPProviderError(path, resp.StatusCode, string(data))
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return providerError(ProviderFailureUnsupportedResponse, "embedding provider returned unsupported JSON", err)
	}
	return nil
}

func resolveEmbeddingProfile(cfg config.Config, profileName string) (string, config.RAGProfileConfig, string, config.RAGProviderConfig, error) {
	profileID := strings.TrimSpace(profileName)
	if profileID == "" {
		profileID = strings.TrimSpace(cfg.RAG.DefaultProfile)
	}
	if profileID == "" {
		profileID = config.DefaultRAGProfile
	}
	profile, ok := cfg.RAG.Profiles[profileID]
	if !ok {
		return "", config.RAGProfileConfig{}, "", config.RAGProviderConfig{}, fmt.Errorf("rag provider: profile %q is not configured", profileID)
	}
	providerID := strings.TrimSpace(profile.Provider)
	if providerID == "" {
		return "", config.RAGProfileConfig{}, "", config.RAGProviderConfig{}, fmt.Errorf("rag provider: profile %q has no provider", profileID)
	}
	provider, ok := cfg.RAG.Providers[providerID]
	if !ok {
		return "", config.RAGProfileConfig{}, "", config.RAGProviderConfig{}, fmt.Errorf("rag provider: provider %q is not configured", providerID)
	}
	return profileID, profile, providerID, provider, nil
}

func embeddingProviderProfile(profileID string, profile config.RAGProfileConfig, providerID string, provider config.RAGProviderConfig) EmbeddingProviderProfile {
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	batchSize := profile.BatchSize
	if batchSize <= 0 {
		batchSize = 1
	}
	return EmbeddingProviderProfile{
		ProfileID:      profileID,
		ProviderID:     providerID,
		ProviderType:   firstNonEmpty(provider.Type, providerID),
		Endpoint:       strings.TrimSpace(provider.Endpoint),
		Model:          strings.TrimSpace(profile.Model),
		Dimensions:     profile.Dimensions,
		BatchSize:      batchSize,
		MaxInputTokens: profile.MaxInputTokens,
		Timeout:        timeout,
	}
}

func validateEmbeddingProviderProfile(profile EmbeddingProviderProfile) error {
	if strings.TrimSpace(profile.ProfileID) == "" || strings.TrimSpace(profile.ProviderID) == "" || strings.TrimSpace(profile.ProviderType) == "" {
		return fmt.Errorf("rag provider: profile id, provider id, and provider type are required")
	}
	if strings.TrimSpace(profile.Model) == "" {
		return fmt.Errorf("rag provider: model is required")
	}
	if profile.Dimensions <= 0 {
		return fmt.Errorf("rag provider: dimensions must be positive")
	}
	if profile.BatchSize <= 0 {
		return fmt.Errorf("rag provider: batch size must be positive")
	}
	if profile.Timeout <= 0 {
		return fmt.Errorf("rag provider: timeout must be positive")
	}
	return nil
}

func namespaceIdentity(profile EmbeddingProviderProfile, info EmbeddingModelInfo, req NamespaceRequest) (cache.EmbeddingNamespaceIdentity, error) {
	repoID := strings.TrimSpace(req.RepoID)
	if repoID == "" {
		return cache.EmbeddingNamespaceIdentity{}, fmt.Errorf("rag provider: repo id is required for embedding namespace")
	}
	chunkPolicyID := firstNonEmpty(req.ChunkPolicyID, "chunk-tokens-default")
	languagePolicyID := firstNonEmpty(req.LanguagePolicyID, DefaultLanguagePolicyID)
	docInstructionID := firstNonEmpty(req.DocumentInstructionID, DefaultDocumentInstructionID)
	queryInstructionID := firstNonEmpty(req.QueryInstructionID, DefaultQueryInstructionID)
	configHash := embeddingConfigHash(profile, chunkPolicyID, languagePolicyID, docInstructionID, queryInstructionID)
	return cache.EmbeddingNamespaceIdentity{
		RepoID:                repoID,
		ProfileID:             profile.ProfileID,
		ProviderID:            profile.ProviderID,
		ProviderType:          profile.ProviderType,
		ModelID:               info.Model,
		ModelRevision:         info.Revision,
		Dimensions:            profile.Dimensions,
		DType:                 DefaultEmbeddingDType,
		Normalization:         DefaultEmbeddingNormalization,
		DocumentInstructionID: docInstructionID,
		QueryInstructionID:    queryInstructionID,
		ChunkPolicyID:         chunkPolicyID,
		LanguagePolicyID:      languagePolicyID,
		ConfigHash:            configHash,
	}, nil
}

func embeddingConfigHash(profile EmbeddingProviderProfile, chunkPolicyID, languagePolicyID, docInstructionID, queryInstructionID string) string {
	payload := map[string]any{
		"provider_id":             profile.ProviderID,
		"provider_type":           profile.ProviderType,
		"model":                   profile.Model,
		"dimensions":              profile.Dimensions,
		"dtype":                   DefaultEmbeddingDType,
		"normalization":           DefaultEmbeddingNormalization,
		"document_instruction_id": docInstructionID,
		"query_instruction_id":    queryInstructionID,
		"chunk_policy_id":         chunkPolicyID,
		"language_policy_id":      languagePolicyID,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func deterministicEmbedding(input string, dimensions int) []float32 {
	vector := make([]float32, dimensions)
	var magnitude float64
	for i := 0; i < dimensions; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", i, input)))
		raw := binary.BigEndian.Uint32(sum[:4])
		value := (float64(raw)/float64(math.MaxUint32))*2 - 1
		vector[i] = float32(value)
		magnitude += value * value
	}
	if magnitude == 0 {
		return vector
	}
	scale := 1 / math.Sqrt(magnitude)
	for i := range vector {
		vector[i] = float32(float64(vector[i]) * scale)
	}
	return vector
}

func providerError(class, message string, err error) error {
	return &ProviderError{Class: class, Message: message, Err: err}
}

func classifyProviderError(message string, err error) error {
	if err == nil {
		return providerError(ProviderFailureUnavailable, message, nil)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return providerError(ProviderFailureTimeout, message, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return providerError(ProviderFailureTimeout, message, err)
	}
	return providerError(ProviderFailureUnavailable, message, err)
}

func classifyHTTPProviderError(path string, status int, body string) error {
	lower := strings.ToLower(body)
	if path == "/api/embeddings" && status == http.StatusNotFound || strings.Contains(lower, "model") && (strings.Contains(lower, "not found") || strings.Contains(lower, "missing")) {
		return providerError(ProviderFailureModelMissing, fmt.Sprintf("embedding provider reported missing model: status %d", status), nil)
	}
	return providerError(ProviderFailureUnavailable, fmt.Sprintf("embedding provider returned status %d", status), nil)
}
