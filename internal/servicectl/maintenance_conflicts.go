package servicectl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
)

const (
	maintenanceConflictResolutionPlanSchema  = "gitcode-mcp.maintenance-conflict-resolution-plan.v1"
	maxMaintenanceConflictResolutionReceipts = 128
)

type MaintenanceConflictResolutionRequest struct {
	RegistrationID     string `json:"registration_id"`
	CandidateRef       string `json:"candidate_ref"`
	ExpectedGeneration int64  `json:"expected_generation"`
	PlanID             string `json:"plan_id,omitempty"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
}

type MaintenanceConflictResolutionEffect struct {
	Class   string `json:"class"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

type MaintenanceConflictResolutionPlan struct {
	SchemaVersion           string                                `json:"schema_version"`
	PlanID                  string                                `json:"plan_id"`
	Status                  string                                `json:"status"`
	RegistrationID          string                                `json:"registration_id"`
	CanonicalRegistrationID string                                `json:"canonical_registration_id"`
	ConflictKind            string                                `json:"conflict_kind"`
	ExpectedGeneration      int64                                 `json:"expected_generation"`
	Selected                MaintenanceIdentityCandidate          `json:"selected"`
	Effects                 []MaintenanceConflictResolutionEffect `json:"effects"`
}

type MaintenanceConflictResolutionResult struct {
	Outcome              string `json:"outcome"`
	ReceiptID            string `json:"receipt_id"`
	PlanID               string `json:"plan_id"`
	RegistrationID       string `json:"registration_id"`
	SelectedCandidateRef string `json:"selected_candidate_ref"`
	Generation           int64  `json:"generation"`
	RetiredClonePaths    int    `json:"retired_clone_paths,omitempty"`
	Replayed             bool   `json:"replayed"`
}

type maintenanceConflictResolutionReceipt struct {
	KeyHash              string    `json:"key_hash"`
	IntentHash           string    `json:"intent_hash"`
	ReceiptID            string    `json:"receipt_id"`
	PlanID               string    `json:"plan_id"`
	RegistrationID       string    `json:"registration_id"`
	SelectedCandidateRef string    `json:"selected_candidate_ref"`
	Generation           int64     `json:"generation"`
	RetiredClonePaths    int       `json:"retired_clone_paths,omitempty"`
	AppliedAt            time.Time `json:"applied_at"`
}

type maintenanceRetiredClonePath struct {
	CacheUUID       string `json:"cache_uuid"`
	PathFingerprint string `json:"path_fingerprint"`
}

type MaintenanceConflictResolutionError struct{ code string }

func (e MaintenanceConflictResolutionError) Error() string {
	switch e.code {
	case "conflict_details_unavailable":
		return "maintenance: conflict candidate details are unavailable"
	case "conflict_candidate_not_found":
		return "maintenance: conflict candidate not found"
	case "conflict_generation_stale":
		return "maintenance: conflict generation is stale"
	case "conflict_candidate_unavailable":
		return "maintenance: selected conflict candidate is unavailable"
	case "conflict_candidate_identity_changed":
		return "maintenance: selected conflict candidate identity changed"
	case "cache_clone_retired":
		return "maintenance: cache clone path was retired by conflict resolution"
	default:
		return "maintenance: conflict resolution is unavailable"
	}
}

func (e MaintenanceConflictResolutionError) DiagnosticCode() string { return e.code }

func (m *MaintenanceManager) PlanConflictResolution(ctx context.Context, req MaintenanceConflictResolutionRequest) (MaintenanceConflictResolutionPlan, error) {
	if err := validateMaintenanceConflictResolutionRequest(req, false); err != nil {
		return MaintenanceConflictResolutionPlan{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	plan, _, _, err := m.planConflictResolutionLocked(ctx, req)
	return plan, err
}

func (m *MaintenanceManager) planConflictResolutionLocked(ctx context.Context, req MaintenanceConflictResolutionRequest) (MaintenanceConflictResolutionPlan, maintenanceIdentityConflictCandidate, cache.RepositoryBinding, error) {
	req.RegistrationID = m.resolveRegistrationIDLocked(req.RegistrationID)
	req.CandidateRef = strings.TrimSpace(req.CandidateRef)
	entry := m.entries[req.RegistrationID]
	if entry == nil || entry.IdentityConflict == nil {
		return MaintenanceConflictResolutionPlan{}, maintenanceIdentityConflictCandidate{}, cache.RepositoryBinding{}, MaintenanceConflictResolutionError{code: "identity_conflict"}
	}
	if req.ExpectedGeneration <= 0 || req.ExpectedGeneration != entry.Generation {
		return MaintenanceConflictResolutionPlan{}, maintenanceIdentityConflictCandidate{}, cache.RepositoryBinding{}, MaintenanceConflictResolutionError{code: "conflict_generation_stale"}
	}
	candidates := m.conflictCandidates[req.RegistrationID]
	if !entry.IdentityConflict.DetailsAvailable || len(candidates) == 0 {
		return MaintenanceConflictResolutionPlan{}, maintenanceIdentityConflictCandidate{}, cache.RepositoryBinding{}, MaintenanceConflictResolutionError{code: "conflict_details_unavailable"}
	}
	var selected maintenanceIdentityConflictCandidate
	found := false
	for _, candidate := range candidates {
		if candidate.CandidateRef == req.CandidateRef {
			selected, found = candidate, true
			break
		}
	}
	if !found {
		return MaintenanceConflictResolutionPlan{}, maintenanceIdentityConflictCandidate{}, cache.RepositoryBinding{}, MaintenanceConflictResolutionError{code: "conflict_candidate_not_found"}
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, selected.CachePath)
	if err != nil {
		return MaintenanceConflictResolutionPlan{}, maintenanceIdentityConflictCandidate{}, cache.RepositoryBinding{}, MaintenanceConflictResolutionError{code: "conflict_candidate_unavailable"}
	}
	defer store.Close()
	identity, identityErr := store.CacheIdentity(ctx)
	binding, bindingErr := store.ResolveRepositoryBinding(ctx, selected.Entry.RepoID)
	if identityErr != nil || bindingErr != nil || identity.UUID != entry.CacheUUID || identity.UUID != selected.Entry.CacheUUID {
		return MaintenanceConflictResolutionPlan{}, maintenanceIdentityConflictCandidate{}, cache.RepositoryBinding{}, MaintenanceConflictResolutionError{code: "conflict_candidate_identity_changed"}
	}
	canonicalID := maintenanceRegistrationID(identity.UUID, binding.RepoID)
	plan := MaintenanceConflictResolutionPlan{
		SchemaVersion: maintenanceConflictResolutionPlanSchema, Status: "ready", RegistrationID: req.RegistrationID,
		CanonicalRegistrationID: canonicalID, ConflictKind: entry.IdentityConflict.Kind, ExpectedGeneration: entry.Generation,
		Selected: publicMaintenanceConflictCandidate(selected),
		Effects: []MaintenanceConflictResolutionEffect{
			{Class: "identity", Summary: "Promote the selected candidate to the canonical repository registration.", Status: "planned"},
			{Class: "history", Summary: "Preserve registration aliases, receipts, job history, and conservative retry state.", Status: "planned"},
			{Class: "authority", Summary: "Restore only the selected private cache, config, and repository-document authority bundle.", Status: "planned"},
		},
	}
	if entry.IdentityConflict.Kind == "cache_clone_conflict" {
		plan.Effects = append(plan.Effects, MaintenanceConflictResolutionEffect{Class: "clone_fence", Summary: "Retire every unselected clone path fingerprint for this cache UUID.", Status: "planned"})
	}
	publicCandidates := make([]MaintenanceIdentityCandidate, 0, len(candidates))
	for _, candidate := range sortedConflictCandidates(candidates) {
		publicCandidates = append(publicCandidates, publicMaintenanceConflictCandidate(candidate))
	}
	plan.PlanID = "maintenance-conflict-plan-" + strings.TrimPrefix(maintenanceHash(struct {
		Schema       string
		Registration string
		Generation   int64
		Kind         string
		Candidate    string
		Canonical    string
		Candidates   []MaintenanceIdentityCandidate
	}{plan.SchemaVersion, plan.RegistrationID, plan.ExpectedGeneration, plan.ConflictKind, plan.Selected.CandidateRef, plan.CanonicalRegistrationID, publicCandidates}), "sha256:")
	return plan, selected, binding, nil
}

func (m *MaintenanceManager) ApplyConflictResolution(ctx context.Context, req MaintenanceConflictResolutionRequest) (MaintenanceConflictResolutionResult, error) {
	if err := validateMaintenanceConflictResolutionRequest(req, true); err != nil {
		return MaintenanceConflictResolutionResult{}, err
	}
	keyHash := maintenanceIdempotencyKeyHash(req.IdempotencyKey)
	intentHash := maintenanceHash(struct {
		PlanID       string
		CandidateRef string
		Generation   int64
	}{strings.TrimSpace(req.PlanID), strings.TrimSpace(req.CandidateRef), req.ExpectedGeneration})
	m.mu.Lock()
	defer m.mu.Unlock()
	if receipt, ok := m.resolutionReceipts[keyHash]; ok {
		if receipt.IntentHash != intentHash {
			return MaintenanceConflictResolutionResult{}, MaintenanceIdempotencyConflictError{}
		}
		return maintenanceConflictResult(receipt, true), nil
	}
	plan, selected, binding, err := m.planConflictResolutionLocked(ctx, req)
	if err != nil {
		return MaintenanceConflictResolutionResult{}, err
	}
	if strings.TrimSpace(req.PlanID) == "" || strings.TrimSpace(req.PlanID) != plan.PlanID {
		return MaintenanceConflictResolutionResult{}, MaintenanceConflictResolutionError{code: "stale_plan"}
	}
	snapshot := m.snapshotConflictMutationLocked()
	oldRegistrationID := plan.RegistrationID
	entry := m.entries[oldRegistrationID]
	candidates := cloneMaintenanceConflictCandidates(m.conflictCandidates[oldRegistrationID])
	canonicalPath, err := canonicalCachePath(selected.CachePath)
	if err != nil {
		return MaintenanceConflictResolutionResult{}, MaintenanceConflictResolutionError{code: "conflict_candidate_unavailable"}
	}
	merged := cloneMaintenanceEntryPrivate(&selected.Entry)
	merged.RegistrationID = plan.CanonicalRegistrationID
	merged.RepoID = binding.RepoID
	merged.Aliases = sortedUniqueStrings(binding.Aliases)
	merged.PathFingerprint = pathFingerprint(maintenanceCanonicalPathKey(canonicalPath))
	merged.IdentityConflict = nil
	merged.LegacyRegistrationIDs = nil
	merged.cachePath, merged.configReference, merged.configSnapshot = canonicalPath, selected.ConfigReference, selected.ConfigSnapshot
	merged.repositoryPath, merged.repositoryProfile = selected.RepositoryPath, selected.RepositoryProfile
	merged.identityBlockedWasEnabled = false
	merged.Enabled = selected.WasEnabled
	if merged.Enabled {
		merged.State = "enrolled"
	} else {
		merged.State = "disabled"
	}
	merged.LastErrorClass, merged.LastError = "", ""
	maxGeneration := entry.Generation
	activeJobs := append([]string(nil), merged.ActiveJobs...)
	cohortIDs := map[string]bool{oldRegistrationID: true}
	for _, candidate := range candidates {
		cohortIDs[candidate.Entry.RegistrationID] = true
		for _, legacyID := range candidate.Entry.LegacyRegistrationIDs {
			cohortIDs[legacyID] = true
		}
		if candidate.Entry.Generation > maxGeneration {
			maxGeneration = candidate.Entry.Generation
		}
		merged.LastSeenAt = laterTime(merged.LastSeenAt, candidate.Entry.LastSeenAt)
		merged.LastReconciledAt = laterTime(merged.LastReconciledAt, candidate.Entry.LastReconciledAt)
		merged.SyncStage = conservativeMaintenanceStage(merged.SyncStage, candidate.Entry.SyncStage)
		merged.RAGStage = conservativeMaintenanceStage(merged.RAGStage, candidate.Entry.RAGStage)
		activeJobs = append(activeJobs, candidate.Entry.ActiveJobs...)
	}
	for id := range cohortIDs {
		if id != merged.RegistrationID {
			merged.LegacyRegistrationIDs = append(merged.LegacyRegistrationIDs, id)
		}
	}
	merged.LegacyRegistrationIDs = sortedUniqueStrings(merged.LegacyRegistrationIDs)
	merged.ActiveJobs = sortedUniqueStrings(activeJobs)
	merged.Generation = maxGeneration + 1
	for from, to := range m.redirects {
		if cohortIDs[to] || to == oldRegistrationID {
			m.redirects[from] = merged.RegistrationID
		}
	}
	for id := range cohortIDs {
		if id != merged.RegistrationID {
			m.redirects[id] = merged.RegistrationID
		}
	}
	if oldRegistrationID != merged.RegistrationID {
		m.redirects[oldRegistrationID] = merged.RegistrationID
	}
	selectedSources := map[string]*repositoryDocsRegisteredSource{}
	for _, diskSource := range selected.RepositorySources {
		source := diskSource
		oldSourceID := source.State.SourceRegistrationID
		newSourceID := repositoryDocsSourceRegistrationID(merged.RegistrationID, source.State.GitStoreRef, source.State.WorktreeRef, source.Profile)
		if oldSourceID != newSourceID {
			m.sourceRedirects[oldSourceID] = newSourceID
			source.State.SourceRegistrationGeneration++
		}
		source.State.SourceRegistrationID = newSourceID
		selectedSources[newSourceID] = &repositoryDocsRegisteredSource{State: source.State, RepositoryPath: source.RepositoryPath, Profile: source.Profile}
	}
	delete(m.entries, oldRegistrationID)
	delete(m.sources, oldRegistrationID)
	delete(m.conflictCandidates, oldRegistrationID)
	m.entries[merged.RegistrationID] = &merged
	m.sources[merged.RegistrationID] = selectedSources
	m.refreshLegacyRepositoryDocsLocked(&merged)
	for key, receipt := range m.receipts {
		if cohortIDs[receipt.RegistrationID] || receipt.RegistrationID == oldRegistrationID {
			receipt.RegistrationID = merged.RegistrationID
			receipt.IntentHash = maintenanceEnrollmentIntentHash(merged.RegistrationID, merged.Policy, merged.ConfigHash)
			m.receipts[key] = receipt
		}
	}
	for key, admission := range m.admissions {
		if cohortIDs[admission.RegistrationID] || admission.RegistrationID == oldRegistrationID {
			delete(m.admissions, key)
		}
	}
	retired := 0
	if plan.ConflictKind == "cache_clone_conflict" {
		if m.retiredClonePaths[merged.CacheUUID] == nil {
			m.retiredClonePaths[merged.CacheUUID] = map[string]bool{}
		}
		selectedFingerprint := pathFingerprint(maintenanceCanonicalPathKey(selected.CachePath))
		for _, candidate := range candidates {
			fingerprint := pathFingerprint(maintenanceCanonicalPathKey(candidate.CachePath))
			if fingerprint != selectedFingerprint && !m.retiredClonePaths[merged.CacheUUID][fingerprint] {
				m.retiredClonePaths[merged.CacheUUID][fingerprint] = true
				retired++
			}
		}
	}
	m.generation++
	receipt := maintenanceConflictResolutionReceipt{
		KeyHash: keyHash, IntentHash: intentHash, ReceiptID: "maintenance-conflict-receipt-" + strings.TrimPrefix(keyHash, "sha256:")[:16],
		PlanID: plan.PlanID, RegistrationID: merged.RegistrationID, SelectedCandidateRef: selected.CandidateRef,
		Generation: merged.Generation, RetiredClonePaths: retired, AppliedAt: m.now(),
	}
	m.resolutionReceipts[keyHash] = receipt
	if err := m.saveLocked(); err != nil {
		m.restoreConflictMutationLocked(snapshot)
		return MaintenanceConflictResolutionResult{}, err
	}
	if m.jobs != nil {
		m.jobs.SetRegistrationRedirects(m.redirects, m.sourceRedirects, m.canonicalRepoIDsLocked())
	}
	return maintenanceConflictResult(receipt, false), nil
}

func maintenanceConflictResult(receipt maintenanceConflictResolutionReceipt, replayed bool) MaintenanceConflictResolutionResult {
	return MaintenanceConflictResolutionResult{Outcome: "resolved", ReceiptID: receipt.ReceiptID, PlanID: receipt.PlanID, RegistrationID: receipt.RegistrationID, SelectedCandidateRef: receipt.SelectedCandidateRef, Generation: receipt.Generation, RetiredClonePaths: receipt.RetiredClonePaths, Replayed: replayed}
}

type maintenanceConflictMutationSnapshot struct {
	generation         int64
	entries            map[string]*MaintenanceEntry
	receipts           map[string]maintenanceReceipt
	admissions         map[string]repositoryDocsAdmissionIntent
	sources            map[string]map[string]*repositoryDocsRegisteredSource
	redirects          map[string]string
	sourceRedirects    map[string]string
	conflictCandidates map[string][]maintenanceIdentityConflictCandidate
	resolutionReceipts map[string]maintenanceConflictResolutionReceipt
	retiredClonePaths  map[string]map[string]bool
}

func (m *MaintenanceManager) snapshotConflictMutationLocked() maintenanceConflictMutationSnapshot {
	snapshot := maintenanceConflictMutationSnapshot{
		generation: m.generation, entries: map[string]*MaintenanceEntry{}, receipts: map[string]maintenanceReceipt{}, admissions: map[string]repositoryDocsAdmissionIntent{}, sources: map[string]map[string]*repositoryDocsRegisteredSource{}, redirects: map[string]string{}, sourceRedirects: map[string]string{}, conflictCandidates: map[string][]maintenanceIdentityConflictCandidate{}, resolutionReceipts: map[string]maintenanceConflictResolutionReceipt{}, retiredClonePaths: map[string]map[string]bool{},
	}
	for id, entry := range m.entries {
		copy := cloneMaintenanceEntryPrivate(entry)
		snapshot.entries[id] = &copy
	}
	for key, receipt := range m.receipts {
		snapshot.receipts[key] = receipt
	}
	for key, admission := range m.admissions {
		snapshot.admissions[key] = admission
	}
	for id, sources := range m.sources {
		snapshot.sources[id] = cloneRepositoryDocsSources(sources)
	}
	for from, to := range m.redirects {
		snapshot.redirects[from] = to
	}
	for from, to := range m.sourceRedirects {
		snapshot.sourceRedirects[from] = to
	}
	for id, candidates := range m.conflictCandidates {
		snapshot.conflictCandidates[id] = cloneMaintenanceConflictCandidates(candidates)
	}
	for key, receipt := range m.resolutionReceipts {
		snapshot.resolutionReceipts[key] = receipt
	}
	for cacheUUID, paths := range m.retiredClonePaths {
		snapshot.retiredClonePaths[cacheUUID] = map[string]bool{}
		for fingerprint := range paths {
			snapshot.retiredClonePaths[cacheUUID][fingerprint] = true
		}
	}
	return snapshot
}

func (m *MaintenanceManager) restoreConflictMutationLocked(snapshot maintenanceConflictMutationSnapshot) {
	m.generation, m.entries, m.receipts, m.admissions, m.sources = snapshot.generation, snapshot.entries, snapshot.receipts, snapshot.admissions, snapshot.sources
	m.redirects, m.sourceRedirects, m.conflictCandidates = snapshot.redirects, snapshot.sourceRedirects, snapshot.conflictCandidates
	m.resolutionReceipts, m.retiredClonePaths = snapshot.resolutionReceipts, snapshot.retiredClonePaths
}

func (m *MaintenanceManager) isRetiredClonePathLocked(cacheUUID, cachePath string) bool {
	return m.retiredClonePaths[cacheUUID][pathFingerprint(maintenanceCanonicalPathKey(cachePath))]
}

func validateMaintenanceConflictResolutionRequest(req MaintenanceConflictResolutionRequest, requireApply bool) error {
	if strings.TrimSpace(req.RegistrationID) == "" || strings.TrimSpace(req.CandidateRef) == "" || req.ExpectedGeneration <= 0 {
		return errors.New("maintenance: registration_id, candidate_ref, and expected_generation are required")
	}
	if requireApply && (strings.TrimSpace(req.PlanID) == "" || strings.TrimSpace(req.IdempotencyKey) == "") {
		return errors.New("maintenance: plan_id and idempotency_key are required")
	}
	if len(req.CandidateRef) > 256 || len(req.PlanID) > 256 || len(req.IdempotencyKey) > 256 {
		return fmt.Errorf("maintenance: conflict resolution input exceeds supported bounds")
	}
	return nil
}

func sortedConflictCandidates(candidates []maintenanceIdentityConflictCandidate) []maintenanceIdentityConflictCandidate {
	out := cloneMaintenanceConflictCandidates(candidates)
	sort.Slice(out, func(i, j int) bool { return out[i].CandidateRef < out[j].CandidateRef })
	return out
}

func (m *MaintenanceManager) pruneConflictResolutionReceiptsLocked() {
	if len(m.resolutionReceipts) <= maxMaintenanceConflictResolutionReceipts {
		return
	}
	receipts := make([]maintenanceConflictResolutionReceipt, 0, len(m.resolutionReceipts))
	for _, receipt := range m.resolutionReceipts {
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(i, j int) bool {
		if receipts[i].AppliedAt.Equal(receipts[j].AppliedAt) {
			return receipts[i].KeyHash < receipts[j].KeyHash
		}
		return receipts[i].AppliedAt.Before(receipts[j].AppliedAt)
	})
	for _, receipt := range receipts[:len(receipts)-maxMaintenanceConflictResolutionReceipts] {
		delete(m.resolutionReceipts, receipt.KeyHash)
	}
}
