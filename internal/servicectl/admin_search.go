package servicectl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/service"
)

type AdminSearchComparison struct {
	SchemaVersion string                      `json:"schema_version"`
	CacheRef      string                      `json:"cache_ref"`
	RepoID        string                      `json:"repo_id"`
	Query         string                      `json:"query"`
	FullText      service.SearchSourcesResult `json:"full_text"`
	Hybrid        service.SearchSourcesResult `json:"hybrid"`
	GeneratedAt   time.Time                   `json:"generated_at"`
}

type AdminProviderSmoke struct {
	Status       string `json:"status"`
	ProfileID    string `json:"profile_id,omitempty"`
	ProviderID   string `json:"provider_id,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	Model        string `json:"model,omitempty"`
	Revision     string `json:"revision,omitempty"`
	Dimensions   int    `json:"dimensions,omitempty"`
	FailureClass string `json:"failure_class,omitempty"`
	Message      string `json:"message,omitempty"`
	Handoff      string `json:"handoff,omitempty"`
}

type AdminRAGRepairPlan struct {
	SchemaVersion string                    `json:"schema_version"`
	PlanID        string                    `json:"plan_id"`
	Status        string                    `json:"status"`
	CacheRef      string                    `json:"cache_ref"`
	RepoID        string                    `json:"repo_id"`
	Profile       string                    `json:"profile,omitempty"`
	MaxChunks     int                       `json:"max_chunks"`
	Provider      AdminProviderSmoke        `json:"provider"`
	NamespaceID   string                    `json:"namespace_id,omitempty"`
	Coverage      service.SearchRAGCoverage `json:"coverage"`
	Effects       []MaintenancePlanAction   `json:"effects"`
	Blockers      []string                  `json:"blockers,omitempty"`
	CacheUUID     string                    `json:"-"`
}

func (m *AdminControlManager) CompareSearch(ctx context.Context, req adminhttp.SearchCompareRequest) (any, error) {
	path, err := m.resolveManagedCachePath(ctx, strings.TrimSpace(req.CacheRef))
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.Query)
	if query == "" || len([]rune(query)) > 512 {
		return nil, controlError(http.StatusBadRequest, "invalid_query", "Search query must contain 1 to 512 characters.", "Use one bounded experiment query.")
	}
	limit := req.Limit
	if limit == 0 {
		limit = 8
	}
	if limit < 1 || limit > 20 {
		return nil, controlError(http.StatusBadRequest, "invalid_request", "Search result limit must be between 1 and 20.", "Use a bounded comparison limit.")
	}
	effective, err := effectiveJobConfig(m.manager, path)
	if err != nil {
		return nil, adminControlError(err)
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		return nil, adminControlError(err)
	}
	defer store.Close()
	svc := service.New(store)
	svc.ConfigureRAGSearch(effective.Config)
	base := service.SearchSourcesRequest{RepoID: strings.TrimSpace(req.RepoID), Query: query, Kind: strings.TrimSpace(req.Kind), Provenance: strings.TrimSpace(req.Provenance), Limit: limit}
	fullRequest := base
	fullRequest.Mode = service.SearchModeFullText
	full, err := svc.SearchSources(ctx, fullRequest)
	if err != nil {
		return nil, adminControlError(err)
	}
	hybridRequest := base
	hybridRequest.Mode = service.SearchModeHybrid
	hybrid, err := svc.SearchSources(ctx, hybridRequest)
	if err != nil {
		return nil, adminControlError(err)
	}
	return AdminSearchComparison{SchemaVersion: "gitcode-mcp.admin-search-comparison.v1", CacheRef: strings.TrimSpace(req.CacheRef), RepoID: full.RepoID, Query: query, FullText: full, Hybrid: hybrid, GeneratedAt: time.Now().UTC()}, nil
}

func (m *AdminControlManager) SmokeProvider(ctx context.Context, req adminhttp.ProviderSmokeRequest) (any, error) {
	path, err := m.resolveManagedCachePath(ctx, strings.TrimSpace(req.CacheRef))
	if err != nil {
		return nil, err
	}
	effective, err := effectiveJobConfig(m.manager, path)
	if err != nil {
		return nil, adminControlError(err)
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		return nil, adminControlError(err)
	}
	defer store.Close()
	if _, err := store.GetRepository(ctx, strings.TrimSpace(req.RepoID)); err != nil {
		return nil, adminControlError(err)
	}
	return adminProviderSmoke(ctx, effective.Config, strings.TrimSpace(req.Profile)), nil
}

func (m *AdminControlManager) PlanRAGRepair(ctx context.Context, req adminhttp.RAGRepairRequest) (any, error) {
	plan, _, err := m.ragRepairPlan(ctx, req)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (m *AdminControlManager) ApplyRAGRepair(ctx context.Context, req adminhttp.RAGRepairRequest) (any, error) {
	if strings.TrimSpace(req.PlanID) == "" {
		return nil, controlError(http.StatusBadRequest, "invalid_request", "plan_id is required.", "Render and review a current bounded repair plan.")
	}
	intent := struct {
		CacheRef  string `json:"cache_ref"`
		RepoID    string `json:"repo_id"`
		PlanID    string `json:"plan_id"`
		MaxChunks int    `json:"max_chunks"`
	}{strings.TrimSpace(req.CacheRef), strings.TrimSpace(req.RepoID), strings.TrimSpace(req.PlanID), req.MaxChunks}
	return m.receipts.Apply(ctx, "rag_repair_apply", intent.CacheRef+"/"+intent.RepoID, req.IdempotencyKey, intent, func() (map[string]any, error) {
		plan, path, err := m.ragRepairPlan(ctx, req)
		if err != nil {
			return nil, err
		}
		if plan.PlanID != strings.TrimSpace(req.PlanID) {
			return nil, controlError(http.StatusConflict, "stale_plan", "The reviewed RAG repair plan no longer matches current coverage.", "Render and confirm a new bounded repair plan.")
		}
		if plan.Status == "blocked" {
			return map[string]any{"outcome": "blocked", "plan": plan}, nil
		}
		if plan.Status == "no_work_needed" {
			return map[string]any{"outcome": "no_work_needed", "plan_id": plan.PlanID}, nil
		}
		active, hadActive := m.jobs.ActiveCacheRepo(RAGIndexJobType, plan.CacheUUID, plan.RepoID)
		registrationID := m.registrationIDFor(plan.CacheRef, plan.RepoID)
		job, err := m.jobs.StartRAGIndex(context.Background(), m.manager, StartRAGIndexJobRequest{RepoID: plan.RepoID, Profile: plan.Profile, CachePath: path, CacheUUID: plan.CacheUUID, RegistrationID: registrationID, NamespaceID: plan.NamespaceID, MaxChunks: plan.MaxChunks})
		if err != nil {
			return nil, adminControlError(err)
		}
		outcome := "created"
		if hadActive && active.ID == job.ID {
			outcome = "coalesced"
		}
		return map[string]any{"outcome": outcome, "plan_id": plan.PlanID, "job_id": job.ID, "max_chunks": plan.MaxChunks}, nil
	})
}

func (m *AdminControlManager) ragRepairPlan(ctx context.Context, req adminhttp.RAGRepairRequest) (AdminRAGRepairPlan, string, error) {
	path, err := m.resolveManagedCachePath(ctx, strings.TrimSpace(req.CacheRef))
	if err != nil {
		return AdminRAGRepairPlan{}, "", err
	}
	maxChunks := req.MaxChunks
	if maxChunks == 0 {
		maxChunks = 128
	}
	if maxChunks < 1 || maxChunks > 1000 {
		return AdminRAGRepairPlan{}, "", controlError(http.StatusBadRequest, "invalid_request", "max_chunks must be between 1 and 1000.", "Choose one bounded repair slice.")
	}
	effective, err := effectiveJobConfig(m.manager, path)
	if err != nil {
		return AdminRAGRepairPlan{}, "", adminControlError(err)
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		return AdminRAGRepairPlan{}, "", adminControlError(err)
	}
	defer store.Close()
	if _, err := store.GetRepository(ctx, strings.TrimSpace(req.RepoID)); err != nil {
		return AdminRAGRepairPlan{}, "", adminControlError(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		return AdminRAGRepairPlan{}, "", adminControlError(err)
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		return AdminRAGRepairPlan{}, "", adminControlError(err)
	}
	profile := strings.TrimSpace(req.Profile)
	provider := adminProviderSmoke(ctx, effective.Config, profile)
	if profile == "" {
		profile = provider.ProfileID
	}
	plan := AdminRAGRepairPlan{SchemaVersion: "gitcode-mcp.admin-rag-repair-plan.v1", Status: "ready", CacheRef: strings.TrimSpace(req.CacheRef), RepoID: strings.TrimSpace(req.RepoID), Profile: profile, MaxChunks: maxChunks, Provider: provider, CacheUUID: identity.UUID}
	plan.Effects = []MaintenancePlanAction{
		{ID: "inspect-rag-coverage", Class: "inspect", Status: "complete", Summary: "inspect current chunk hashes, namespace, and generation coverage"},
		{ID: "enqueue-bounded-rag-repair", Class: "job_enqueue", Status: "required", Summary: fmt.Sprintf("enqueue at most %d missing or stale chunks", maxChunks), ConfirmationRequired: true},
		{ID: "embed-cached-text", Class: "provider_request", Status: "required", Summary: "send only the selected bounded cached-text slice to the configured embedding provider", DataBoundary: "configured_embedding_provider", ConfirmationRequired: true},
		{ID: "gitcode-network", Class: "network_read", Status: "not_performed", Summary: "no GitCode request is performed"},
	}
	if provider.Status != "ready" {
		plan.Status = "blocked"
		plan.Blockers = append(plan.Blockers, "embedding provider or model is not ready")
		plan.Effects[1].Status, plan.Effects[2].Status = "blocked", "blocked"
		plan.Effects[2].Handoff = provider.Handoff
	} else {
		ops := rag.NewOperations(store, effective.Config, rag.OperationsOptions{})
		status, statusErr := ops.Status(ctx, rag.StatusRequest{RepoID: plan.RepoID, ProfileID: profile})
		if statusErr != nil {
			plan.Status = "blocked"
			plan.Blockers = append(plan.Blockers, "RAG coverage could not be inspected")
			plan.Effects[1].Status, plan.Effects[2].Status = "blocked", "blocked"
		} else {
			plan.NamespaceID = status.Namespace.ID
			plan.Coverage = service.SearchRAGCoverage{EligibleChunks: status.Coverage.TotalChunks, EmbeddedChunks: status.Coverage.EmbeddedChunks, MissingChunks: status.Coverage.MissingChunks, StaleChunks: status.Coverage.StaleChunks, FailedChunks: status.Coverage.FailedChunks, NamespaceID: status.Namespace.ID, ContentGeneration: status.Coverage.ContentGeneration, CoveredGeneration: status.Coverage.CoveredGeneration}
			if plan.Coverage.EligibleChunks > 0 {
				plan.Coverage.Ratio = float64(plan.Coverage.EmbeddedChunks) / float64(plan.Coverage.EligibleChunks)
			}
			if plan.Coverage.EligibleChunks == 0 || (plan.Coverage.MissingChunks+plan.Coverage.StaleChunks+plan.Coverage.FailedChunks == 0 && status.Namespace.Current && (!status.Coverage.GenerationTracked || status.Coverage.CoveredGeneration >= status.Coverage.ContentGeneration)) {
				plan.Status = "no_work_needed"
				plan.Effects[1].Status, plan.Effects[2].Status = "complete", "not_performed"
			}
		}
	}
	plan.PlanID = ragRepairPlanID(plan, identity.UUID, version)
	return plan, path, nil
}

func adminProviderSmoke(ctx context.Context, cfg config.Config, profile string) AdminProviderSmoke {
	provider, err := rag.NewEmbeddingProviderFromConfig(cfg, profile, rag.ProviderOptions{})
	if err != nil {
		return AdminProviderSmoke{Status: "unavailable", FailureClass: adminProviderFailure(err), Message: "Embedding provider configuration is unavailable.", Handoff: "gitcode-mcp rag setup --yes"}
	}
	configured := provider.Profile()
	result := AdminProviderSmoke{Status: "ready", ProfileID: configured.ProfileID, ProviderID: configured.ProviderID, ProviderType: configured.ProviderType, Model: configured.Model, Dimensions: configured.Dimensions}
	info, err := provider.ModelInfo(ctx)
	if err == nil {
		result.Model = info.Model
		result.Revision = info.Revision
		return result
	}
	result.Status = "unavailable"
	result.FailureClass = adminProviderFailure(err)
	result.Handoff = "gitcode-mcp rag setup --yes"
	switch result.FailureClass {
	case rag.ProviderFailureModelMissing:
		result.Message = "The configured embedding model is not available."
	case rag.ProviderFailureTimeout:
		result.Message = "The embedding provider smoke test timed out."
	default:
		result.Message = "The configured embedding provider is unavailable."
	}
	return result
}

func ragRepairPlanID(plan AdminRAGRepairPlan, cacheUUID string, version int) string {
	return "rag-repair-plan-" + strings.TrimPrefix(maintenanceHash(struct {
		Schema      string                    `json:"schema"`
		CacheUUID   string                    `json:"cache_uuid"`
		Version     int                       `json:"schema_version"`
		RepoID      string                    `json:"repo_id"`
		Profile     string                    `json:"profile"`
		MaxChunks   int                       `json:"max_chunks"`
		Provider    AdminProviderSmoke        `json:"provider"`
		NamespaceID string                    `json:"namespace_id"`
		Coverage    service.SearchRAGCoverage `json:"coverage"`
	}{plan.SchemaVersion, cacheUUID, version, plan.RepoID, plan.Profile, plan.MaxChunks, plan.Provider, plan.NamespaceID, plan.Coverage}), "sha256:")
}

func (m *AdminControlManager) registrationIDFor(cacheRef, repoID string) string {
	for _, entry := range m.maintenance.adminEntries() {
		if entry.entry.RepoID == repoID && publicCacheRef(entry.entry.CacheUUID, entry.path) == cacheRef {
			return entry.entry.RegistrationID
		}
	}
	return ""
}

func adminProviderFailure(err error) string {
	var providerErr *rag.ProviderError
	if errors.As(err, &providerErr) && providerErr.Class != "" {
		return providerErr.Class
	}
	return "configuration_invalid"
}
