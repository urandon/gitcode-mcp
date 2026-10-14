package localauthority

import (
	"encoding/json"
	"math"
	"reflect"

	"gitcode-mcp/internal/authority"
)

const maxProjectionRepairs = 4
const maxRepairSnapshotBytes = 64 << 10
const maxRepairStages = 4
const maxRepairStageBytes = maxRepairStages * maxRepairSnapshotBytes

// Shared explicit input, never a path or caller-supplied destination.
type ProjectionRepairRequest struct {
	ProjectID      authority.UUID `json:"project_id"`
	PlanID         string         `json:"plan_id"`
	IdempotencyKey string         `json:"idempotency_key"`
	WriteMode      string         `json:"write_mode"`
}

func (r ProjectionRepairRequest) Validate() error {
	if !validDigest(r.PlanID) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return (authority.WriteIntent{ProjectID: r.ProjectID, WriteMode: r.WriteMode, IdempotencyKey: r.IdempotencyKey}).Validate(true)
}

type ProjectionRepairView struct {
	Schema          int            `json:"schema"`
	ProjectID       authority.UUID `json:"project_id"`
	RepairID        authority.UUID `json:"repair_id"`
	PlanID          string         `json:"plan_id"`
	Sequence        uint64         `json:"sequence"`
	State           string         `json:"state"`
	Generation      uint64         `json:"generation"`
	Files           int            `json:"files"`
	Bytes           int64          `json:"bytes"`
	RestartRequired bool           `json:"restart_required"`
	Replayed        bool           `json:"replayed"`
}

func (v ProjectionRepairView) Validate() error {
	phase := repairPhase(v.State)
	if v.Schema != 1 || !v.ProjectID.Valid() || !v.RepairID.Valid() || !validDigest(v.PlanID) || v.Sequence > math.MaxInt64 || phase == 0 || v.Generation != phase || !v.RestartRequired || v.Files < 0 || v.Files > MaxProjectionStages+1 || v.Bytes < 0 || v.Bytes > projectionRepairByteLimit || v.Bytes > int64(v.Files)*MaxProjectionBytes {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func repairPhase(s string) uint64 {
	switch s {
	case "prepared":
		return 1
	case "quarantined":
		return 2
	case "confirmed":
		return 3
	}
	return 0
}

type projectionRepairRecord struct {
	ID                    authority.UUID       `json:"id"`
	KeyHash               string               `json:"key_hash"`
	IntentHash            string               `json:"intent_hash"`
	ActorHash             string               `json:"actor_hash"`
	Plan                  ProjectionRepairPlan `json:"plan"`
	RootIdentity          string               `json:"root_identity"`
	ProjectionIdentity    string               `json:"projection_identity"`
	ProjectionFingerprint string               `json:"projection_fingerprint"`
	IndexIdentity         string               `json:"index_identity"`
	IndexDigest           string               `json:"index_digest"`
	State                 string               `json:"state"`
	Generation            uint64               `json:"generation"`
}

func (r projectionRepairRecord) valid() bool {
	if !r.ID.Valid() || !validDigest(r.KeyHash) || !validDigest(r.IntentHash) || !validDigest(r.ActorHash) || r.Plan.Validate() != nil || r.Plan.State != "ready" || !validDigest(r.RootIdentity) || !validDigest(r.ProjectionIdentity) || !validDigest(r.ProjectionFingerprint) || repairPhase(r.State) == 0 || repairPhase(r.State) != r.Generation {
		return false
	}
	_, expectedIntent := repairIntentHashes(ProjectionRepairRequest{ProjectID: r.Plan.ProjectID, PlanID: r.Plan.PlanID})
	if r.IntentHash != expectedIntent {
		return false
	}
	if r.Plan.LedgerState == "missing" {
		return r.IndexIdentity == "" && r.IndexDigest == ""
	}
	return validDigest(r.IndexIdentity) && validDigest(r.IndexDigest)
}

func repairView(r projectionRepairRecord, replay bool) ProjectionRepairView {
	return ProjectionRepairView{Schema: 1, ProjectID: r.Plan.ProjectID, RepairID: r.ID, PlanID: r.Plan.PlanID, Sequence: r.Plan.Sequence, State: r.State, Generation: r.Generation, Files: r.Plan.Files, Bytes: r.Plan.Bytes, RestartRequired: true, Replayed: replay}
}

func repairIntentHashes(r ProjectionRepairRequest) (string, string) {
	key := hash([]byte(string(r.ProjectID) + "\x00projection-repair\x00" + r.IdempotencyKey))
	body, _ := json.Marshal(struct {
		Project authority.UUID
		Plan    string
	}{r.ProjectID, r.PlanID})
	return key, hash(body)
}

type projectionRepairSnapshot struct {
	Schema          int                      `json:"schema"`
	ProjectID       authority.UUID           `json:"project_id"`
	TrackerInstance authority.UUID           `json:"tracker_instance"`
	ManifestHash    string                   `json:"manifest_hash"`
	Generation      uint64                   `json:"generation"`
	Records         []projectionRepairRecord `json:"records"`
}

func (s projectionRepairSnapshot) valid() bool {
	if s.Schema != 1 || !s.ProjectID.Valid() || !s.TrackerInstance.Valid() || !validDigest(s.ManifestHash) || s.Generation < 1 || s.Generation > math.MaxInt64 || len(s.Records) < 1 || len(s.Records) > maxProjectionRepairs {
		return false
	}
	keys := map[string]bool{}
	ids := map[authority.UUID]bool{}
	plans := map[string]bool{}
	images := map[string]bool{}
	pending := 0
	generations := uint64(0)
	for i, r := range s.Records {
		if !r.valid() || r.Plan.ProjectID != s.ProjectID || keys[r.KeyHash] || ids[r.ID] || plans[r.Plan.PlanID] || images[r.ProjectionIdentity] {
			return false
		}
		keys[r.KeyHash], ids[r.ID] = true, true
		plans[r.Plan.PlanID], images[r.ProjectionIdentity] = true, true
		generations += r.Generation
		if r.State != "confirmed" {
			if i != len(s.Records)-1 {
				return false
			}
			pending++
		}
		if i > 0 && (r.Plan.Sequence < s.Records[i-1].Plan.Sequence || r.Plan.LedgerGeneration < s.Records[i-1].Plan.LedgerGeneration) {
			return false
		}
	}
	return pending <= 1 && s.Generation == generations
}

type projectionRepairEnvelope struct {
	State  projectionRepairSnapshot `json:"state"`
	Digest string                   `json:"digest"`
}

func (e projectionRepairEnvelope) Validate() error {
	body, err := json.Marshal(e.State)
	if err != nil || !e.State.valid() || hash(body) != e.Digest {
		return Error{RepairCorrupt}
	}
	return nil
}

// Conserves original plans and intent. One causal phase/admission per update.
func repairSnapshotTransition(before, after projectionRepairSnapshot) bool {
	if !after.valid() {
		return false
	}
	if before.Generation == 0 {
		return after.Generation == 1 && len(after.Records) == 1 && after.Records[0].State == "prepared"
	}
	if !before.valid() || after.Generation != before.Generation+1 || after.ProjectID != before.ProjectID || after.TrackerInstance != before.TrackerInstance || after.ManifestHash != before.ManifestHash || len(after.Records) < len(before.Records) || len(after.Records) > len(before.Records)+1 {
		return false
	}
	changed := 0
	for i, old := range before.Records {
		next := after.Records[i]
		if reflect.DeepEqual(old, next) {
			continue
		}
		if old.State == "confirmed" || next.Generation != old.Generation+1 || repairPhase(next.State) != next.Generation {
			return false
		}
		next.State, next.Generation = old.State, old.Generation
		if !reflect.DeepEqual(old, next) {
			return false
		}
		changed++
	}
	if len(after.Records) > len(before.Records) {
		for _, old := range before.Records {
			if old.State != "confirmed" {
				return false
			}
		}
		if after.Records[len(before.Records)].State != "prepared" {
			return false
		}
		changed++
	}
	return changed == 1
}
