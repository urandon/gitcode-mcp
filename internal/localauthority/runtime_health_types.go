package localauthority

import (
	"math"
	"time"

	"gitcode-mcp/internal/authority"
)

const MaxRuntimeHealthBytes = 16 << 10

type runtimeBindingKey struct{}

// RuntimeHealthRequest selects an admitted UUID and an independently retained
// owner epoch. The binding is consistency metadata, not a bearer credential.
type RuntimeHealthRequest struct {
	ProjectID      authority.UUID `json:"project_id"`
	RuntimeBinding string         `json:"runtime_binding"`
}

func (r RuntimeHealthRequest) Validate() error {
	if !r.ProjectID.Valid() || !validDigest(r.RuntimeBinding) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type RuntimeTrackerHealth struct {
	State          string  `json:"state"`
	Writer         string  `json:"writer"`
	SourceSequence *uint64 `json:"source_sequence,omitempty"`
}

type RuntimeKnowledgeHealth struct {
	State   string `json:"state"`
	Pending bool   `json:"pending"`
}

type RuntimeProjectionHealth struct {
	State           string  `json:"state"`
	IndexedSequence *uint64 `json:"indexed_sequence,omitempty"`
}

// Fixed fields avoid unbounded job identifiers or caller labels in health.
type RuntimeJobCounts struct {
	Queued      int `json:"queued"`
	Running     int `json:"running"`
	Cancelling  int `json:"cancelling"`
	Cancelled   int `json:"cancelled"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Interrupted int `json:"interrupted"`
}

func (c RuntimeJobCounts) valid(limit int) bool {
	total := 0
	for _, n := range []int{c.Queued, c.Running, c.Cancelling, c.Cancelled, c.Succeeded, c.Failed, c.Interrupted} {
		if n < 0 || n > limit {
			return false
		}
		total += n
	}
	return total <= limit && c.Queued+c.Running+c.Cancelling <= 1
}

func (c *RuntimeJobCounts) add(state string) bool {
	switch state {
	case "queued":
		c.Queued++
	case "running":
		c.Running++
	case "cancelling":
		c.Cancelling++
	case "cancelled":
		c.Cancelled++
	case "succeeded":
		c.Succeeded++
	case "failed":
		c.Failed++
	case "interrupted":
		c.Interrupted++
	default:
		return false
	}
	return true
}

type RuntimeJobsHealth struct {
	State        string            `json:"state"`
	Counts       *RuntimeJobCounts `json:"counts,omitempty"`
	WorkerActive bool              `json:"worker_active"`
}

func (v RuntimeJobsHealth) valid(limit int) bool {
	switch v.State {
	case "ready":
		return v.Counts != nil && v.Counts.valid(limit)
	case "missing", "corrupt", "uncertain", "unavailable":
		return v.Counts == nil
	}
	return false
}

// RuntimeHealthView is current owner evidence, not retained observation health.
// Qualified sequence zero is represented by a nonnil pointer, not absence.
type RuntimeHealthView struct {
	Schema         int                     `json:"schema"`
	ProjectID      authority.UUID          `json:"project_id"`
	RuntimeBinding string                  `json:"runtime_binding"`
	RetrievedAt    string                  `json:"retrieved_at"`
	Phase          string                  `json:"phase"`
	Tracker        RuntimeTrackerHealth    `json:"tracker"`
	Knowledge      RuntimeKnowledgeHealth  `json:"knowledge"`
	Projection     RuntimeProjectionHealth `json:"projection"`
	RecoveryJobs   RuntimeJobsHealth       `json:"recovery_jobs"`
	IndexJobs      RuntimeJobsHealth       `json:"index_jobs"`
	Repair         string                  `json:"repair"`
}

func (v RuntimeHealthView) Validate() error {
	invalid := authority.Error{Code: authority.InvalidContract}
	if v.Schema != 1 || !v.ProjectID.Valid() || !validDigest(v.RuntimeBinding) || v.Phase != "starting" && v.Phase != "serving" && v.Phase != "not_serving" {
		return invalid
	}
	at, err := time.Parse(time.RFC3339Nano, v.RetrievedAt)
	if err != nil || at.IsZero() || at.UTC().Format(time.RFC3339Nano) != v.RetrievedAt {
		return invalid
	}
	switch v.Tracker.State {
	case "ready":
		if v.Tracker.Writer != "owned" || v.Tracker.SourceSequence == nil {
			return invalid
		}
	case "recovery_required":
		if v.Tracker.Writer != "quiesced" || v.Tracker.SourceSequence == nil {
			return invalid
		}
	case "read_only":
		if v.Tracker.Writer != "unavailable" || v.Tracker.SourceSequence == nil {
			return invalid
		}
	case "corrupt", "unavailable":
		if v.Tracker.Writer != "unavailable" || v.Tracker.SourceSequence != nil {
			return invalid
		}
	default:
		return invalid
	}
	if v.Tracker.SourceSequence != nil && *v.Tracker.SourceSequence > math.MaxInt64 {
		return invalid
	}
	if v.Tracker.SourceSequence == nil && v.Knowledge.State != "unavailable" {
		return invalid
	}
	if v.Tracker.SourceSequence == nil && (v.RecoveryJobs.State == "ready" || v.IndexJobs.State == "ready" || v.Repair == "ready" || v.Repair == "pending") {
		return invalid
	}
	switch v.Knowledge.State {
	case "ready", "recovery_required":
	case "uninitialized", "migration_required", "reserved", "bound", "corrupt", "capacity", "unavailable":
		if v.Knowledge.Pending {
			return invalid
		}
	default:
		return invalid
	}
	switch v.Projection.State {
	case "current", "stale":
		if v.Projection.IndexedSequence == nil || v.Tracker.SourceSequence == nil || *v.Projection.IndexedSequence > *v.Tracker.SourceSequence {
			return invalid
		}
		if (v.Projection.State == "current") != (*v.Projection.IndexedSequence == *v.Tracker.SourceSequence) {
			return invalid
		}
	case "missing", "corrupt", "uncertain", "restart_required":
		if v.Projection.IndexedSequence != nil {
			return invalid
		}
	default:
		return invalid
	}
	if !v.RecoveryJobs.valid(MaxRecoveryJobs) || !v.IndexJobs.valid(maxIndexJobs) {
		return invalid
	}
	switch v.Repair {
	case "ready", "missing", "pending", "restart_required", "corrupt", "uncertain":
	default:
		return invalid
	}
	if v.Repair == "restart_required" && v.Projection.State != "restart_required" {
		return invalid
	}
	if v.Repair == "pending" && v.Projection.State != "uncertain" {
		return invalid
	}
	return nil
}

func VerifyRuntimeHealth(req RuntimeHealthRequest, v RuntimeHealthView) error {
	if req.Validate() != nil || v.Validate() != nil || v.ProjectID != req.ProjectID || v.RuntimeBinding != req.RuntimeBinding {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}
