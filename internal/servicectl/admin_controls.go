package servicectl

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

type AdminBindingPlan struct {
	SchemaVersion string                    `json:"schema_version"`
	PlanID        string                    `json:"plan_id"`
	Status        string                    `json:"status"`
	CacheRef      string                    `json:"cache_ref"`
	RepoID        string                    `json:"repo_id"`
	Action        string                    `json:"action"`
	BeforeHash    string                    `json:"before_hash,omitempty"`
	AfterHash     string                    `json:"after_hash"`
	Binding       service.RepositoryBinding `json:"binding"`
	Effects       []MaintenancePlanAction   `json:"effects"`
	Blockers      []string                  `json:"blockers,omitempty"`
}

type AdminControlManager struct {
	manager     Manager
	maintenance *MaintenanceManager
	receipts    *AdminControlReceiptManager
}

func NewAdminControlManager(manager Manager, maintenance *MaintenanceManager, receipts *AdminControlReceiptManager) *AdminControlManager {
	return &AdminControlManager{manager: manager, maintenance: maintenance, receipts: receipts}
}

func (m *AdminControlManager) PlanMaintenance(ctx context.Context, req adminhttp.MaintenanceControlRequest) (any, error) {
	setup, mapped, err := m.maintenanceSetup(ctx, req)
	if err != nil {
		return nil, err
	}
	plan, err := setup.Plan(ctx, mapped)
	if err != nil {
		return nil, adminControlError(err)
	}
	return plan, nil
}

func (m *AdminControlManager) ApplyMaintenance(ctx context.Context, req adminhttp.MaintenanceControlRequest) (any, error) {
	if strings.TrimSpace(req.PlanID) == "" {
		return nil, controlError(http.StatusBadRequest, "invalid_request", "plan_id is required.", "Render and review a current plan before apply.")
	}
	setup, mapped, err := m.maintenanceSetup(ctx, req)
	if err != nil {
		return nil, err
	}
	mapped.PlanID = strings.TrimSpace(req.PlanID)
	mapped.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	mapped.Confirmed = true
	// Browser authority never extends to service installation, provider startup,
	// model downloads, migration, or other machine-level effects.
	mapped.AllowMachineChange = false
	result, err := setup.Apply(ctx, mapped)
	if err != nil {
		return nil, adminControlError(err)
	}
	return result, nil
}

func (m *AdminControlManager) DisableMaintenance(ctx context.Context, req adminhttp.RegistrationControlRequest) (any, error) {
	if m.maintenance == nil {
		return nil, controlError(http.StatusNotImplemented, "capability_unavailable", "Maintenance controls are unavailable.", "Use the CLI maintenance surface.")
	}
	return m.receipts.Apply(ctx, "maintenance_disable", req.RegistrationID, req.IdempotencyKey, struct {
		RegistrationID string `json:"registration_id"`
	}{req.RegistrationID}, func() (map[string]any, error) {
		entry, err := m.maintenance.Disable(ctx, req.RegistrationID)
		if err != nil {
			return nil, adminControlError(err)
		}
		return map[string]any{"outcome": "disabled", "registration": adminMaintenanceObservation(entry)}, nil
	})
}

func (m *AdminControlManager) ReconcileMaintenance(ctx context.Context, req adminhttp.RegistrationControlRequest) (any, error) {
	if m.maintenance == nil {
		return nil, controlError(http.StatusNotImplemented, "capability_unavailable", "Maintenance controls are unavailable.", "Use the CLI maintenance surface.")
	}
	return m.receipts.Apply(ctx, "maintenance_reconcile", req.RegistrationID, req.IdempotencyKey, struct {
		RegistrationID string `json:"registration_id"`
	}{req.RegistrationID}, func() (map[string]any, error) {
		result, err := m.maintenance.ReconcileRegistration(ctx, req.RegistrationID)
		if err != nil {
			return nil, adminControlError(err)
		}
		return map[string]any{"outcome": maintenanceReconcileOutcome(result), "jobs_started": result.JobsStarted, "checked_at": result.CheckedAt}, nil
	})
}

func (m *AdminControlManager) PlanBinding(ctx context.Context, req adminhttp.BindingControlRequest) (any, error) {
	plan, _, err := m.bindingPlan(ctx, req)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (m *AdminControlManager) ApplyBinding(ctx context.Context, req adminhttp.BindingControlRequest) (any, error) {
	if strings.TrimSpace(req.PlanID) == "" {
		return nil, controlError(http.StatusBadRequest, "invalid_request", "plan_id is required.", "Render and review a current binding plan.")
	}
	intent := struct {
		CacheRef string `json:"cache_ref"`
		RepoID   string `json:"repo_id"`
		PlanID   string `json:"plan_id"`
	}{strings.TrimSpace(req.CacheRef), strings.TrimSpace(req.RepoID), strings.TrimSpace(req.PlanID)}
	return m.receipts.Apply(ctx, "binding_apply", intent.CacheRef+"/"+intent.RepoID, req.IdempotencyKey, intent, func() (map[string]any, error) {
		plan, path, err := m.bindingPlan(ctx, req)
		if err != nil {
			return nil, err
		}
		if req.PlanID != plan.PlanID {
			return nil, controlError(http.StatusConflict, "stale_plan", "The reviewed binding plan no longer matches current cache state.", "Render and confirm a new binding plan.")
		}
		if plan.Status == "blocked" {
			return map[string]any{"outcome": "blocked", "plan": plan}, nil
		}
		if plan.Action == "no_op" {
			return map[string]any{"outcome": "already_applied", "plan_id": plan.PlanID, "binding": plan.Binding}, nil
		}
		store, err := cache.NewSQLiteStore(ctx, path)
		if err != nil {
			return nil, adminControlError(err)
		}
		defer store.Close()
		svc := service.New(store)
		request := service.AddRepositoryRequest{RepoID: plan.Binding.RepoID, Owner: plan.Binding.Owner, Name: plan.Binding.Name, APIBaseURL: plan.Binding.APIBaseURL, DisplayName: plan.Binding.DisplayName, Aliases: plan.Binding.Aliases}
		for _, scope := range plan.Binding.Scopes {
			request.Scopes = append(request.Scopes, string(scope))
		}
		var binding service.RepositoryBinding
		if plan.Action == "add" {
			binding, err = svc.AddRepository(ctx, request)
		} else {
			binding, err = svc.UpdateRepository(ctx, request)
		}
		if err != nil {
			return nil, adminControlError(err)
		}
		outcome := "updated"
		if plan.Action == "add" {
			outcome = "added"
		}
		return map[string]any{"outcome": outcome, "plan_id": plan.PlanID, "binding": binding}, nil
	})
}

func (m *AdminControlManager) bindingPlan(ctx context.Context, req adminhttp.BindingControlRequest) (AdminBindingPlan, string, error) {
	path, err := m.resolveManagedCachePath(ctx, strings.TrimSpace(req.CacheRef))
	if err != nil {
		return AdminBindingPlan{}, "", err
	}
	repoID := strings.Trim(strings.TrimSpace(req.RepoID), "/")
	owner, name := strings.TrimSpace(req.Owner), strings.TrimSpace(req.Name)
	parts := strings.Split(repoID, "/")
	if owner == "" && len(parts) == 2 {
		owner = parts[0]
	}
	if name == "" && len(parts) == 2 {
		name = parts[1]
	}
	effective, err := effectiveJobConfig(m.manager, path)
	if err != nil {
		return AdminBindingPlan{}, "", adminControlError(err)
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		return AdminBindingPlan{}, "", adminControlError(err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		return AdminBindingPlan{}, "", adminControlError(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		return AdminBindingPlan{}, "", adminControlError(err)
	}
	repositories, err := store.ListRepositories(ctx)
	if err != nil {
		return AdminBindingPlan{}, "", adminControlError(err)
	}
	var current *cache.RepositoryBinding
	for index := range repositories {
		if repositories[index].RepoID == repoID {
			copy := repositories[index]
			current = &copy
			break
		}
	}
	apiURL := strings.TrimSpace(req.APIBaseURL)
	scopes := append([]string(nil), req.Scopes...)
	aliases := append([]string(nil), req.Aliases...)
	displayName := ""
	if req.DisplayName != nil {
		displayName = *req.DisplayName
	}
	if current != nil {
		if owner == "" {
			owner = current.Owner
		}
		if name == "" {
			name = current.Name
		}
		if apiURL == "" {
			apiURL = current.APIBaseURL
		}
		if req.Scopes == nil {
			for _, scope := range current.Scopes {
				scopes = append(scopes, string(scope))
			}
		}
		if req.Aliases == nil {
			aliases = append(aliases, current.Aliases...)
		}
		if req.DisplayName == nil {
			displayName = current.DisplayName
		}
	}
	if apiURL == "" {
		apiURL = strings.TrimSpace(effective.Config.GitCodeBaseURL)
	}
	desired, err := service.ValidateRepositoryBinding(service.AddRepositoryRequest{RepoID: repoID, Owner: owner, Name: name, APIBaseURL: apiURL, Scopes: scopes, Aliases: aliases, DisplayName: displayName}, time.Unix(0, 0).UTC())
	if err != nil {
		return AdminBindingPlan{}, "", controlError(http.StatusBadRequest, "invalid_binding", "The repository binding is not valid.", "Use owner/repository, supported scopes, unique aliases, and a valid API URL.")
	}
	plan := AdminBindingPlan{SchemaVersion: "gitcode-mcp.binding-plan.v1", Status: "ready", CacheRef: publicCacheRef(identity.UUID, path), RepoID: desired.RepoID, Action: "add", Binding: desired}
	plan.Effects = []MaintenancePlanAction{{ID: "validate-binding", Class: "inspect", Status: "complete", Summary: "validate repository identity, API route, scopes, and aliases"}, {ID: "write-binding", Class: "cache_write", Status: "required", Summary: "atomically write repository binding metadata", ConfirmationRequired: true}, {ID: "gitcode-network", Class: "network_read", Status: "not_performed", Summary: "no GitCode request is performed while planning or applying the binding"}}
	if version != cache.CurrentSchemaVersion() {
		plan.Status = "blocked"
		plan.Blockers = append(plan.Blockers, "cache schema must be migrated before binding changes")
		plan.Effects[1].Status = "blocked"
		plan.Effects[1].Handoff = "gitcode-mcp migrate-cache --confirm"
	}
	for _, existing := range repositories {
		if existing.RepoID == desired.RepoID {
			plan.Action = "update"
			plan.BeforeHash = maintenanceHash(existing)
			if bindingEquivalent(existing, desired) {
				plan.Action = "no_op"
				plan.Effects[1].Status = "complete"
				plan.Effects[1].Summary = "binding already matches the requested intent"
			}
			continue
		}
		for _, existingAlias := range existing.Aliases {
			if desired.RepoID == existingAlias {
				plan.Status = "blocked"
				plan.Blockers = append(plan.Blockers, "repository id "+desired.RepoID+" is an alias of another repository binding")
			}
		}
		for _, alias := range desired.Aliases {
			if alias == existing.RepoID {
				plan.Status = "blocked"
				plan.Blockers = append(plan.Blockers, "alias "+alias+" belongs to another repository binding")
			}
			for _, existingAlias := range existing.Aliases {
				if alias == existingAlias {
					plan.Status = "blocked"
					plan.Blockers = append(plan.Blockers, "alias "+alias+" belongs to another repository binding")
				}
			}
		}
	}
	if len(plan.Blockers) > 0 {
		plan.Effects[1].Status = "blocked"
	}
	plan.AfterHash = maintenanceHash(desired)
	plan.PlanID = "binding-plan-" + strings.TrimPrefix(maintenanceHash(struct {
		Schema     string `json:"schema"`
		CacheUUID  string `json:"cache_uuid"`
		Version    int    `json:"schema_version"`
		BeforeHash string `json:"before_hash"`
		AfterHash  string `json:"after_hash"`
	}{Schema: plan.SchemaVersion, CacheUUID: identity.UUID, Version: version, BeforeHash: plan.BeforeHash, AfterHash: plan.AfterHash}), "sha256:")
	return plan, path, nil
}

func bindingEquivalent(existing cache.RepositoryBinding, desired service.RepositoryBinding) bool {
	if existing.RepoID != desired.RepoID || existing.Owner != desired.Owner || existing.Name != desired.Name || existing.APIBaseURL != desired.APIBaseURL || existing.DisplayName != desired.DisplayName {
		return false
	}
	existingScopes := make([]string, 0, len(existing.Scopes))
	for _, scope := range existing.Scopes {
		existingScopes = append(existingScopes, string(scope))
	}
	desiredScopes := make([]string, 0, len(desired.Scopes))
	for _, scope := range desired.Scopes {
		desiredScopes = append(desiredScopes, string(scope))
	}
	sort.Strings(existingScopes)
	sort.Strings(desiredScopes)
	existingAliases := append([]string(nil), existing.Aliases...)
	desiredAliases := append([]string(nil), desired.Aliases...)
	sort.Strings(existingAliases)
	sort.Strings(desiredAliases)
	return strings.Join(existingScopes, "\x00") == strings.Join(desiredScopes, "\x00") && strings.Join(existingAliases, "\x00") == strings.Join(desiredAliases, "\x00")
}

func (m *AdminControlManager) maintenanceSetup(ctx context.Context, req adminhttp.MaintenanceControlRequest) (MaintenanceSetup, MaintenanceSetupRequest, error) {
	if err := validateAdminMaintenanceRequest(req); err != nil {
		return MaintenanceSetup{}, MaintenanceSetupRequest{}, err
	}
	path, err := m.resolveManagedCachePath(ctx, strings.TrimSpace(req.CacheRef))
	if err != nil {
		return MaintenanceSetup{}, MaintenanceSetupRequest{}, err
	}
	effective, err := effectiveJobConfig(m.manager, path)
	if err != nil {
		return MaintenanceSetup{}, MaintenanceSetupRequest{}, adminControlError(err)
	}
	mapped := MaintenanceSetupRequest{
		RepoID: req.RepoID, Profile: req.Profile, SyncMode: req.SyncMode, Collections: req.Collections, RAGMode: req.RAGMode,
		HeadIntervalSeconds: req.HeadIntervalSeconds, RAGIntervalSeconds: req.RAGIntervalSeconds,
		HeadMaxPages: req.HeadMaxPages, TailSlicePages: req.TailSlicePages, PerPage: req.PerPage,
		NoServiceInstall: true, NoModelDownload: true, Detach: true,
	}
	setup := MaintenanceSetup{Manager: m.manager, Config: effective.Config, CachePath: path, CachePathSource: "daemon-managed", StartupTimeout: effective.Config.DefaultTimeout}
	return setup, mapped, nil
}

func (m *AdminControlManager) resolveManagedCachePath(ctx context.Context, cacheRef string) (string, error) {
	if cacheRef == "" {
		return "", controlError(http.StatusBadRequest, "invalid_request", "cache_ref is required.", "Select one cache exposed by the admin snapshot.")
	}
	entries := m.maintenance.adminEntries()
	groups := groupAdminCaches(strings.TrimSpace(m.manager.AdminCachePath), entries)
	matches := []string{}
	for _, group := range groups {
		uuid := group.uuid
		if store, err := cache.NewSQLiteReadOnlyStore(ctx, group.path); err == nil {
			if identity, identityErr := store.CacheIdentity(ctx); identityErr == nil && identity.UUID != "" {
				uuid = identity.UUID
			}
			store.Close()
		}
		if publicCacheRef(uuid, group.path) == cacheRef {
			matches = append(matches, group.path)
		}
	}
	if len(matches) == 0 {
		return "", controlError(http.StatusNotFound, "cache_not_found", "The selected managed cache is no longer available.", "Refresh the admin snapshot and select a listed cache.")
	}
	if len(matches) > 1 {
		return "", controlError(http.StatusConflict, "ambiguous_cache", "The cache reference resolves to multiple managed locations.", "Run service doctor and resolve cloned cache identity.")
	}
	return matches[0], nil
}

func validateAdminMaintenanceRequest(req adminhttp.MaintenanceControlRequest) error {
	if strings.TrimSpace(req.RepoID) == "" {
		return controlError(http.StatusBadRequest, "invalid_request", "repo_id is required.", "Select a repository in the managed cache.")
	}
	for _, bound := range []struct {
		name  string
		value int
		max   int
	}{{"head_interval_seconds", req.HeadIntervalSeconds, 86400 * 30}, {"rag_interval_seconds", req.RAGIntervalSeconds, 86400 * 30}, {"head_max_pages", req.HeadMaxPages, 1000}, {"tail_slice_pages", req.TailSlicePages, 1000}, {"per_page", req.PerPage, 100}} {
		if bound.value < 0 || bound.value > bound.max {
			return controlError(http.StatusBadRequest, "invalid_request", bound.name+" is outside the supported bound.", "Use a non-negative bounded policy value.")
		}
	}
	return nil
}

func maintenanceReconcileOutcome(result MaintenanceReconcileResult) string {
	if len(result.JobsStarted) > 0 {
		return "created"
	}
	return "no_work_needed"
}

func adminControlError(err error) error {
	var stale StaleMaintenancePlanError
	if errors.As(err, &stale) {
		return controlError(http.StatusConflict, "stale_plan", "The reviewed plan no longer matches current state.", "Render and confirm a new plan.")
	}
	if coded, ok := err.(interface{ DiagnosticCode() string }); ok {
		code := coded.DiagnosticCode()
		return controlError(http.StatusConflict, code, "The requested control was rejected by current state.", "Refresh diagnostics and render a new plan.")
	}
	var conflict service.ErrConflict
	if errors.As(err, &conflict) {
		return controlError(http.StatusConflict, "binding_conflict", "The repository binding conflicts with current cache state.", "Refresh bindings, resolve repository ids or aliases, and render a new plan.")
	}
	var aliasConflict cache.ErrAliasConflict
	if errors.As(err, &aliasConflict) {
		return controlError(http.StatusConflict, "binding_conflict", "The repository alias conflicts with current cache state.", "Refresh bindings, choose unique aliases, and render a new plan.")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "not found") || strings.Contains(message, "not bound") {
		return controlError(http.StatusNotFound, "target_not_found", "The selected binding or registration is unavailable.", "Refresh managed caches and bindings.")
	}
	return controlError(http.StatusBadRequest, "invalid_request", "The requested policy cannot be planned safely.", "Correct the policy or follow the returned CLI handoff.")
}

func controlError(status int, code, message, remediation string) error {
	return adminhttp.ControlError{Status: status, Code: code, Message: message, Remediation: remediation}
}
