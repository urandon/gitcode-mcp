package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gitcode-mcp/internal/adminhttp"
)

const (
	jobActionReceiptSchema = "gitcode-mcp.job-actions.v1"
	maxJobActionReceipts   = 256
)

type jobActionReceiptDisk struct {
	KeyHash     string                     `json:"key_hash"`
	IntentHash  string                     `json:"intent_hash"`
	Receipt     adminhttp.JobActionReceipt `json:"receipt"`
	RetrySource *jobActionRetrySource      `json:"retry_source,omitempty"`
}

type jobActionRetrySource struct {
	Type           string `json:"type"`
	RepoID         string `json:"repo_id"`
	CacheUUID      string `json:"cache_uuid"`
	RegistrationID string `json:"registration_id"`
	Collection     string `json:"collection,omitempty"`
}

func retrySourceFromJob(job Job, collection string) *jobActionRetrySource {
	return &jobActionRetrySource{Type: job.Type, RepoID: job.RepoID, CacheUUID: job.CacheUUID, RegistrationID: job.RegistrationID, Collection: strings.TrimSpace(collection)}
}

func (source *jobActionRetrySource) job(receipt adminhttp.JobActionReceipt) (Job, bool) {
	if source == nil || strings.TrimSpace(source.Type) == "" || strings.TrimSpace(source.RegistrationID) == "" {
		return Job{}, false
	}
	return Job{ID: receipt.TargetJob, Type: source.Type, RepoID: source.RepoID, CacheUUID: source.CacheUUID, RegistrationID: source.RegistrationID, Status: receipt.JobStatus}, true
}

type jobActionReceiptFile struct {
	Schema   string                 `json:"schema"`
	Receipts []jobActionReceiptDisk `json:"receipts"`
}

type jobActionIntentContextKey struct{}

func withJobActionIntent(ctx context.Context, ref string) context.Context {
	return context.WithValue(ctx, jobActionIntentContextKey{}, strings.TrimSpace(ref))
}

func jobActionIntentFromContext(ctx context.Context) string {
	ref, _ := ctx.Value(jobActionIntentContextKey{}).(string)
	return strings.TrimSpace(ref)
}

type JobActionManager struct {
	mu        sync.Mutex
	path      string
	jobs      *JobManager
	reconcile func(context.Context, string, string, string) (MaintenanceReconcileResult, error)
	receipts  map[string]jobActionReceiptDisk
	now       func() time.Time
	writeFile func(string, []byte, os.FileMode) error
}

func NewJobActionManager(path string, jobs *JobManager, maintenance *MaintenanceManager) *JobActionManager {
	manager := &JobActionManager{path: path, jobs: jobs, receipts: map[string]jobActionReceiptDisk{}, now: func() time.Time { return time.Now().UTC() }, writeFile: durableAtomicWriteFile}
	if maintenance != nil {
		manager.reconcile = func(ctx context.Context, registrationID, collection, actionIntentRef string) (MaintenanceReconcileResult, error) {
			ctx = withJobActionIntent(ctx, actionIntentRef)
			if strings.TrimSpace(collection) != "" {
				return maintenance.RetrySyncCollection(ctx, registrationID, collection)
			}
			return maintenance.ReconcileRegistration(ctx, registrationID)
		}
	}
	return manager
}

func (m *JobActionManager) Load() error {
	if m == nil || m.path == "" {
		return nil
	}
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file jobActionReceiptFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.Schema != jobActionReceiptSchema {
		return errors.New("job actions: unsupported receipt schema")
	}
	for _, receipt := range file.Receipts {
		m.receipts[receipt.KeyHash] = receipt
	}
	changed := m.recoverPendingLocked(context.Background())
	m.pruneLocked()
	if changed {
		if err := m.saveLocked(); err != nil {
			return err
		}
	}
	for _, receipt := range m.receipts {
		if receipt.Receipt.Action == "retry" && receipt.Receipt.Outcome != "pending" {
			_ = m.jobs.ReleaseActionIntent(hashJobAction(receipt.KeyHash + "\x00" + receipt.IntentHash))
		}
	}
	return nil
}

func (m *JobActionManager) recoverPendingLocked(ctx context.Context) bool {
	ordered := make([]jobActionReceiptDisk, 0, len(m.receipts))
	for _, receipt := range m.receipts {
		if receipt.Receipt.Action == "retry" && receipt.Receipt.Outcome == "pending" {
			ordered = append(ordered, receipt)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Receipt.CreatedAt.Equal(ordered[j].Receipt.CreatedAt) {
			return ordered[i].KeyHash < ordered[j].KeyHash
		}
		return ordered[i].Receipt.CreatedAt.Before(ordered[j].Receipt.CreatedAt)
	})
	changed := false
	for _, stored := range ordered {
		actionIntentRef := hashJobAction(stored.KeyHash + "\x00" + stored.IntentHash)
		receipt := stored.Receipt
		receipt.Replayed = true
		if admitted, found := m.jobs.RetainedRetryIntentResult(actionIntentRef); found {
			receipt.ResultJob, receipt.Outcome, receipt.JobStatus = admitted.ID, "coalesced", admitted.Status
		} else {
			// Legacy v1 pending receipts did not retain collection scope. They
			// remain fail-closed unless an exact admitted-job correlation exists.
			if stored.RetrySource == nil {
				continue
			}
			source, found := m.jobs.Get(receipt.TargetJob)
			if !found {
				source, found = stored.RetrySource.job(receipt)
			}
			if !found || m.reconcile == nil {
				continue
			}
			result, err := m.reconcile(ctx, source.RegistrationID, stored.RetrySource.Collection, actionIntentRef)
			if err != nil {
				continue
			}
			m.applyRetryResult(&receipt, source, result)
		}
		stored.Receipt = receipt
		m.receipts[stored.KeyHash] = stored
		changed = true
	}
	return changed
}

func (m *JobActionManager) Cancel(ctx context.Context, req adminhttp.JobActionRequest) (adminhttp.JobActionReceipt, error) {
	return m.apply(ctx, "cancel", req)
}

func (m *JobActionManager) Retry(ctx context.Context, req adminhttp.JobActionRequest) (adminhttp.JobActionReceipt, error) {
	return m.apply(ctx, "retry", req)
}

func (m *JobActionManager) apply(ctx context.Context, action string, req adminhttp.JobActionRequest) (adminhttp.JobActionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req.JobID = strings.TrimSpace(req.JobID)
	req.Collection = strings.TrimSpace(req.Collection)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.JobID == "" || req.IdempotencyKey == "" {
		return adminhttp.JobActionReceipt{}, jobActionError(http.StatusBadRequest, "invalid_request", "job_id and idempotency_key are required.", "Refresh the job and generate a new action key.")
	}
	keyHash := hashJobAction(req.IdempotencyKey)
	intentHash := hashJobAction(action + "\x00" + req.JobID + "\x00" + req.Collection)
	actionIntentRef := hashJobAction(keyHash + "\x00" + intentHash)
	var preparedReceipt *adminhttp.JobActionReceipt
	var preparedDisk *jobActionReceiptDisk
	if stored, ok := m.receipts[keyHash]; ok {
		if stored.IntentHash != intentHash {
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusConflict, "idempotency_conflict", "The idempotency key was already used for a different job action.", "Generate a new key for the changed intent.")
		}
		receipt := stored.Receipt
		receipt.Replayed = true
		if action != "retry" || receipt.Outcome != "pending" {
			if action == "retry" {
				_ = m.jobs.ReleaseActionIntent(actionIntentRef)
			}
			return receipt, nil
		}
		// A pending retry is a durable intent, not a terminal receipt. First
		// resolve the exact action correlation attached during job admission;
		// only an intent that crashed before admission is re-driven.
		preparedReceipt = &receipt
		storedCopy := stored
		preparedDisk = &storedCopy
		if admitted, found := m.jobs.RetainedRetryIntentResult(actionIntentRef); found {
			receipt.ResultJob, receipt.Outcome, receipt.JobStatus = admitted.ID, "coalesced", admitted.Status
			storedCopy.Receipt = receipt
			m.receipts[keyHash] = storedCopy
			m.pruneLocked()
			if err := m.saveLocked(); err != nil {
				m.receipts[keyHash] = *preparedDisk
				return adminhttp.JobActionReceipt{}, err
			}
			_ = m.jobs.ReleaseActionIntent(actionIntentRef)
			return receipt, nil
		}
	}
	job, ok := m.jobs.Get(req.JobID)
	if !ok && preparedDisk != nil {
		job, ok = preparedDisk.RetrySource.job(preparedDisk.Receipt)
	}
	if !ok {
		return adminhttp.JobActionReceipt{}, jobActionError(http.StatusNotFound, "job_not_retained", "The selected job has expired or is not retained by this daemon.", "Return to the bounded job list; cached repository data and audit evidence are unaffected.")
	}
	if job.Type != SyncJobType && job.Type != RAGIndexJobType && !(action == "cancel" && job.Type == RepositoryDocsIndexJobType) {
		return adminhttp.JobActionReceipt{}, jobActionError(http.StatusForbidden, "capability_unavailable", "This job type does not support the requested admin action.", "Use the capability catalog or CLI for supported operations.")
	}
	receipt := adminhttp.JobActionReceipt{ReceiptID: "receipt-" + keyHash[:16], Action: action, TargetJob: job.ID, CreatedAt: m.now()}
	if preparedReceipt != nil {
		receipt = *preparedReceipt
	}
	prepare := func() error {
		if preparedReceipt != nil {
			return nil
		}
		if m.pendingCountLocked() >= maxJobActionReceipts {
			return jobActionError(http.StatusServiceUnavailable, "job_action_receipt_capacity", "The durable job-action intent journal is full.", "Resolve or replay pending Admin actions before submitting another mutation.")
		}
		pending := receipt
		pending.Outcome, pending.JobStatus = "pending", job.Status
		m.receipts[keyHash] = jobActionReceiptDisk{KeyHash: keyHash, IntentHash: intentHash, Receipt: pending, RetrySource: retrySourceFromJob(job, req.Collection)}
		m.pruneLocked()
		if err := m.saveLocked(); err != nil {
			delete(m.receipts, keyHash)
			return err
		}
		return nil
	}
	switch action {
	case "cancel":
		if job.Status != JobStatusQueued && job.Status != JobStatusRunning {
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusConflict, "job_not_active", "Only queued or running jobs can be cancelled.", "Refresh the job and inspect its terminal state.")
		}
		cancelled, found, cancelErr := m.jobs.Cancel(job.ID)
		if cancelErr != nil {
			if errors.Is(cancelErr, ErrJobSnapshotPersistence) {
				return adminhttp.JobActionReceipt{}, jobActionError(http.StatusServiceUnavailable, "repository_docs_cancel_snapshot_failed", "Cancellation is durable and the worker was signalled, but terminal job history could not be saved.", "Refresh the job after service storage is writable; a restart reconciles the terminal state from the durable cancellation tombstone.")
			}
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusServiceUnavailable, "repository_docs_cancel_persist_failed", "Cancellation could not be made durable, so the job was left running.", "Retry cancellation after durable service state is writable.")
		}
		if !found {
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusNotFound, "job_not_retained", "The selected job is no longer retained.", "Return to the bounded job list; cached repository data and audit evidence are unaffected.")
		}
		outcome := "cancellation_requested"
		if cancelled.Status == JobStatusCancelled {
			outcome = "cancelled"
		}
		receipt.ResultJob, receipt.Outcome, receipt.JobStatus = cancelled.ID, outcome, cancelled.Status
	case "retry":
		if !jobTerminalStatus(job.Status) {
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusConflict, "job_not_terminal", "Retry requires a terminal job.", "Wait for completion or cancel active work first.")
		}
		if job.RegistrationID == "" {
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusConflict, "retry_unavailable", "The retained job has no maintenance registration to reconcile.", "Use the maintenance setup CLI for this repository.")
		}
		if preparedReceipt == nil && req.Collection != "" && (job.Type != SyncJobType || !retryableSyncCollection(job, req.Collection)) {
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusConflict, "collection_retry_unavailable", "The selected collection is not a failed terminal collection on this sync job.", "Refresh the job and choose a partial or permanently failed collection.")
		}
		if m.reconcile == nil {
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusNotImplemented, "capability_unavailable", "Retry is not available in the running daemon.", "Use the maintenance CLI for this registration.")
		}
		if err := prepare(); err != nil {
			return adminhttp.JobActionReceipt{}, err
		}
		result, err := m.reconcile(ctx, job.RegistrationID, req.Collection, actionIntentRef)
		if err != nil {
			// Keep the prepared intent. Reconcile can fail after crossing its
			// mutation boundary, so deleting the claim would permit a new key to
			// duplicate work instead of retrying/coalescing this exact intent.
			return adminhttp.JobActionReceipt{}, jobActionError(http.StatusConflict, "retry_failed", "The maintenance registration could not be reconciled.", "Refresh maintenance status and resolve its typed diagnostic.")
		}
		m.applyRetryResult(&receipt, job, result)
	default:
		return adminhttp.JobActionReceipt{}, jobActionError(http.StatusBadRequest, "invalid_action", "Unknown job action.", "Refresh the admin UI.")
	}
	previous, hadPrevious := m.receipts[keyHash]
	m.receipts[keyHash] = jobActionReceiptDisk{KeyHash: keyHash, IntentHash: intentHash, Receipt: receipt}
	m.pruneLocked()
	if err := m.saveLocked(); err != nil {
		// A retry mutation is correlated with its admitted job. Restore the
		// pending intent in memory when terminal receipt persistence fails so a
		// same-process replay follows the same durable recovery path as restart.
		if action == "retry" && hadPrevious {
			m.receipts[keyHash] = previous
		}
		return adminhttp.JobActionReceipt{}, err
	}
	if action == "retry" {
		_ = m.jobs.ReleaseActionIntent(actionIntentRef)
	}
	return receipt, nil
}

func (m *JobActionManager) applyRetryResult(receipt *adminhttp.JobActionReceipt, source Job, result MaintenanceReconcileResult) {
	if len(result.JobsStarted) > 0 {
		started, found := m.jobs.Get(result.JobsStarted[0])
		if found {
			receipt.ResultJob, receipt.Outcome, receipt.JobStatus = started.ID, "created", started.Status
		} else {
			receipt.ResultJob, receipt.Outcome, receipt.JobStatus = result.JobsStarted[0], "created", "not_retained"
		}
	} else if len(result.JobsCoalesced) > 0 {
		coalesced, found := m.jobs.Get(result.JobsCoalesced[0])
		if found {
			receipt.ResultJob, receipt.Outcome, receipt.JobStatus = coalesced.ID, "coalesced", coalesced.Status
		} else {
			receipt.ResultJob, receipt.Outcome, receipt.JobStatus = result.JobsCoalesced[0], "coalesced", "not_retained"
		}
	} else {
		receipt.ResultJob, receipt.Outcome, receipt.JobStatus = source.ID, "no_work_needed", source.Status
	}
}

func (m *JobActionManager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	file := jobActionReceiptFile{Schema: jobActionReceiptSchema}
	for _, receipt := range m.receipts {
		file.Receipts = append(file.Receipts, receipt)
	}
	sort.Slice(file.Receipts, func(i, j int) bool {
		if file.Receipts[i].Receipt.CreatedAt.Equal(file.Receipts[j].Receipt.CreatedAt) {
			return file.Receipts[i].KeyHash < file.Receipts[j].KeyHash
		}
		return file.Receipts[i].Receipt.CreatedAt.Before(file.Receipts[j].Receipt.CreatedAt)
	})
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return m.writeFile(m.path, append(data, '\n'), 0o600)
}

func (m *JobActionManager) pruneLocked() {
	if maxJobActionReceipts <= 0 || len(m.receipts) <= maxJobActionReceipts {
		return
	}
	// Pending entries are mutation intents, not disposable history. Retention
	// may evict only settled receipts; admission fails closed when pending
	// intents alone fill the bounded journal.
	settled := make([]jobActionReceiptDisk, 0, len(m.receipts))
	pending := 0
	for _, receipt := range m.receipts {
		if receipt.Receipt.Outcome == "pending" {
			pending++
			continue
		}
		settled = append(settled, receipt)
	}
	sort.Slice(settled, func(i, j int) bool {
		if settled[i].Receipt.CreatedAt.Equal(settled[j].Receipt.CreatedAt) {
			return settled[i].KeyHash < settled[j].KeyHash
		}
		return settled[i].Receipt.CreatedAt.Before(settled[j].Receipt.CreatedAt)
	})
	keepSettled := maxJobActionReceipts - pending
	if keepSettled < 0 {
		keepSettled = 0
	}
	for _, receipt := range settled[:max(0, len(settled)-keepSettled)] {
		delete(m.receipts, receipt.KeyHash)
	}
}

func (m *JobActionManager) pendingCountLocked() int {
	pending := 0
	for _, receipt := range m.receipts {
		if receipt.Receipt.Outcome == "pending" {
			pending++
		}
	}
	return pending
}

func hashJobAction(value string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(hash[:])
}

func retryableSyncCollection(job Job, collection string) bool {
	for _, state := range job.SyncCollections {
		if state.Collection != collection {
			continue
		}
		return state.Outcome == SyncCollectionPartial || state.Outcome == SyncCollectionPermanentFailure
	}
	return false
}

func jobActionError(status int, code, message, remediation string) error {
	return adminhttp.JobActionError{Status: status, Code: code, Message: message, Remediation: remediation}
}
