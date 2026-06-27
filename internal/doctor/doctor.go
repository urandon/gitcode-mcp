package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

type Store interface {
	ListRepositories(context.Context) ([]cache.RepositoryBinding, error)
	RecordCounts(context.Context, string) (cache.RecordCounts, error)
	Close() error
}

type Service interface {
	CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error)
	SyncStatus(context.Context, service.ListSourcesRequest) (service.SyncStatusSummaryResult, error)
	StaleIndex(context.Context, service.StaleIndexRequest) (service.StaleIndexResult, error)
}

type OpenStoreFunc func(context.Context, string) (Store, error)

type NewServiceFunc func(Store) Service

type Request struct {
	Version            string
	Source             config.Source
	CredentialReporter config.CredentialStatusReporter
	CredentialStatus   *config.CredentialStatus
	CachePath          string
	Live               bool
	ProviderMode       string
	MCPToolAccess      string
	APIBaseURL         string
	RepoID             string
	LiveBinding        service.LiveRepositoryBinding
	OpenStore          OpenStoreFunc
	NewService         NewServiceFunc
}

type Report struct {
	Version       string                `json:"version"`
	Config        ConfigSection         `json:"config"`
	Cache         CacheSection          `json:"cache"`
	Repo          RepoSection           `json:"repo"`
	Credential    CredentialSection     `json:"credential"`
	Sync          SyncSection           `json:"sync"`
	Index         IndexSection          `json:"index"`
	MCP           MCPSection            `json:"mcp"`
	LiveProvider  LiveProviderSection   `json:"live_provider"`
	LiveReadiness LiveReadinessSnapshot `json:"live_readiness,omitempty"`
	AuthProbe     AuthProbeSection      `json:"auth_probe"`
	Diagnostics   []string              `json:"diagnostics,omitempty"`
}

type ConfigSection struct {
	Path      string `json:"path"`
	Source    string `json:"source"`
	Format    string `json:"format"`
	Exists    bool   `json:"exists"`
	CachePath string `json:"cache_path"`
}

type CacheSection struct {
	Path            string `json:"path"`
	Status          string `json:"status"`
	SchemaVersion   string `json:"schema_version"`
	ExpectedVersion string `json:"expected_schema_version,omitempty"`
	Records         int    `json:"records"`
	Chunks          int    `json:"chunks"`
	SyncEvents      int    `json:"sync_events"`
	Remediation     string `json:"remediation,omitempty"`
}

type RepoSection struct {
	Status   string `json:"status"`
	RepoID   string `json:"repo_id,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Name     string `json:"name,omitempty"`
	Scopes   string `json:"scopes,omitempty"`
	BindHint string `json:"bind_hint,omitempty"`
}

type CredentialSection struct {
	Status             string   `json:"status"`
	Source             string   `json:"source"`
	TokenPresent       bool     `json:"token_present"`
	StoreMode          string   `json:"store_mode"`
	AttemptedSources   []string `json:"attempted_sources,omitempty"`
	AvailableSources   []string `json:"available_sources,omitempty"`
	UnavailableSources []string `json:"unavailable_sources,omitempty"`
	Remediation        string   `json:"remediation,omitempty"`
}

type SyncSection struct {
	Status              string `json:"status"`
	LastSyncAt          string `json:"last_sync_at,omitempty"`
	LastSyncStartedAt   string `json:"last_sync_started_at,omitempty"`
	LastSyncCompletedAt string `json:"last_sync_completed_at,omitempty"`
	FreshCount          int    `json:"fresh_count"`
	StaleCount          int    `json:"stale_count"`
	CacheEmpty          bool   `json:"cache_empty"`
	ZeroDelta           bool   `json:"zero_delta"`
}

type IndexSection struct {
	Status        string `json:"status"`
	StaleCount    int    `json:"stale_count"`
	LastIndexedAt string `json:"last_indexed_at,omitempty"`
}

type MCPSection struct {
	Status         string `json:"status"`
	TransportStdio string `json:"transport_stdio"`
	TransportHTTP  string `json:"transport_http"`
	ServerVersion  string `json:"server_version"`
	ToolAccess     string `json:"tool_access"`
}

type LiveProviderSection struct {
	Status           string `json:"status"`
	Reachable        string `json:"reachable"`
	ProviderMode     string `json:"provider_mode"`
	APIBaseURL       string `json:"api_base_url,omitempty"`
	APIBaseURLSource string `json:"api_base_url_source,omitempty"`
	ReadinessStatus  string `json:"readiness_status,omitempty"`
	Remediation      string `json:"remediation,omitempty"`
}

type LiveReadinessSnapshot struct {
	ProviderMode      string   `json:"provider_mode"`
	CredentialSource  string   `json:"credential_source"`
	CredentialPresent bool     `json:"credential_present"`
	CachePath         string   `json:"cache_path"`
	APIBaseURL        string   `json:"api_base_url,omitempty"`
	APIBaseURLSource  string   `json:"api_base_url_source,omitempty"`
	ReadinessStatus   string   `json:"readiness_status"`
	Diagnostics       []string `json:"diagnostics,omitempty"`
}

type AuthProbeSection struct {
	Status      string `json:"status"`
	ProbeResult string `json:"probe_result"`
	Remediation string `json:"remediation,omitempty"`
}

func Build(ctx context.Context, req Request) (Report, error) {
	if req.Source == nil {
		req.Source = config.OSSource{}
	}
	if req.CredentialReporter == nil {
		req.CredentialReporter = config.DefaultCredentialProvider(req.Source)
	}
	if req.OpenStore == nil {
		req.OpenStore = func(ctx context.Context, path string) (Store, error) {
			return cache.NewSQLiteReadOnlyStore(ctx, path)
		}
	}
	if req.NewService == nil {
		req.NewService = func(store Store) Service {
			if cacheStore, ok := store.(cache.Store); ok {
				return service.New(cacheStore)
			}
			return nil
		}
	}

	eff, configLoadErr := config.LoadEffective(req.Source, config.Overrides{})
	if configLoadErr != nil {
		eff = config.EffectiveConfig{Config: config.Config{}, Location: config.Locate(req.Source), CredentialPolicy: config.CredentialConfig{Store: "auto"}}
	}

	cred := req.CredentialReporter.Status(ctx, eff)
	if req.CredentialStatus != nil {
		cred = *req.CredentialStatus
	}
	cred.Source = config.RedactDiagnostic(cred.Source, req.Source)
	cred.ErrorClass = config.RedactDiagnostic(cred.ErrorClass, req.Source)
	cred.Remediation = config.RedactDiagnostic(cred.Remediation, req.Source)

	cachePath := strings.TrimSpace(req.CachePath)
	if cachePath == "" {
		cachePath = eff.Config.CachePath
	}
	if cachePath == "" {
		cachePath = DefaultCachePath(req.Source)
	}

	report := Report{Version: req.Version}
	report.Config = ConfigSection{Path: eff.Location.Path, Source: eff.Location.Source, Format: eff.Location.Format, Exists: eff.Location.Exists, CachePath: cachePath}
	report.Cache = CacheSection{Path: cachePath, Status: "not_available", SchemaVersion: "unknown"}
	report.Repo = RepoSection{Status: "no_repo_bound", BindHint: "run 'gitcode-mcp repo add --repo <id> --owner <owner> --name <name> --api-base-url <url> --scopes issues,wiki'"}
	report.Sync = SyncSection{Status: "no_repo_bound"}
	report.Index = IndexSection{Status: "no_repo_bound"}
	toolAccess, err := config.NormalizeMCPToolAccess(req.MCPToolAccess)
	if err != nil {
		toolAccess = config.MCPToolAccessRead
	}
	report.MCP = MCPSection{Status: "available", TransportStdio: "supported", TransportHTTP: "supported", ServerVersion: req.Version, ToolAccess: toolAccess}
	report.LiveProvider = LiveProviderSection{Status: "skipped", Reachable: "not_configured", ProviderMode: "fixture", Remediation: "set GITCODE_TOKEN and use --live to enable live provider"}
	report.AuthProbe = AuthProbeSection{Status: "skipped", ProbeResult: "not_probed", Remediation: "set GITCODE_TOKEN to enable authentication probing"}

	report.Credential = CredentialSection{Source: emptyAsNone(cred.Source), TokenPresent: cred.Present, StoreMode: cred.StoreMode, AttemptedSources: append([]string(nil), cred.AttemptedSources...), AvailableSources: append([]string(nil), cred.AvailableSources...), UnavailableSources: append([]string(nil), cred.UnavailableSources...), Remediation: cred.Remediation}
	if cred.Present {
		report.Credential.Status = "token_configured"
	} else {
		report.Credential.Status = "no_token_configured"
		report.Diagnostics = append(report.Diagnostics, "no token configured; available sources: "+strings.Join(cred.AvailableSources, ","))
	}

	if req.Live {
		initializeLiveReadiness(&report, req, cred, cachePath, "")
	}

	store, err := req.OpenStore(ctx, cachePath)
	if err != nil {
		applyCacheOpenError(&report, err)
		return redactReport(report, req.Source), nil
	}
	defer store.Close()
	report.Cache.Status = "available"
	if sv, ok := store.(interface {
		SchemaVersion(context.Context) (int, error)
	}); ok {
		if version, err := sv.SchemaVersion(ctx); err == nil {
			report.Cache.SchemaVersion = strconv.Itoa(version)
		}
	}

	repos, err := store.ListRepositories(ctx)
	if err != nil || len(repos) == 0 {
		report.Diagnostics = append(report.Diagnostics, "no repo bound; add a repository binding before live readiness checks")
		if req.Live {
			finalizeLiveReadiness(&report, req, cred, cachePath, cache.RepositoryBinding{}, false, "")
		}
		return redactReport(report, req.Source), nil
	}
	repo, repoFound := selectRepositoryBinding(repos, req.RepoID, req.LiveBinding)
	if req.Live {
		finalizeLiveReadiness(&report, req, cred, cachePath, repo, repoFound, "")
	}
	if !repoFound {
		report.Diagnostics = append(report.Diagnostics, "selected repo binding not found")
		return redactReport(report, req.Source), nil
	}
	report.Repo = RepoSection{Status: "ready", RepoID: repo.RepoID, Owner: "[REDACTED]", Name: "[REDACTED]", Scopes: scopesText(repo.Scopes)}

	svc := req.NewService(store)
	if svc == nil {
		counts, err := store.RecordCounts(ctx, repo.RepoID)
		if err == nil {
			report.Cache.Records = counts.Records
			report.Cache.Chunks = counts.Chunks
			report.Cache.SyncEvents = counts.SyncEvents
		}
		return redactReport(report, req.Source), nil
	}
	cacheStatus, err := svc.CacheStatus(ctx, service.CacheStatusRequest{RepoID: repo.RepoID})
	if err == nil {
		report.Cache.Records = cacheStatus.Records
		report.Cache.Chunks = cacheStatus.Chunks
		report.Cache.SyncEvents = cacheStatus.SyncEvents
	}
	applySync(ctx, svc, repo.RepoID, &report)
	applyIndex(ctx, svc, repo.RepoID, &report)
	return redactReport(report, req.Source), nil
}

func selectRepositoryBinding(repos []cache.RepositoryBinding, repoID string, liveBinding service.LiveRepositoryBinding) (cache.RepositoryBinding, bool) {
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoID < repos[j].RepoID })
	if strings.TrimSpace(repoID) != "" {
		for _, candidate := range repos {
			if candidate.RepoID == repoID {
				return candidate, true
			}
		}
		return cache.RepositoryBinding{}, false
	}
	if strings.TrimSpace(liveBinding.RepoID) != "" {
		for _, candidate := range repos {
			if candidate.RepoID == liveBinding.RepoID {
				return candidate, true
			}
		}
	}
	return repos[0], true
}

func initializeLiveReadiness(report *Report, req Request, cred config.CredentialStatus, cachePath, cacheDiagnostic string) {
	finalizeLiveReadiness(report, req, cred, cachePath, cache.RepositoryBinding{}, false, cacheDiagnostic)
}

func finalizeLiveReadiness(report *Report, req Request, cred config.CredentialStatus, cachePath string, repo cache.RepositoryBinding, repoFound bool, cacheDiagnostic string) {
	providerMode := firstNonEmpty(req.ProviderMode, "live-http")
	apiBaseURL, apiBaseURLSource := effectiveAPIBaseURL(req, repo, repoFound)
	status, diagnostics := liveReadinessStatus(repoFound, apiBaseURL, cred.Present, cacheDiagnostic)
	report.LiveReadiness = LiveReadinessSnapshot{ProviderMode: providerMode, CredentialSource: emptyAsNone(cred.Source), CredentialPresent: cred.Present, CachePath: cachePath, APIBaseURL: apiBaseURL, APIBaseURLSource: apiBaseURLSource, ReadinessStatus: status, Diagnostics: diagnostics}
	report.LiveProvider.ProviderMode = providerMode
	report.LiveProvider.APIBaseURL = apiBaseURL
	report.LiveProvider.APIBaseURLSource = apiBaseURLSource
	report.LiveProvider.ReadinessStatus = status
	report.LiveProvider.Reachable = "skipped"
	if status == "ready" || status == "configuration_warning" {
		report.LiveProvider.Status = "configured"
		report.LiveProvider.Remediation = ""
		report.AuthProbe.Status = authProbeStatus(cred.AuthProbe)
		report.AuthProbe.ProbeResult = authProbeMessage(cred.AuthProbe)
		report.AuthProbe.Remediation = cred.Remediation
		return
	}
	report.LiveProvider.Status = "error"
	report.LiveProvider.Remediation = strings.Join(diagnostics, "; ")
	if status == "missing_credential" {
		report.AuthProbe.Remediation = "missing_credential: set a token to enable authentication probing"
	}
}

func effectiveAPIBaseURL(req Request, repo cache.RepositoryBinding, repoFound bool) (string, string) {
	if repoFound && strings.TrimSpace(repo.APIBaseURL) != "" {
		return strings.TrimSpace(repo.APIBaseURL), "repository_binding.api_base_url"
	}
	if strings.TrimSpace(req.LiveBinding.APIBaseURL) != "" {
		return strings.TrimSpace(req.LiveBinding.APIBaseURL), firstNonEmpty(req.LiveBinding.BaseURLSource, "repository_binding.api_base_url")
	}
	if strings.TrimSpace(req.APIBaseURL) != "" {
		return strings.TrimSpace(req.APIBaseURL), "startup.api_base_url"
	}
	return "", ""
}

func liveReadinessStatus(repoFound bool, apiBaseURL string, credentialPresent bool, cacheDiagnostic string) (string, []string) {
	if !repoFound {
		return "configuration_error", []string{"missing_repository_binding"}
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		return "configuration_error", []string{"missing_api_base_url"}
	}
	if !validAPIBaseURL(apiBaseURL) {
		return "configuration_error", []string{"invalid_api_base_url"}
	}
	if !credentialPresent {
		return "missing_credential", []string{"missing_credential"}
	}
	if cacheDiagnostic != "" {
		return "configuration_warning", []string{cacheDiagnostic}
	}
	return "ready", nil
}

func validAPIBaseURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func applySync(ctx context.Context, svc Service, repoID string, report *Report) {
	syncStatus, err := svc.SyncStatus(ctx, service.ListSourcesRequest{RepoID: repoID})
	if err != nil {
		report.Sync.Status = "error"
		return
	}
	report.Sync.Status = "available"
	report.Sync.FreshCount = syncStatus.FreshCount
	report.Sync.StaleCount = syncStatus.StaleCount
	report.Sync.CacheEmpty = syncStatus.CacheEmpty
	report.Sync.ZeroDelta = syncStatus.ZeroDelta
	report.Sync.LastSyncAt = formatTime(syncStatus.LastSyncAt)
	report.Sync.LastSyncStartedAt = formatTime(syncStatus.LastSyncStartedAt)
	report.Sync.LastSyncCompletedAt = formatTime(syncStatus.LastSyncCompletedAt)
}

func applyIndex(ctx context.Context, svc Service, repoID string, report *Report) {
	stale, err := svc.StaleIndex(ctx, service.StaleIndexRequest{RepoID: repoID})
	if err != nil {
		report.Index.Status = "error"
		return
	}
	report.Index.Status = "available"
	report.Index.StaleCount = stale.StaleCount
	report.Index.LastIndexedAt = formatTime(stale.LastIndexedAt)
}

func applyCacheOpenError(report *Report, err error) {
	var schemaErr *cache.SchemaVersionError
	if errors.As(err, &schemaErr) {
		report.Cache.Status = "incompatible"
		report.Cache.SchemaVersion = strconv.Itoa(schemaErr.Compat.DetectedVersion)
		report.Cache.ExpectedVersion = strconv.Itoa(schemaErr.Compat.ExpectedVersion)
		report.Cache.Remediation = schemaErr.Compat.Remediation
		report.Diagnostics = append(report.Diagnostics, schemaErr.Compat.Message)
		return
	}
	report.Cache.Status = "not_available"
}

func authProbeStatus(probe *config.CredentialAuthProbe) string {
	if probe == nil || probe.Status == "" {
		return "configured"
	}
	return probe.Status
}

func authProbeMessage(probe *config.CredentialAuthProbe) string {
	if probe == nil {
		return "skipped"
	}
	if probe.Message != "" {
		return probe.Message
	}
	return probe.Status
}

func scopesText(scopes []cache.RepositoryScope) string {
	values := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		values = append(values, string(scope))
	}
	return strings.Join(values, ",")
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func DefaultCachePath(src config.Source) string {
	dir, err := src.UserCacheDir()
	if err != nil || dir == "" {
		return "cache.db"
	}
	return filepath.Join(dir, "gitcode-mcp", "cache.db")
}

func emptyAsNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func redactReport(report Report, src config.Source) Report {
	b, err := json.Marshal(report)
	if err != nil {
		return report
	}
	redacted := config.RedactDiagnostic(string(b), src)
	var out Report
	if err := json.Unmarshal([]byte(redacted), &out); err != nil {
		return report
	}
	return out
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintf(w, "version: %s\n", report.Version)
	fmt.Fprintln(w, "config:")
	fmt.Fprintf(w, "  path: %s\n", emptyAsNone(report.Config.Path))
	fmt.Fprintf(w, "  source: %s\n", report.Config.Source)
	fmt.Fprintf(w, "  format: %s\n", report.Config.Format)
	fmt.Fprintf(w, "  exists: %t\n", report.Config.Exists)
	fmt.Fprintf(w, "  cache_path: %s\n", report.Config.CachePath)
	fmt.Fprintln(w, "cache:")
	fmt.Fprintf(w, "  path: %s\n", report.Cache.Path)
	fmt.Fprintf(w, "  status: %s\n", report.Cache.Status)
	fmt.Fprintf(w, "  schema_version: %s\n", report.Cache.SchemaVersion)
	if report.Cache.ExpectedVersion != "" {
		fmt.Fprintf(w, "  expected_schema_version: %s\n", report.Cache.ExpectedVersion)
	}
	fmt.Fprintf(w, "  records: %d\n", report.Cache.Records)
	fmt.Fprintf(w, "  chunks: %d\n", report.Cache.Chunks)
	fmt.Fprintf(w, "  sync_events: %d\n", report.Cache.SyncEvents)
	if report.Cache.Remediation != "" {
		fmt.Fprintf(w, "  remediation: %s\n", report.Cache.Remediation)
	}
	fmt.Fprintln(w, "credential:")
	fmt.Fprintf(w, "  status: %s\n", report.Credential.Status)
	fmt.Fprintf(w, "  source: %s\n", report.Credential.Source)
	fmt.Fprintf(w, "  token_present: %t\n", report.Credential.TokenPresent)
	fmt.Fprintf(w, "  store_mode: %s\n", report.Credential.StoreMode)
	if len(report.Credential.AvailableSources) > 0 {
		fmt.Fprintf(w, "  available_sources: %s\n", strings.Join(report.Credential.AvailableSources, ","))
	}
	if report.Credential.Remediation != "" {
		fmt.Fprintf(w, "  remediation: %s\n", report.Credential.Remediation)
	}
	fmt.Fprintln(w, "repo:")
	fmt.Fprintf(w, "  status: %s\n", report.Repo.Status)
	if report.Repo.RepoID != "" {
		fmt.Fprintf(w, "  repo_id: %s\n", report.Repo.RepoID)
		fmt.Fprintf(w, "  owner: %s\n", report.Repo.Owner)
		fmt.Fprintf(w, "  name: %s\n", report.Repo.Name)
		fmt.Fprintf(w, "  scopes: %s\n", report.Repo.Scopes)
	}
	if report.Repo.BindHint != "" {
		fmt.Fprintf(w, "  bind_hint: %s\n", report.Repo.BindHint)
	}
	fmt.Fprintln(w, "sync:")
	fmt.Fprintf(w, "  status: %s\n", report.Sync.Status)
	if report.Sync.LastSyncAt != "" {
		fmt.Fprintf(w, "  last_sync_at: %s\n", report.Sync.LastSyncAt)
	}
	if report.Sync.LastSyncStartedAt != "" {
		fmt.Fprintf(w, "  last_sync_started_at: %s\n", report.Sync.LastSyncStartedAt)
	}
	if report.Sync.LastSyncCompletedAt != "" {
		fmt.Fprintf(w, "  last_sync_completed_at: %s\n", report.Sync.LastSyncCompletedAt)
	}
	fmt.Fprintf(w, "  fresh_count: %d\n", report.Sync.FreshCount)
	fmt.Fprintf(w, "  stale_count: %d\n", report.Sync.StaleCount)
	fmt.Fprintf(w, "  cache_empty: %t\n", report.Sync.CacheEmpty)
	fmt.Fprintf(w, "  zero_delta: %t\n", report.Sync.ZeroDelta)
	fmt.Fprintln(w, "index:")
	fmt.Fprintf(w, "  status: %s\n", report.Index.Status)
	fmt.Fprintf(w, "  stale_count: %d\n", report.Index.StaleCount)
	if report.Index.LastIndexedAt != "" {
		fmt.Fprintf(w, "  last_indexed_at: %s\n", report.Index.LastIndexedAt)
	}
	fmt.Fprintln(w, "mcp:")
	fmt.Fprintf(w, "  status: %s\n", report.MCP.Status)
	fmt.Fprintf(w, "  transport_stdio: %s\n", report.MCP.TransportStdio)
	fmt.Fprintf(w, "  transport_http: %s\n", report.MCP.TransportHTTP)
	fmt.Fprintf(w, "  server_version: %s\n", report.MCP.ServerVersion)
	fmt.Fprintf(w, "  tool_access: %s\n", report.MCP.ToolAccess)
	fmt.Fprintln(w, "live_provider:")
	fmt.Fprintf(w, "  status: %s\n", report.LiveProvider.Status)
	fmt.Fprintf(w, "  reachable: %s\n", report.LiveProvider.Reachable)
	fmt.Fprintf(w, "  provider_mode: %s\n", report.LiveProvider.ProviderMode)
	if report.LiveProvider.APIBaseURL != "" {
		fmt.Fprintf(w, "  api_base_url: %s\n", report.LiveProvider.APIBaseURL)
	}
	if report.LiveProvider.APIBaseURLSource != "" {
		fmt.Fprintf(w, "  api_base_url_source: %s\n", report.LiveProvider.APIBaseURLSource)
	}
	if report.LiveProvider.ReadinessStatus != "" {
		fmt.Fprintf(w, "  readiness_status: %s\n", report.LiveProvider.ReadinessStatus)
	}
	if report.LiveProvider.Remediation != "" {
		fmt.Fprintf(w, "  remediation: %s\n", report.LiveProvider.Remediation)
	}
	if report.LiveReadiness.ProviderMode != "" {
		fmt.Fprintln(w, "live_readiness:")
		fmt.Fprintf(w, "  provider_mode: %s\n", report.LiveReadiness.ProviderMode)
		fmt.Fprintf(w, "  credential_source: %s\n", report.LiveReadiness.CredentialSource)
		fmt.Fprintf(w, "  credential_present: %t\n", report.LiveReadiness.CredentialPresent)
		fmt.Fprintf(w, "  cache_path: %s\n", report.LiveReadiness.CachePath)
		if report.LiveReadiness.APIBaseURL != "" {
			fmt.Fprintf(w, "  api_base_url: %s\n", report.LiveReadiness.APIBaseURL)
		}
		if report.LiveReadiness.APIBaseURLSource != "" {
			fmt.Fprintf(w, "  api_base_url_source: %s\n", report.LiveReadiness.APIBaseURLSource)
		}
		fmt.Fprintf(w, "  readiness_status: %s\n", report.LiveReadiness.ReadinessStatus)
		if len(report.LiveReadiness.Diagnostics) > 0 {
			fmt.Fprintf(w, "  diagnostics: %s\n", strings.Join(report.LiveReadiness.Diagnostics, ","))
		}
	}
	fmt.Fprintln(w, "auth_probe:")
	fmt.Fprintf(w, "  status: %s\n", report.AuthProbe.Status)
	fmt.Fprintf(w, "  probe_result: %s\n", report.AuthProbe.ProbeResult)
	if report.AuthProbe.Remediation != "" {
		fmt.Fprintf(w, "  remediation: %s\n", report.AuthProbe.Remediation)
	}
	if len(report.Diagnostics) > 0 {
		fmt.Fprintf(w, "diagnostics: %s\n", strings.Join(report.Diagnostics, "; "))
	}
}
