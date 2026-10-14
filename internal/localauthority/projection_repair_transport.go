package localauthority

import (
	"context"
	"encoding/json"

	"gitcode-mcp/internal/authority"
)

const MaxProjectionRepairHistoryBytes = 8 << 10

type ProjectionRepairPlanRequest struct {
	ProjectID authority.UUID `json:"project_id"`
}

func (r ProjectionRepairPlanRequest) Validate() error {
	if !r.ProjectID.Valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type ProjectionRepairLookup struct {
	ProjectID authority.UUID `json:"project_id"`
	RepairID  authority.UUID `json:"repair_id"`
}

func (r ProjectionRepairLookup) Validate() error {
	if !r.ProjectID.Valid() || !r.RepairID.Valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// These digests bind response consistency, not peer authority. Native same-user
// authentication is still required; no envelope can grant execution itself.
type ProjectionRepairPlanResult struct {
	Schema int                  `json:"schema"`
	Digest string               `json:"digest"`
	Plan   ProjectionRepairPlan `json:"plan"`
}

func (r ProjectionRepairPlanResult) Validate() error {
	if r.Schema != 1 || !validDigest(r.Digest) || r.Plan.Validate() != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type ProjectionRepairConfirmation struct {
	Schema int                  `json:"schema"`
	Digest string               `json:"digest"`
	Repair ProjectionRepairView `json:"repair"`
}

func (r ProjectionRepairConfirmation) Validate() error {
	if r.Schema != 1 || !validDigest(r.Digest) || r.Repair.Validate() != nil || r.Repair.State != "confirmed" {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func repairResponseDigest(domain string, request, response any) string {
	body, _ := json.Marshal(struct {
		Request  any `json:"request"`
		Response any `json:"response"`
	}{request, response})
	return hash(append([]byte("authority.v1/projection_repair/"+domain+"\x00"), body...))
}

func VerifyProjectionRepairPlan(req ProjectionRepairPlanRequest, result ProjectionRepairPlanResult) error {
	if req.Validate() != nil || result.Validate() != nil || result.Plan.ProjectID != req.ProjectID || result.Digest != repairResponseDigest("plan", req, result.Plan) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func VerifyProjectionRepairConfirmation(req ProjectionRepairRequest, result ProjectionRepairConfirmation) error {
	if req.Validate() != nil || result.Validate() != nil || result.Repair.ProjectID != req.ProjectID || result.Repair.PlanID != req.PlanID || result.Digest != repairResponseDigest("apply", req, result.Repair) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (c *Coordinator) InspectProjectionRepair(ctx context.Context, req ProjectionRepairPlanRequest) (ProjectionRepairPlanResult, error) {
	if req.Validate() != nil {
		return ProjectionRepairPlanResult{}, authority.Error{Code: authority.InvalidContract}
	}
	plan, err := c.PlanProjectionRepair(ctx, req.ProjectID)
	if err != nil {
		return ProjectionRepairPlanResult{}, err
	}
	return ProjectionRepairPlanResult{Schema: 1, Digest: repairResponseDigest("plan", req, plan), Plan: plan}, nil
}

func (c *Coordinator) ConfirmProjectionRepair(ctx context.Context, req ProjectionRepairRequest, actor Actor) (ProjectionRepairConfirmation, error) {
	view, err := c.ApplyProjectionRepair(ctx, req, actor)
	if err != nil {
		return ProjectionRepairConfirmation{}, err
	}
	return ProjectionRepairConfirmation{Schema: 1, Digest: repairResponseDigest("apply", req, view), Repair: view}, nil
}

// All retained repairs fit one atomic bounded view. No pagination, row filter,
// cursor or caller-selected snapshot can silently omit pending original intent.
type ProjectionRepairHistory struct {
	Schema         int                    `json:"schema"`
	ProjectID      authority.UUID         `json:"project_id"`
	State          string                 `json:"state"`
	Generation     uint64                 `json:"generation"`
	SnapshotDigest string                 `json:"snapshot_digest"`
	Retained       int                    `json:"retained"`
	Repairs        []ProjectionRepairView `json:"repairs"`
}

func repairHistoryDigest(h ProjectionRepairHistory) string {
	h.SnapshotDigest = ""
	body, _ := json.Marshal(h)
	return hash(append([]byte("authority.v1/projection_repair/history\x00"), body...))
}

func (h ProjectionRepairHistory) Validate() error {
	invalid := authority.Error{Code: authority.InvalidContract}
	if h.Schema != 1 || !h.ProjectID.Valid() || h.Repairs == nil || h.Retained != len(h.Repairs) || h.Retained < 0 || h.Retained > maxProjectionRepairs {
		return invalid
	}
	if h.State == "missing" {
		if h.Generation != 0 || h.SnapshotDigest != "" || h.Retained != 0 {
			return invalid
		}
		return nil
	}
	if h.State != "ready" || h.Retained == 0 || h.Generation < 1 || h.Generation > 3*maxProjectionRepairs || !validDigest(h.SnapshotDigest) {
		return invalid
	}
	ids, plans := map[authority.UUID]bool{}, map[string]bool{}
	var generation, sequence uint64
	for i, v := range h.Repairs {
		if v.Validate() != nil || v.ProjectID != h.ProjectID || v.Replayed || ids[v.RepairID] || plans[v.PlanID] || v.Sequence < sequence || i < len(h.Repairs)-1 && v.State != "confirmed" {
			return invalid
		}
		ids[v.RepairID], plans[v.PlanID] = true, true
		generation, sequence = generation+v.Generation, v.Sequence
	}
	body, err := json.Marshal(h)
	if err != nil || len(body) > MaxProjectionRepairHistoryBytes || generation != h.Generation || repairHistoryDigest(h) != h.SnapshotDigest {
		return invalid
	}
	return nil
}

func VerifyProjectionRepairHistory(req ProjectionRepairPlanRequest, result ProjectionRepairHistory) error {
	if req.Validate() != nil || result.Validate() != nil || result.ProjectID != req.ProjectID {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (c *Coordinator) ProjectionRepairs(ctx context.Context, req ProjectionRepairPlanRequest) (ProjectionRepairHistory, error) {
	var zero ProjectionRepairHistory
	if req.Validate() != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	_, call, release, err := c.begin(ctx, req.ProjectID)
	if err != nil {
		return zero, err
	}
	defer release()
	q := c.projects[req.ProjectID].repair
	if q == nil {
		return zero, Error{RepairUncertain}
	}
	if err = q.lock(call); err != nil {
		return zero, err
	}
	defer q.mu.Unlock()
	if _, _, err = q.inventory(call); err != nil {
		return zero, err
	}
	history := ProjectionRepairHistory{Schema: 1, ProjectID: req.ProjectID, State: "missing", Repairs: []ProjectionRepairView{}}
	if q.state.Generation != 0 {
		history.State, history.Generation = "ready", q.state.Generation
		for _, r := range q.state.Records {
			history.Repairs = append(history.Repairs, repairView(r, false))
		}
		history.Retained = len(history.Repairs)
		history.SnapshotDigest = repairHistoryDigest(history)
	}
	if call.Err() != nil {
		return zero, call.Err()
	}
	if history.Validate() != nil {
		return zero, Error{RepairCorrupt}
	}
	return history, nil
}
