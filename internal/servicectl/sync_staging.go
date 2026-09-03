package servicectl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gitcode-mcp/internal/cache"
)

const (
	syncStageEnvelopeVersion     = 2
	defaultSyncStageMaxBytes     = int64(16 << 20)
	defaultSyncStageMaxRecords   = 10_000
	defaultSyncStageTotalBytes   = int64(64 << 20)
	defaultSyncStageTotalRecords = 50_000
	defaultSyncStageMaxCount     = 256
	defaultSyncStageMaxAge       = 24 * time.Hour
	defaultSyncCommitRetries     = 6
	defaultSyncCommitBaseDelay   = time.Second
	defaultSyncCommitMaxDelay    = time.Minute
)

var syncStageCapacityMu sync.Mutex

var (
	ErrSyncStageCorrupt = errors.New("sync stage is corrupt")
	ErrSyncStageBound   = errors.New("sync stage exceeds configured bounds")
)

type SyncStagePhase string

const (
	SyncStageFetching      SyncStagePhase = "fetching"
	SyncStageStaged        SyncStagePhase = "staged"
	SyncStageWaitingCommit SyncStagePhase = "waiting_commit"
	SyncStageCommitting    SyncStagePhase = "committing"
	SyncStageCommitted     SyncStagePhase = "committed"
	SyncStageSuperseded    SyncStagePhase = "superseded"
	SyncStageRejected      SyncStagePhase = "rejected"
)

type SyncStageState struct {
	Phase          SyncStagePhase `json:"phase"`
	Attempt        int            `json:"attempt,omitempty"`
	RetryBudget    int            `json:"retry_budget,omitempty"`
	RetryAfter     time.Time      `json:"retry_after,omitempty"`
	BlockerClass   string         `json:"blocker_class,omitempty"`
	BlockingOp     string         `json:"blocking_operation,omitempty"`
	BlockingJobRef string         `json:"blocking_job_ref,omitempty"`
	FetchedAt      time.Time      `json:"fetched_at,omitempty"`
	StagedAt       time.Time      `json:"staged_at,omitempty"`
	CommittedAt    time.Time      `json:"committed_at,omitempty"`
	TerminalReason string         `json:"terminal_reason,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// SyncStageWorkflow is private restart state for a multi-collection sync job.
// It contains only the request controls needed to continue with the next
// collection; source bodies remain confined to Payload.
type SyncStageWorkflow struct {
	Collections           []string       `json:"collections,omitempty"`
	Current               int            `json:"current,omitempty"`
	ProviderMode          string         `json:"provider_mode,omitempty"`
	RequestIdempotencyKey string         `json:"request_idempotency_key,omitempty"`
	MaxPages              int            `json:"max_pages,omitempty"`
	MaxRecords            int            `json:"max_records,omitempty"`
	PerPage               int            `json:"per_page,omitempty"`
	Page                  int            `json:"page,omitempty"`
	Lane                  string         `json:"lane,omitempty"`
	CollectionPages       map[string]int `json:"collection_pages,omitempty"`
}

func (w *SyncStageWorkflow) hasRemaining() bool {
	return w != nil && w.Current >= 0 && w.Current+1 < len(w.Collections)
}

// SyncStageEnvelope is private daemon state. Payload may contain source bodies
// and must never be projected through IPC, Admin, logs, or CLI diagnostics.
type SyncStageEnvelope struct {
	Version             int                        `json:"version"`
	StageID             string                     `json:"stage_id"`
	JobID               string                     `json:"job_id"`
	CacheUUID           string                     `json:"cache_uuid"`
	CacheSchema         int                        `json:"cache_schema"`
	CachePath           string                     `json:"cache_path"`
	RegistrationID      string                     `json:"registration_id"`
	RepoID              string                     `json:"repo_id"`
	BindingFingerprint  string                     `json:"binding_fingerprint"`
	Collection          string                     `json:"collection"`
	Checkpoint          string                     `json:"checkpoint,omitempty"`
	ProviderRevision    string                     `json:"provider_revision,omitempty"`
	IdempotencyKey      string                     `json:"idempotency_key"`
	CreatedAt           time.Time                  `json:"created_at"`
	ExpiresAt           time.Time                  `json:"expires_at"`
	RecordCount         int                        `json:"record_count"`
	ByteCount           int64                      `json:"byte_count"`
	Payload             json.RawMessage            `json:"payload"`
	MaintenanceFrontier *cache.MaintenanceFrontier `json:"maintenance_frontier,omitempty"`
	Workflow            *SyncStageWorkflow         `json:"workflow,omitempty"`
	Checksum            string                     `json:"checksum"`
	State               SyncStageState             `json:"state"`
}

// SyncStageView is the complete public contract. It deliberately has no local
// path, idempotency key, checksum, checkpoint, provider revision, or payload.
type SyncStageView struct {
	StageRef       string         `json:"stage_ref"`
	CacheRef       string         `json:"cache_ref"`
	RepoID         string         `json:"repo_id"`
	Collection     string         `json:"collection"`
	Phase          SyncStagePhase `json:"phase"`
	Fetched        int            `json:"fetched"`
	Staged         int            `json:"staged"`
	Committed      int            `json:"committed"`
	StagedBytes    int64          `json:"staged_bytes,omitempty"`
	Attempt        int            `json:"attempt,omitempty"`
	RetryBudget    int            `json:"retry_budget,omitempty"`
	RetryAfter     time.Time      `json:"retry_after,omitempty"`
	BlockerClass   string         `json:"blocker_class,omitempty"`
	BlockingOp     string         `json:"blocking_operation,omitempty"`
	BlockingJobRef string         `json:"blocking_job_ref,omitempty"`
	FetchedAt      time.Time      `json:"fetched_at,omitempty"`
	StagedAt       time.Time      `json:"staged_at,omitempty"`
	CommittedAt    time.Time      `json:"committed_at,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at"`
	TerminalCause  string         `json:"terminal_reason,omitempty"`
}

func (e SyncStageEnvelope) PublicView() SyncStageView {
	view := SyncStageView{
		StageRef: publicStageRef(e.StageID), CacheRef: publicCacheRef(e.CacheUUID, ""),
		RepoID: e.RepoID, Collection: e.Collection, Phase: e.State.Phase, StagedBytes: e.ByteCount,
		Attempt: e.State.Attempt, RetryBudget: e.State.RetryBudget,
		RetryAfter: e.State.RetryAfter, BlockerClass: e.State.BlockerClass,
		BlockingOp: e.State.BlockingOp, BlockingJobRef: e.State.BlockingJobRef, FetchedAt: e.State.FetchedAt,
		StagedAt: e.State.StagedAt, CommittedAt: e.State.CommittedAt,
		UpdatedAt: e.State.UpdatedAt, TerminalCause: e.State.TerminalReason,
	}
	if !e.State.FetchedAt.IsZero() {
		view.Fetched = e.RecordCount
	}
	if e.State.Phase != SyncStageFetching {
		view.Staged = e.RecordCount
	}
	if e.State.Phase == SyncStageCommitted {
		view.Committed = e.RecordCount
	}
	return view
}

type SyncStageLimits struct {
	MaxBytes        int64
	MaxRecords      int
	MaxTotalBytes   int64
	MaxTotalRecords int
	MaxStages       int
	MaxAge          time.Duration
}

type SyncStageJournal struct {
	dir       string
	limits    SyncStageLimits
	now       func() time.Time
	writeFile func(string, []byte, os.FileMode) error
}

type SyncStageLoadRejection struct {
	StageRef string
	Reason   string
}

func NewSyncStageJournal(runtimeDir string, limits SyncStageLimits) *SyncStageJournal {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaultSyncStageMaxBytes
	}
	if limits.MaxRecords <= 0 {
		limits.MaxRecords = defaultSyncStageMaxRecords
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = defaultSyncStageTotalBytes
	}
	if limits.MaxTotalRecords <= 0 {
		limits.MaxTotalRecords = defaultSyncStageTotalRecords
	}
	if limits.MaxStages <= 0 {
		limits.MaxStages = defaultSyncStageMaxCount
	}
	if limits.MaxAge <= 0 {
		limits.MaxAge = defaultSyncStageMaxAge
	}
	dir := ""
	if runtimeDir = strings.TrimSpace(runtimeDir); runtimeDir != "" {
		dir = filepath.Join(runtimeDir, "sync-stages")
	}
	return &SyncStageJournal{
		dir: dir, limits: limits,
		now: func() time.Time { return time.Now().UTC() }, writeFile: durableAtomicWriteFile,
	}
}

func (j *SyncStageJournal) Create(envelope SyncStageEnvelope) (SyncStageEnvelope, error) {
	syncStageCapacityMu.Lock()
	defer syncStageCapacityMu.Unlock()
	now := j.now().UTC()
	envelope.Version = syncStageEnvelopeVersion
	envelope.JobID = strings.TrimSpace(envelope.JobID)
	envelope.CacheUUID = strings.TrimSpace(envelope.CacheUUID)
	envelope.CachePath = strings.TrimSpace(envelope.CachePath)
	envelope.RegistrationID = strings.TrimSpace(envelope.RegistrationID)
	envelope.RepoID = strings.TrimSpace(envelope.RepoID)
	envelope.BindingFingerprint = strings.TrimSpace(envelope.BindingFingerprint)
	envelope.Collection = strings.TrimSpace(envelope.Collection)
	envelope.IdempotencyKey = strings.TrimSpace(envelope.IdempotencyKey)
	if envelope.JobID == "" || envelope.CacheUUID == "" || envelope.CachePath == "" || envelope.RegistrationID == "" || envelope.RepoID == "" || envelope.BindingFingerprint == "" || envelope.Collection == "" || envelope.IdempotencyKey == "" || envelope.CacheSchema <= 0 {
		return SyncStageEnvelope{}, fmt.Errorf("%w: incomplete stage identity", ErrSyncStageCorrupt)
	}
	if !validSyncStageWorkflow(envelope.Workflow, envelope.Collection) {
		return SyncStageEnvelope{}, fmt.Errorf("%w: invalid workflow checkpoint", ErrSyncStageCorrupt)
	}
	if !json.Valid(envelope.Payload) {
		return SyncStageEnvelope{}, fmt.Errorf("%w: payload is not valid json", ErrSyncStageCorrupt)
	}
	envelope.ByteCount = int64(len(envelope.Payload))
	if envelope.RecordCount < 0 || envelope.RecordCount > j.limits.MaxRecords || envelope.ByteCount > j.limits.MaxBytes {
		return SyncStageEnvelope{}, ErrSyncStageBound
	}
	if envelope.CreatedAt.IsZero() {
		envelope.CreatedAt = now
	}
	if envelope.ExpiresAt.IsZero() {
		envelope.ExpiresAt = envelope.CreatedAt.Add(j.limits.MaxAge)
	}
	if envelope.ExpiresAt.After(envelope.CreatedAt.Add(j.limits.MaxAge)) || !envelope.ExpiresAt.After(envelope.CreatedAt) {
		return SyncStageEnvelope{}, ErrSyncStageBound
	}
	if envelope.State.Phase == "" {
		envelope.State.Phase = SyncStageStaged
	}
	if envelope.State.FetchedAt.IsZero() {
		envelope.State.FetchedAt = now
	}
	if envelope.State.StagedAt.IsZero() {
		envelope.State.StagedAt = now
	}
	envelope.State.UpdatedAt = now
	envelope.Checksum = syncStageChecksum(envelope)
	envelope.StageID = syncStageIdentity(envelope)
	if existing, err := j.Load(envelope.StageID); err == nil {
		if sameSyncStageBatch(existing, envelope) {
			return existing, nil
		}
		return SyncStageEnvelope{}, fmt.Errorf("%w: stage identity collision", ErrSyncStageCorrupt)
	} else if !errors.Is(err, os.ErrNotExist) {
		return SyncStageEnvelope{}, err
	}
	if _, err := j.GC(); err != nil {
		return SyncStageEnvelope{}, err
	}
	bytes, records, stages, err := j.aggregateUsage()
	if err != nil {
		return SyncStageEnvelope{}, err
	}
	if bytes+envelope.ByteCount > j.limits.MaxTotalBytes || records+envelope.RecordCount > j.limits.MaxTotalRecords || stages+1 > j.limits.MaxStages {
		return SyncStageEnvelope{}, ErrSyncStageBound
	}
	if err := j.persist(envelope); err != nil {
		return SyncStageEnvelope{}, err
	}
	return envelope, nil
}

func (j *SyncStageJournal) aggregateUsage() (bytes int64, records, stages int, err error) {
	envelopes, _, err := j.ListForRecovery()
	if err != nil {
		return 0, 0, 0, err
	}
	for _, envelope := range envelopes {
		// Committed payloads are deleted immediately by GC and have an atomic
		// SQLite receipt. Rejected/cancelled payloads remain diagnostic evidence,
		// so they must continue consuming every aggregate quota until expiry.
		if envelope.State.Phase == SyncStageCommitted {
			continue
		}
		bytes += envelope.ByteCount
		records += envelope.RecordCount
		stages++
	}
	return bytes, records, stages, nil
}

func (j *SyncStageJournal) Load(stageID string) (SyncStageEnvelope, error) {
	path, err := j.path(stageID)
	if err != nil {
		return SyncStageEnvelope{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SyncStageEnvelope{}, err
	}
	var envelope SyncStageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return SyncStageEnvelope{}, fmt.Errorf("%w: invalid envelope", ErrSyncStageCorrupt)
	}
	if err := j.validate(envelope, stageID); err != nil {
		return SyncStageEnvelope{}, err
	}
	return envelope, nil
}

func (j *SyncStageJournal) UpdateState(stageID string, state SyncStageState) (SyncStageEnvelope, error) {
	envelope, err := j.Load(stageID)
	if err != nil {
		return SyncStageEnvelope{}, err
	}
	state.UpdatedAt = j.now().UTC()
	envelope.State = state
	if err := j.persist(envelope); err != nil {
		return SyncStageEnvelope{}, err
	}
	return envelope, nil
}

func (j *SyncStageJournal) List() ([]SyncStageEnvelope, error) {
	entries, err := os.ReadDir(j.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stages := make([]SyncStageEnvelope, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		stage, err := j.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	sort.Slice(stages, func(a, b int) bool {
		if stages[a].State.UpdatedAt.Equal(stages[b].State.UpdatedAt) {
			return stages[a].StageID < stages[b].StageID
		}
		return stages[a].State.UpdatedAt.Before(stages[b].State.UpdatedAt)
	})
	return stages, nil
}

// ListForRecovery isolates corrupt private envelopes and continues returning
// valid work. A single torn or tampered sidecar must not prevent the daemon or
// unrelated repositories from recovering.
func (j *SyncStageJournal) ListForRecovery() ([]SyncStageEnvelope, []SyncStageLoadRejection, error) {
	entries, err := os.ReadDir(j.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	stages := make([]SyncStageEnvelope, 0, len(entries))
	rejections := make([]SyncStageLoadRejection, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		stageID := strings.TrimSuffix(entry.Name(), ".json")
		stage, loadErr := j.Load(stageID)
		if loadErr == nil {
			stages = append(stages, stage)
			continue
		}
		reason := "corrupt_stage"
		if errors.Is(loadErr, ErrSyncStageBound) {
			reason = "stage_bounds_exceeded"
		} else if !errors.Is(loadErr, ErrSyncStageCorrupt) {
			return nil, rejections, loadErr
		}
		if err := j.quarantine(entry.Name()); err != nil {
			return nil, rejections, err
		}
		rejections = append(rejections, SyncStageLoadRejection{StageRef: publicStageRef(stageID), Reason: reason})
	}
	sort.Slice(stages, func(a, b int) bool {
		if stages[a].State.UpdatedAt.Equal(stages[b].State.UpdatedAt) {
			return stages[a].StageID < stages[b].StageID
		}
		return stages[a].State.UpdatedAt.Before(stages[b].State.UpdatedAt)
	})
	return stages, rejections, nil
}

func (j *SyncStageJournal) quarantine(name string) error {
	if strings.TrimSpace(j.dir) == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return ErrSyncStageCorrupt
	}
	from := filepath.Join(j.dir, name)
	to := strings.TrimSuffix(from, ".json") + ".rejected"
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return os.Chmod(to, 0o600)
}

func (j *SyncStageJournal) GC() (int, error) {
	stages, _, err := j.ListForRecovery()
	if err != nil {
		return 0, err
	}
	now := j.now().UTC()
	latestWorkflowIndex := map[string]int{}
	for _, stage := range stages {
		if stage.Workflow == nil {
			continue
		}
		if current, ok := latestWorkflowIndex[stage.JobID]; !ok || stage.Workflow.Current > current {
			latestWorkflowIndex[stage.JobID] = stage.Workflow.Current
		}
	}
	removed := 0
	for _, stage := range stages {
		if !syncStageTerminal(stage.State.Phase) || stage.State.Phase != SyncStageCommitted && now.Before(stage.ExpiresAt) {
			continue
		}
		// Keep the newest committed stage for each active workflow until the
		// durable job snapshot is terminal. It is the restart checkpoint for
		// the crash window before the next collection is staged.
		if stage.State.Phase == SyncStageCommitted && stage.Workflow != nil && stage.Workflow.Current >= latestWorkflowIndex[stage.JobID] && now.Before(stage.ExpiresAt) {
			continue
		}
		path, err := j.path(stage.StageID)
		if err != nil {
			return removed, err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	entries, readErr := os.ReadDir(j.dir)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return removed, readErr
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rejected") {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return removed, statErr
		}
		if now.Before(info.ModTime().UTC().Add(j.limits.MaxAge)) {
			continue
		}
		if err := os.Remove(filepath.Join(j.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (j *SyncStageJournal) RemoveJobStages(jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil
	}
	stages, _, err := j.ListForRecovery()
	if err != nil {
		return err
	}
	for _, stage := range stages {
		if stage.JobID != jobID {
			continue
		}
		path, pathErr := j.path(stage.StageID)
		if pathErr != nil {
			return pathErr
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	return nil
}

func (j *SyncStageJournal) validate(envelope SyncStageEnvelope, expectedID string) error {
	if envelope.Version != syncStageEnvelopeVersion || envelope.StageID != expectedID || !json.Valid(envelope.Payload) {
		return ErrSyncStageCorrupt
	}
	if strings.TrimSpace(envelope.JobID) == "" || strings.TrimSpace(envelope.CacheUUID) == "" || strings.TrimSpace(envelope.CachePath) == "" || strings.TrimSpace(envelope.RegistrationID) == "" || strings.TrimSpace(envelope.RepoID) == "" || strings.TrimSpace(envelope.BindingFingerprint) == "" || strings.TrimSpace(envelope.Collection) == "" || strings.TrimSpace(envelope.IdempotencyKey) == "" || envelope.CacheSchema <= 0 {
		return ErrSyncStageCorrupt
	}
	if !validSyncStageWorkflow(envelope.Workflow, envelope.Collection) {
		return ErrSyncStageCorrupt
	}
	if envelope.ByteCount != int64(len(envelope.Payload)) {
		return ErrSyncStageCorrupt
	}
	if envelope.ByteCount > j.limits.MaxBytes || envelope.RecordCount < 0 || envelope.RecordCount > j.limits.MaxRecords {
		return ErrSyncStageBound
	}
	checksum := syncStageChecksum(envelope)
	if checksum != envelope.Checksum || expectedID != syncStageIdentity(envelope) {
		return ErrSyncStageCorrupt
	}
	return nil
}

func validSyncStageWorkflow(workflow *SyncStageWorkflow, collection string) bool {
	if workflow == nil {
		return true
	}
	if workflow.Current < 0 || workflow.Current >= len(workflow.Collections) || workflow.Collections[workflow.Current] != collection {
		return false
	}
	seen := map[string]bool{}
	for _, candidate := range workflow.Collections {
		if !supportedDurableSyncCollection(candidate) || seen[candidate] {
			return false
		}
		seen[candidate] = true
	}
	return true
}

func (j *SyncStageJournal) persist(envelope SyncStageEnvelope) error {
	path, err := j.path(envelope.StageID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return j.writeFile(path, append(data, '\n'), 0o600)
}

func (j *SyncStageJournal) path(stageID string) (string, error) {
	if strings.TrimSpace(j.dir) == "" {
		return "", errors.New("sync stage journal requires service runtime directory")
	}
	if !validStageID(stageID) {
		return "", fmt.Errorf("%w: invalid stage id", ErrSyncStageCorrupt)
	}
	return filepath.Join(j.dir, stageID+".json"), nil
}

func validStageID(stageID string) bool {
	if len(stageID) != len("stage-")+24 || !strings.HasPrefix(stageID, "stage-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(stageID, "stage-"))
	return err == nil
}

func syncStageChecksum(envelope SyncStageEnvelope) string {
	immutable := struct {
		Version             int                        `json:"version"`
		JobID               string                     `json:"job_id"`
		CacheUUID           string                     `json:"cache_uuid"`
		CacheSchema         int                        `json:"cache_schema"`
		CachePath           string                     `json:"cache_path"`
		RegistrationID      string                     `json:"registration_id"`
		RepoID              string                     `json:"repo_id"`
		BindingFingerprint  string                     `json:"binding_fingerprint"`
		Collection          string                     `json:"collection"`
		Checkpoint          string                     `json:"checkpoint,omitempty"`
		ProviderRevision    string                     `json:"provider_revision,omitempty"`
		IdempotencyKey      string                     `json:"idempotency_key"`
		CreatedAt           time.Time                  `json:"created_at"`
		ExpiresAt           time.Time                  `json:"expires_at"`
		RecordCount         int                        `json:"record_count"`
		ByteCount           int64                      `json:"byte_count"`
		Payload             json.RawMessage            `json:"payload"`
		MaintenanceFrontier *cache.MaintenanceFrontier `json:"maintenance_frontier,omitempty"`
		Workflow            *SyncStageWorkflow         `json:"workflow,omitempty"`
	}{
		envelope.Version, envelope.JobID, envelope.CacheUUID, envelope.CacheSchema, envelope.CachePath,
		envelope.RegistrationID, envelope.RepoID, envelope.BindingFingerprint, envelope.Collection,
		envelope.Checkpoint, envelope.ProviderRevision, envelope.IdempotencyKey,
		envelope.CreatedAt.UTC(), envelope.ExpiresAt.UTC(), envelope.RecordCount,
		envelope.ByteCount, envelope.Payload, envelope.MaintenanceFrontier, envelope.Workflow,
	}
	data, _ := json.Marshal(immutable)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func syncStageIdentity(envelope SyncStageEnvelope) string {
	identity := struct {
		CacheUUID          string `json:"cache_uuid"`
		CacheSchema        int    `json:"cache_schema"`
		RegistrationID     string `json:"registration_id"`
		RepoID             string `json:"repo_id"`
		BindingFingerprint string `json:"binding_fingerprint"`
		Collection         string `json:"collection"`
		Checkpoint         string `json:"checkpoint,omitempty"`
		ProviderRevision   string `json:"provider_revision,omitempty"`
		IdempotencyKey     string `json:"idempotency_key"`
	}{
		envelope.CacheUUID, envelope.CacheSchema, envelope.RegistrationID,
		envelope.RepoID, envelope.BindingFingerprint, envelope.Collection, envelope.Checkpoint,
		envelope.ProviderRevision, envelope.IdempotencyKey,
	}
	data, _ := json.Marshal(identity)
	sum := sha256.Sum256(data)
	return "stage-" + hex.EncodeToString(sum[:12])
}

func sameSyncStageBatch(first, second SyncStageEnvelope) bool {
	return first.CacheUUID == second.CacheUUID && first.CacheSchema == second.CacheSchema &&
		first.RegistrationID == second.RegistrationID && first.RepoID == second.RepoID &&
		first.BindingFingerprint == second.BindingFingerprint &&
		first.Collection == second.Collection && first.Checkpoint == second.Checkpoint &&
		first.ProviderRevision == second.ProviderRevision && first.IdempotencyKey == second.IdempotencyKey &&
		first.RecordCount == second.RecordCount && first.ByteCount == second.ByteCount &&
		string(first.Payload) == string(second.Payload) && sameSyncStageWorkflow(first.Workflow, second.Workflow)
}

func sameSyncStageWorkflow(first, second *SyncStageWorkflow) bool {
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	return string(left) == string(right)
}

func syncStageTerminal(phase SyncStagePhase) bool {
	switch phase {
	case SyncStageCommitted, SyncStageSuperseded, SyncStageRejected:
		return true
	default:
		return false
	}
}

// nextSyncCommitRetry returns a deterministic, capped backoff. The stage id
// supplies stable jitter so daemon restart cannot collapse retries into a hot
// loop or change an already observable retry schedule.
func nextSyncCommitRetry(stageID string, previous SyncStageState, now time.Time) (SyncStageState, bool) {
	budget := previous.RetryBudget
	if budget <= 0 {
		budget = defaultSyncCommitRetries
	}
	attempt := previous.Attempt + 1
	if attempt > budget {
		previous.Phase = SyncStageRejected
		previous.Attempt = attempt
		previous.RetryBudget = budget
		previous.RetryAfter = time.Time{}
		previous.TerminalReason = "commit_retry_budget_exhausted"
		previous.UpdatedAt = now.UTC()
		return previous, false
	}
	delay := defaultSyncCommitBaseDelay << min(attempt-1, 16)
	if delay > defaultSyncCommitMaxDelay {
		delay = defaultSyncCommitMaxDelay
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", stageID, attempt)))
	jitter := time.Duration(uint16(sum[0])<<8|uint16(sum[1])) * (delay / 4) / time.Duration(^uint16(0))
	previous.Phase = SyncStageWaitingCommit
	previous.Attempt = attempt
	previous.RetryBudget = budget
	previous.RetryAfter = now.UTC().Add(delay + jitter)
	previous.BlockerClass = "cache_busy"
	previous.TerminalReason = ""
	previous.UpdatedAt = now.UTC()
	return previous, true
}

func publicStageRef(stageID string) string {
	sum := sha256.Sum256([]byte("sync-stage\x00" + stageID))
	return "stage-" + hex.EncodeToString(sum[:6])
}
