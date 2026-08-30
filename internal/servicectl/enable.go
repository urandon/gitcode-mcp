package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/rag"
)

const MaintenancePlanSchema = "gitcode-mcp.maintenance-plan.v1"

type MaintenanceSetupRequest struct {
	RepoID              string   `json:"repo_id"`
	Profile             string   `json:"profile,omitempty"`
	SyncMode            string   `json:"sync,omitempty"`
	Collections         []string `json:"collections,omitempty"`
	RAGMode             string   `json:"rag,omitempty"`
	HeadIntervalSeconds int      `json:"head_interval_seconds,omitempty"`
	RAGIntervalSeconds  int      `json:"rag_interval_seconds,omitempty"`
	HeadMaxPages        int      `json:"head_max_pages,omitempty"`
	TailSlicePages      int      `json:"tail_slice_pages,omitempty"`
	PerPage             int      `json:"per_page,omitempty"`
	NoServiceInstall    bool     `json:"no_service_install,omitempty"`
	NoModelDownload     bool     `json:"no_model_download,omitempty"`
	Detach              bool     `json:"detach,omitempty"`
	IdempotencyKey      string   `json:"idempotency_key,omitempty"`
	PlanID              string   `json:"plan_id,omitempty"`
	Confirmed           bool     `json:"-"`
	AllowMachineChange  bool     `json:"-"`
}

type MaintenancePlanAction struct {
	ID                   string `json:"id"`
	Class                string `json:"class"`
	Status               string `json:"status"`
	Summary              string `json:"summary"`
	DataBoundary         string `json:"data_boundary,omitempty"`
	ConfirmationRequired bool   `json:"confirmation_required,omitempty"`
	Handoff              string `json:"handoff,omitempty"`
}

type MaintenanceCachePlan struct {
	Ref               string   `json:"cache_ref"`
	UUID              string   `json:"cache_uuid"`
	PathFingerprint   string   `json:"path_fingerprint"`
	LocationKind      string   `json:"location_kind"`
	SchemaVersion     int      `json:"schema_version"`
	RepositoryBinding string   `json:"repository_binding_hash"`
	Scopes            []string `json:"scopes"`
}

type MaintenanceProviderPlan struct {
	Profile              string `json:"profile"`
	Provider             string `json:"provider"`
	ProviderType         string `json:"provider_type"`
	Model                string `json:"model"`
	ModelRevision        string `json:"model_revision"`
	ConfigurationHash    string `json:"configuration_hash"`
	DataBoundary         string `json:"data_boundary"`
	Installed            bool   `json:"installed"`
	Running              bool   `json:"running"`
	ModelAvailable       bool   `json:"model_available"`
	EmbeddingSmokeStatus string `json:"embedding_smoke_status"`
}

type MaintenancePlan struct {
	SchemaVersion     string                  `json:"schema_version"`
	PlanID            string                  `json:"plan_id"`
	ConfigurationHash string                  `json:"configuration_hash"`
	Status            string                  `json:"status"`
	RepoID            string                  `json:"repo_id"`
	Cache             MaintenanceCachePlan    `json:"cache"`
	DaemonProtocol    string                  `json:"daemon_protocol"`
	DaemonVersion     string                  `json:"daemon_version,omitempty"`
	Service           Status                  `json:"service"`
	Provider          MaintenanceProviderPlan `json:"provider"`
	Policy            MaintenancePolicy       `json:"policy"`
	Actions           []MaintenancePlanAction `json:"actions"`
	Blockers          []string                `json:"blockers,omitempty"`
	NextAction        string                  `json:"next_action,omitempty"`
}

type MaintenanceApplyResult struct {
	Status          string                  `json:"status"`
	PlanID          string                  `json:"plan_id"`
	RepoID          string                  `json:"repo_id"`
	Registration    *MaintenanceEntry       `json:"registration,omitempty"`
	JobsStarted     []string                `json:"jobs_started,omitempty"`
	CompletedStages []string                `json:"completed_stages,omitempty"`
	PendingActions  []MaintenancePlanAction `json:"pending_actions,omitempty"`
	AuditReceipt    string                  `json:"audit_receipt,omitempty"`
	FailureClass    string                  `json:"failure_class,omitempty"`
	Message         string                  `json:"message,omitempty"`
	NextAction      string                  `json:"next_action,omitempty"`
}

type StaleMaintenancePlanError struct {
	Expected string
	Actual   string
}

func (e StaleMaintenancePlanError) Error() string {
	return fmt.Sprintf("maintenance: stale plan: expected %s, current %s", e.Expected, e.Actual)
}

func (e StaleMaintenancePlanError) DiagnosticCode() string { return "stale_plan" }

type MaintenanceSetup struct {
	Manager         Manager
	Config          config.Config
	CachePath       string
	CachePathSource string
	RAGRuntime      rag.Runtime
	Client          func() (*RPCClient, error)
	ConfigReference string
	StartupTimeout  time.Duration
}

type MaintenanceServiceReadinessError struct{}

func (MaintenanceServiceReadinessError) Error() string {
	return "maintenance: service did not expose the required maintenance protocol before the readiness deadline; run gitcode-mcp service repair"
}

func (MaintenanceServiceReadinessError) DiagnosticCode() string { return "service_not_ready" }

func (s MaintenanceSetup) Plan(ctx context.Context, req MaintenanceSetupRequest) (MaintenancePlan, error) {
	req, err := normalizeMaintenanceSetupRequest(req)
	if err != nil {
		return MaintenancePlan{}, err
	}
	configSnapshot := s.Config
	configSnapshot.CachePath = s.CachePath
	plan := MaintenancePlan{SchemaVersion: MaintenancePlanSchema, RepoID: req.RepoID, DaemonProtocol: maintenanceRegistrySchema, ConfigurationHash: maintenanceHash(configSnapshot)}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, s.CachePath)
	if err != nil {
		return MaintenancePlan{}, fmt.Errorf("maintenance plan: selected cache is unavailable: %w", err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		return MaintenancePlan{}, err
	}
	binding, err := maintenanceBinding(ctx, store, req.RepoID)
	if err != nil {
		return MaintenancePlan{}, err
	}
	policy, err := setupMaintenancePolicy(req, binding)
	if err != nil {
		return MaintenancePlan{}, err
	}
	canonicalPath, err := canonicalCachePath(s.CachePath)
	if err != nil {
		return MaintenancePlan{}, err
	}
	if version != cache.CurrentSchemaVersion() {
		plan.Policy = policy
		plan.Cache = MaintenanceCachePlan{Ref: "unavailable", PathFingerprint: pathFingerprint(canonicalPath), LocationKind: maintenanceLocationKind(s.CachePathSource), SchemaVersion: version, RepositoryBinding: maintenanceHash(binding), Scopes: maintenanceScopes(binding)}
		plan.Status = "blocked"
		plan.Blockers = []string{fmt.Sprintf("cache schema %d must be migrated to %d", version, cache.CurrentSchemaVersion())}
		plan.Actions = []MaintenancePlanAction{{ID: "migrate-cache", Class: "cache_write", Status: "blocked", Summary: "migrate the selected cache before daemon enrollment", ConfirmationRequired: true, Handoff: "gitcode-mcp migrate-cache --confirm"}}
		plan.NextAction = plan.Actions[0].Handoff
		plan.PlanID = maintenancePlanID(plan)
		return plan, nil
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		return MaintenancePlan{}, err
	}
	plan.Policy = policy
	plan.Cache = MaintenanceCachePlan{
		Ref: maintenanceRegistrationID(identity.UUID, req.RepoID), UUID: identity.UUID,
		PathFingerprint: pathFingerprint(canonicalPath), LocationKind: maintenanceLocationKind(s.CachePathSource),
		SchemaVersion: version, RepositoryBinding: maintenanceHash(binding), Scopes: maintenanceScopes(binding),
	}
	plan.Actions = append(plan.Actions, MaintenancePlanAction{ID: "validate-cache", Class: "inspect", Status: "complete", Summary: "validate cache identity, schema, and repository binding"})
	serviceStatus, statusErr := s.Manager.Status()
	if statusErr != nil {
		plan.Blockers = append(plan.Blockers, "service status is unavailable")
	} else {
		plan.Service = sanitizeMaintenanceServiceStatus(serviceStatus)
		if !serviceStatus.Installed && !serviceStatus.Running {
			action := MaintenancePlanAction{ID: "install-service", Class: "local_service_change", Status: "required", Summary: "install the per-user gitcode-mcp service", ConfirmationRequired: true, Handoff: "gitcode-mcp service install"}
			if req.NoServiceInstall {
				action.Status = "blocked"
				plan.Blockers = append(plan.Blockers, "service installation is disabled by policy")
			}
			plan.Actions = append(plan.Actions, action)
		}
		if !serviceStatus.Running {
			plan.Actions = append(plan.Actions, MaintenancePlanAction{ID: "start-service", Class: "local_service_change", Status: "required", Summary: "start the per-user gitcode-mcp service", ConfirmationRequired: true, Handoff: "gitcode-mcp service start"})
		} else {
			client, clientErr := s.rpcClient()
			var capabilities MaintenanceCapabilities
			if clientErr != nil || client.Call(ctx, "Maintenance.Capabilities", nil, &capabilities) != nil {
				plan.Blockers = append(plan.Blockers, "running daemon does not expose the required maintenance protocol; reinstall or restart it with the current binary")
				plan.Actions = append(plan.Actions, MaintenancePlanAction{ID: "upgrade-service", Class: "local_service_change", Status: "blocked", Summary: "repair and reload the daemon with the current binary", ConfirmationRequired: true, Handoff: "gitcode-mcp service repair"})
			} else if capabilities.RegistryProtocol != maintenanceRegistrySchema {
				plan.Blockers = append(plan.Blockers, fmt.Sprintf("daemon registry protocol %q is incompatible with %q", capabilities.RegistryProtocol, maintenanceRegistrySchema))
			} else {
				plan.DaemonProtocol = capabilities.RegistryProtocol
				plan.DaemonVersion = capabilities.BinaryVersion
				plan.Actions = append(plan.Actions, MaintenancePlanAction{ID: "validate-daemon-protocol", Class: "inspect", Status: "complete", Summary: "validate daemon maintenance protocol and capabilities"})
			}
		}
	}
	providerPlan, providerActions, providerBlockers, err := s.providerPlan(ctx, req)
	if err != nil {
		return MaintenancePlan{}, err
	}
	plan.Provider = providerPlan
	plan.Actions = append(plan.Actions, providerActions...)
	plan.Blockers = append(plan.Blockers, providerBlockers...)
	plan.Actions = append(plan.Actions,
		MaintenancePlanAction{ID: "enroll-cache", Class: "local_config_write", Status: "required", Summary: "enroll the validated cache and maintenance policy"},
		MaintenancePlanAction{ID: "enqueue-initial-maintenance", Class: "job_enqueue", Status: "required", Summary: "coalesce initial head, backfill, and RAG maintenance work"},
	)
	plan.Status = "ready"
	if len(plan.Blockers) > 0 {
		plan.Status = "blocked"
	} else if maintenancePlanNeedsConfirmation(plan.Actions) {
		plan.Status = "confirmation_required"
	}
	plan.NextAction = maintenancePlanNextAction(plan)
	plan.PlanID = maintenancePlanID(plan)
	return plan, nil
}

func (s MaintenanceSetup) Apply(ctx context.Context, req MaintenanceSetupRequest) (MaintenanceApplyResult, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return MaintenanceApplyResult{}, MaintenanceSetupInputError{Field: "idempotency_key", Message: "is required"}
	}
	current, err := s.Plan(ctx, req)
	if err != nil {
		return MaintenanceApplyResult{}, err
	}
	if strings.TrimSpace(req.PlanID) != "" && req.PlanID != current.PlanID {
		return MaintenanceApplyResult{}, StaleMaintenancePlanError{Expected: req.PlanID, Actual: current.PlanID}
	}
	result := MaintenanceApplyResult{Status: current.Status, PlanID: current.PlanID, RepoID: req.RepoID}
	if current.Status == "blocked" {
		result.PendingActions = pendingMaintenanceActions(current.Actions)
		result.NextAction = firstMaintenanceHandoff(result.PendingActions)
		if result.NextAction == "run the CLI maintenance enable flow to confirm machine-level changes" {
			result.NextAction = current.NextAction
		}
		return result, nil
	}
	if !req.Confirmed {
		result.Status = "confirmation_required"
		result.PendingActions = pendingMaintenanceActions(current.Actions)
		result.NextAction = "review the rendered plan, then apply it with explicit confirmation"
		return result, nil
	}
	if maintenancePlanNeedsMachineChange(current.Actions) && !req.AllowMachineChange {
		result.Status = "confirmation_required"
		result.PendingActions = machineChangeActions(current.Actions)
		result.NextAction = firstMaintenanceHandoff(result.PendingActions)
		return result, nil
	}
	if actionRequired(current.Actions, "install-service") {
		if _, err := s.Manager.Install(false); err != nil {
			return MaintenanceApplyResult{}, err
		}
		result.CompletedStages = append(result.CompletedStages, "service_installed")
	}
	if actionRequired(current.Actions, "start-service") {
		if _, err := s.Manager.Start(ctx); err != nil {
			return MaintenanceApplyResult{}, err
		}
		result.CompletedStages = append(result.CompletedStages, "service_started")
	}
	client, capabilities, err := s.waitForMaintenanceDaemon(ctx)
	if err != nil {
		return MaintenanceApplyResult{}, err
	}
	if capabilities.RegistryProtocol != maintenanceRegistrySchema {
		return MaintenanceApplyResult{}, MaintenanceServiceReadinessError{}
	}
	if current.Policy.RAGEnabled {
		setup, err := rag.Setup(ctx, rag.SetupRequest{Config: s.Config, Profile: req.Profile, Yes: !req.NoModelDownload, Runtime: s.RAGRuntime})
		if err != nil {
			return MaintenanceApplyResult{}, err
		}
		if setup.Status != "ready" {
			result.Status = "blocked"
			result.FailureClass = setup.Status
			result.Message = maintenanceProviderFailureMessage(setup.Status)
			result.NextAction = firstString(setup.Actions)
			if result.NextAction == "" {
				result.NextAction = firstString(setup.InstallInstructions)
			}
			if result.NextAction == "" {
				result.NextAction = "gitcode-mcp rag setup --yes"
			}
			return result, nil
		}
		result.CompletedStages = append(result.CompletedStages, "provider_model_ready", "provider_smoke_verified")
	}
	configSnapshot := s.Config
	configSnapshot.CachePath = s.CachePath
	configHash := current.ConfigurationHash
	var resolved MaintenanceResolveConfigResult
	if err := client.Call(ctx, "Maintenance.ResolveConfig", MaintenanceResolveConfigRequest{CachePath: s.CachePath, Profile: current.Policy.Profile, RAGEnabled: current.Policy.RAGEnabled, ConfigSnapshot: configSnapshot}, &resolved); err != nil {
		return MaintenanceApplyResult{}, err
	}
	if resolved.ConfigHash != configHash {
		return MaintenanceApplyResult{}, errors.New("maintenance: daemon resolved a different configuration snapshot")
	}
	result.CompletedStages = append(result.CompletedStages, "daemon_config_verified")
	var entry MaintenanceEntry
	if err := client.Call(ctx, "Maintenance.Enroll", MaintenanceEnrollRequest{CachePath: s.CachePath, RepoID: req.RepoID, Policy: current.Policy, IdempotencyKey: req.IdempotencyKey, ConfigReference: s.ConfigReference, ConfigHash: configHash, ConfigSnapshot: configSnapshot}, &entry); err != nil {
		return MaintenanceApplyResult{}, err
	}
	result.Registration = &entry
	result.AuditReceipt = maintenanceHash(struct {
		Plan string `json:"plan"`
		Key  string `json:"key"`
	}{current.PlanID, req.IdempotencyKey})
	result.CompletedStages = append(result.CompletedStages, "cache_enrolled", "policy_stored")
	var reconcile MaintenanceReconcileResult
	if err := client.Call(ctx, "Maintenance.ReconcileRegistration", MaintenanceRegistrationRequest{RegistrationID: entry.RegistrationID}, &reconcile); err != nil {
		return MaintenanceApplyResult{}, err
	}
	result.JobsStarted = append([]string(nil), reconcile.JobsStarted...)
	if len(reconcile.JobsStarted) > 0 {
		result.CompletedStages = append(result.CompletedStages, "jobs_enqueued")
	} else {
		result.CompletedStages = append(result.CompletedStages, "maintenance_reconciled")
	}
	result.Status = maintenanceApplyStatus(entry, reconcile)
	result.NextAction = maintenanceApplyNextAction(result.Status, req.Detach)
	return result, nil
}

type MaintenanceSetupInputError struct {
	Field   string
	Message string
}

func (e MaintenanceSetupInputError) Error() string {
	return fmt.Sprintf("maintenance: invalid query %s: %s", e.Field, e.Message)
}

func (MaintenanceSetupInputError) DiagnosticCode() string { return "invalid_query" }

func (s MaintenanceSetup) providerPlan(ctx context.Context, req MaintenanceSetupRequest) (MaintenanceProviderPlan, []MaintenancePlanAction, []string, error) {
	if req.RAGMode == "off" {
		return MaintenanceProviderPlan{DataBoundary: "unknown", EmbeddingSmokeStatus: "skipped"}, nil, nil, nil
	}
	setup, err := rag.Setup(ctx, rag.SetupRequest{Config: s.Config, Profile: req.Profile, DryRun: true, Runtime: s.RAGRuntime})
	if err != nil {
		return MaintenanceProviderPlan{}, nil, nil, err
	}
	profile := s.Config.RAG.Profiles[setup.Profile]
	provider := s.Config.RAG.Providers[setup.Provider]
	boundary := strings.TrimSpace(provider.DataBoundary)
	if boundary == "" {
		boundary = "unknown"
	}
	configurationHash := maintenanceHash(struct {
		Profile  config.RAGProfileConfig  `json:"profile"`
		Provider config.RAGProviderConfig `json:"provider"`
	}{profile, provider})
	modelRevision := "configured:" + strings.TrimPrefix(configurationHash, "sha256:")
	result := MaintenanceProviderPlan{Profile: setup.Profile, Provider: setup.Provider, ProviderType: setup.ProviderType, Model: setup.Model, ModelRevision: modelRevision, ConfigurationHash: configurationHash, DataBoundary: boundary, Installed: setup.ProviderInstalled, Running: setup.ProviderLive, ModelAvailable: setup.ModelAvailable, EmbeddingSmokeStatus: setup.EmbeddingSmoke}
	actions := []MaintenancePlanAction{}
	blockers := []string{}
	if !setup.ProviderInstalled {
		actions = append(actions, MaintenancePlanAction{ID: "install-provider", Class: "local_service_change", Status: "blocked", Summary: "install the configured embedding provider", ConfirmationRequired: true, DataBoundary: boundary, Handoff: firstString(setup.InstallInstructions)})
		blockers = append(blockers, "embedding provider is not installed")
		return result, actions, blockers, nil
	}
	if !setup.ProviderLive {
		actions = append(actions, MaintenancePlanAction{ID: "start-provider", Class: "local_service_change", Status: "required", Summary: "start the configured embedding provider", ConfirmationRequired: true, DataBoundary: boundary, Handoff: "gitcode-mcp rag setup --yes"})
		action := MaintenancePlanAction{ID: "download-model-if-missing", Class: "large_download", Status: "required", Summary: "after provider startup, download the configured model only if verification finds it missing", ConfirmationRequired: true, DataBoundary: boundary, Handoff: "gitcode-mcp rag setup --yes"}
		if req.NoModelDownload {
			action.Status = "blocked"
			blockers = append(blockers, "model availability cannot be verified while the provider is stopped and downloads are disabled")
		}
		actions = append(actions, action)
	}
	if setup.ProviderLive && !setup.ModelAvailable {
		action := MaintenancePlanAction{ID: "download-model", Class: "large_download", Status: "required", Summary: "download the configured embedding model", ConfirmationRequired: true, DataBoundary: boundary, Handoff: "gitcode-mcp rag setup --yes"}
		if req.NoModelDownload {
			action.Status = "blocked"
			blockers = append(blockers, "model download is disabled by policy")
		}
		actions = append(actions, action)
	}
	if setup.Status == "ready" {
		if embeddingProvider, providerErr := rag.NewEmbeddingProviderFromConfig(s.Config, setup.Profile, rag.ProviderOptions{}); providerErr == nil {
			if info, infoErr := embeddingProvider.ModelInfo(ctx); infoErr == nil && strings.TrimSpace(info.Revision) != "" {
				result.ModelRevision = strings.TrimSpace(info.Revision)
			}
		}
		actions = append(actions, MaintenancePlanAction{ID: "verify-provider-smoke", Class: "provider_data_transfer", Status: "required", Summary: "run an embedding smoke request before daemon enrollment", DataBoundary: boundary})
	}
	return result, actions, blockers, nil
}

func (s MaintenanceSetup) rpcClient() (*RPCClient, error) {
	if s.Client != nil {
		return s.Client()
	}
	return s.Manager.Client()
}

func (s MaintenanceSetup) waitForMaintenanceDaemon(ctx context.Context) (*RPCClient, MaintenanceCapabilities, error) {
	timeout := s.StartupTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		client, err := s.rpcClient()
		if err == nil {
			var capabilities MaintenanceCapabilities
			if callErr := client.Call(ctx, "Maintenance.Capabilities", nil, &capabilities); callErr == nil {
				return client, capabilities, nil
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, MaintenanceCapabilities{}, MaintenanceServiceReadinessError{}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, MaintenanceCapabilities{}, MaintenanceServiceReadinessError{}
		case <-timer.C:
		}
	}
}

func normalizeMaintenanceSetupRequest(req MaintenanceSetupRequest) (MaintenanceSetupRequest, error) {
	req.RepoID = strings.TrimSpace(req.RepoID)
	if req.RepoID == "" {
		return req, errors.New("maintenance: repo_id is required")
	}
	if req.SyncMode == "" {
		req.SyncMode = "head-and-backfill"
	}
	if req.SyncMode != "off" && req.SyncMode != "head" && req.SyncMode != "head-and-backfill" {
		return req, errors.New("maintenance: sync must be off, head, or head-and-backfill")
	}
	if req.RAGMode == "" {
		req.RAGMode = "maintain"
	}
	if req.RAGMode != "off" && req.RAGMode != "maintain" {
		return req, errors.New("maintenance: rag must be off or maintain")
	}
	seen := map[string]bool{}
	for _, value := range req.Collections {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(strings.ToLower(item))
			switch item {
			case "", "all":
			case "issues", "issue-comments", "wiki", "pulls", "pr-comments":
				seen[item] = true
			default:
				return req, fmt.Errorf("maintenance: unsupported collection %q", item)
			}
		}
	}
	req.Collections = req.Collections[:0]
	for _, item := range []string{"issues", "issue-comments", "wiki", "pulls", "pr-comments"} {
		if seen[item] {
			req.Collections = append(req.Collections, item)
		}
	}
	return req, nil
}

func setupMaintenancePolicy(req MaintenanceSetupRequest, binding cache.RepositoryBinding) (MaintenancePolicy, error) {
	policy := MaintenancePolicy{
		SyncEnabled: req.SyncMode != "off", SyncMode: req.SyncMode, RAGEnabled: req.RAGMode == "maintain", Profile: strings.TrimSpace(req.Profile),
		HeadIntervalSeconds: req.HeadIntervalSeconds, RAGIntervalSeconds: req.RAGIntervalSeconds,
		HeadMaxPages: req.HeadMaxPages, TailSlicePages: req.TailSlicePages, PerPage: req.PerPage,
	}
	if len(req.Collections) > 0 {
		for _, collection := range req.Collections {
			switch collection {
			case "issues":
				policy.Issues = true
			case "issue-comments":
				policy.IssueComments = true
			case "wiki":
				policy.Wiki = true
			case "pulls":
				policy.Pulls = true
			case "pr-comments":
				policy.PRComments = true
			}
		}
	}
	policy, err := normalizeMaintenancePolicy(policy, binding)
	if err != nil {
		return MaintenancePolicy{}, err
	}
	return policy, nil
}

func maintenanceBinding(ctx context.Context, store *cache.SQLiteStore, repoID string) (cache.RepositoryBinding, error) {
	repos, err := store.ListRepositories(ctx)
	if err != nil {
		return cache.RepositoryBinding{}, err
	}
	for _, binding := range repos {
		if binding.RepoID == repoID {
			return binding, nil
		}
	}
	return cache.RepositoryBinding{}, fmt.Errorf("maintenance: repository %q is not bound in selected cache", repoID)
}

func maintenanceScopes(binding cache.RepositoryBinding) []string {
	result := make([]string, 0, len(binding.Scopes))
	for _, scope := range binding.Scopes {
		result = append(result, string(scope))
	}
	sort.Strings(result)
	return result
}

func maintenanceLocationKind(source string) string {
	source = strings.TrimSpace(source)
	switch {
	case source == "command":
		return "explicit"
	case strings.HasPrefix(source, "repo-local:"):
		return "repo-local"
	case strings.Contains(strings.ToLower(source), "codex"):
		return "codex"
	case source == "default", source == "yaml", source == "legacy-json":
		return "global"
	default:
		return "configured"
	}
}

func maintenanceHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:16])
}

func maintenancePlanID(plan MaintenancePlan) string {
	copy := plan
	copy.PlanID = ""
	copy.NextAction = ""
	copy.Service.UpdatedAt = nil
	return "maintenance-plan-" + strings.TrimPrefix(maintenanceHash(copy), "sha256:")
}

func sanitizeMaintenanceServiceStatus(status Status) Status {
	return Status{Status: status.Status, Installed: status.Installed, Running: status.Running, PIDAlive: status.PIDAlive, SocketPresent: status.SocketPresent, InstallKind: status.InstallKind, Message: status.Message}
}

func maintenancePlanNeedsConfirmation(actions []MaintenancePlanAction) bool {
	for _, action := range actions {
		if action.Status == "required" && action.ConfirmationRequired {
			return true
		}
	}
	return false
}

func maintenancePlanNeedsMachineChange(actions []MaintenancePlanAction) bool {
	for _, action := range actions {
		if action.Status == "required" && (action.Class == "local_service_change" || action.Class == "large_download") {
			return true
		}
	}
	return false
}

func maintenancePlanNextAction(plan MaintenancePlan) string {
	if len(plan.Blockers) > 0 {
		return plan.Blockers[0]
	}
	for _, action := range plan.Actions {
		if action.Status == "required" && action.ConfirmationRequired {
			return "review and confirm: " + action.Summary
		}
	}
	return "apply the plan with an idempotency key"
}

func pendingMaintenanceActions(actions []MaintenancePlanAction) []MaintenancePlanAction {
	result := []MaintenancePlanAction{}
	for _, action := range actions {
		if action.Status != "complete" {
			result = append(result, action)
		}
	}
	return result
}

func machineChangeActions(actions []MaintenancePlanAction) []MaintenancePlanAction {
	result := []MaintenancePlanAction{}
	for _, action := range actions {
		if action.Status == "required" && (action.Class == "local_service_change" || action.Class == "large_download") {
			result = append(result, action)
		}
	}
	return result
}

func firstMaintenanceHandoff(actions []MaintenancePlanAction) string {
	for _, action := range actions {
		if action.Handoff != "" {
			return action.Handoff
		}
	}
	return "run the CLI maintenance enable flow to confirm machine-level changes"
}

func actionRequired(actions []MaintenancePlanAction, id string) bool {
	for _, action := range actions {
		if action.ID == id && action.Status == "required" {
			return true
		}
	}
	return false
}

func maintenanceApplyStatus(entry MaintenanceEntry, reconcile MaintenanceReconcileResult) string {
	for _, candidate := range reconcile.Entries {
		if candidate.RegistrationID == entry.RegistrationID {
			switch candidate.State {
			case "backfilling":
				return "backfilling"
			case "refreshing":
				return "refreshing"
			case "indexing":
				return "indexing"
			case "degraded":
				return "blocked"
			}
		}
	}
	if len(reconcile.JobsStarted) > 0 {
		return "indexing"
	}
	return "ready"
}

func maintenanceApplyNextAction(status string, detach bool) string {
	if status == "ready" {
		return "maintenance is enabled; inspect with gitcode-mcp service maintenance"
	}
	if detach {
		return "inspect progress with gitcode-mcp service maintenance"
	}
	return "wait for daemon maintenance to reach ready or report a typed failure"
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func maintenanceProviderFailureMessage(status string) string {
	switch status {
	case "missing_provider":
		return "embedding provider is not installed"
	case "provider_not_running":
		return "embedding provider is not running"
	case "missing_model":
		return "configured embedding model is unavailable"
	case "model_pull_failed":
		return "configured embedding model could not be installed"
	case "smoke_failed":
		return "embedding provider smoke verification failed"
	default:
		return "embedding provider verification failed"
	}
}
