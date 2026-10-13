package localauthority

import (
	"encoding/json"
	"math"
	"reflect"

	"gitcode-mcp/internal/authority"
)

const maxIndexJobs = 128
const maxIndexAttempts = 8

// Private durable selection, not caller-provided catalog JSON. A coordinator
// records this exact intake before any segmented projection work is admitted.
// The build UUID is also the job UUID, so post-publication recovery is exact.
type indexSelection struct {
	ProjectID       authority.UUID `json:"project_id"`
	TrackerInstance authority.UUID `json:"tracker_instance"`
	ManifestHash    string         `json:"manifest_hash"`
	Namespace       string         `json:"namespace"`
	Sequence        uint64         `json:"sequence"`
	Epoch           authority.UUID `json:"epoch"`
	Generation      uint64         `json:"generation"`
	BuildID         authority.UUID `json:"build_id"`
	BaseDigest      string         `json:"base_digest"`
}

func (s indexSelection) valid() bool {
	return s.ProjectID.Valid() && s.TrackerInstance.Valid() && validDigest(s.ManifestHash) && s.Namespace == projectionNamespace && s.Sequence <= math.MaxInt64 && s.Epoch.Valid() && s.BuildID.Valid() && s.Generation >= 1 && s.Generation <= math.MaxInt64 && (s.Generation == 1 && s.BaseDigest == "" || s.Generation > 1 && validDigest(s.BaseDigest))
}

// A proof identifies the complete catalog bytes, never a source mutation,
// embedding readiness, wall-clock approximation or worker's error string.
type indexPublication struct {
	Selection indexSelection `json:"selection"`
	Digest    string         `json:"digest"`
	Entries   int            `json:"entries"`
}

func (p indexPublication) valid() bool {
	return p.Selection.valid() && validDigest(p.Digest) && p.Entries >= 0 && p.Entries <= MaxProjectionEntries && uint64(p.Entries) <= p.Selection.Sequence
}

type indexJobRecord struct {
	Selection             indexSelection    `json:"selection"`
	KeyHash               string            `json:"key_hash"`
	IntentHash            string            `json:"intent_hash"`
	ActorHash             string            `json:"actor_hash"`
	ActionActorHash       string            `json:"action_actor_hash"`
	Generation            uint64            `json:"generation"`
	AdmittedGeneration    uint64            `json:"admitted_generation"`
	State                 string            `json:"state"`
	Attempts              int               `json:"attempts"`
	CancellationRequested bool              `json:"cancellation_requested"`
	CancelFrom            uint64            `json:"cancel_from"`
	RetryFrom             uint64            `json:"retry_from"`
	Code                  string            `json:"code"`
	Publication           *indexPublication `json:"publication,omitempty"`
}

func indexJobCode(code string) bool {
	switch code {
	case "", "cancelled", "interrupted", "timeout", "operation_failed", "outcome_uncertain",
		string(ProjectionCorrupt), string(ProjectionUncertain), string(Capacity),
		string(DurabilityUnavailable), string(authority.Conflict):
		return true
	}
	return false
}

func (r indexJobRecord) valid() bool {
	if !r.Selection.valid() || !validDigest(r.KeyHash) || !validDigest(r.IntentHash) || !validDigest(r.ActorHash) || r.ActionActorHash != "" && !validDigest(r.ActionActorHash) || r.Generation < 1 || r.Generation > math.MaxInt64 || r.AdmittedGeneration < 2 || r.AdmittedGeneration > math.MaxInt64 || r.Attempts < 0 || r.Attempts > maxIndexAttempts || r.Generation < uint64(r.Attempts)+1 || r.CancelFrom >= r.Generation || r.RetryFrom >= r.Generation || !indexJobCode(r.Code) {
		return false
	}
	if (r.CancelFrom > 0 || r.RetryFrom > 0) && r.ActionActorHash == "" || r.CancellationRequested && r.CancelFrom == 0 {
		return false
	}
	if r.State != "succeeded" && r.Publication != nil || r.State != "queued" && r.Generation < 2 {
		return false
	}
	switch r.State {
	case "queued":
		return r.Code == "" && !r.CancellationRequested && r.Attempts < maxIndexAttempts
	case "running":
		return r.Code == "" && !r.CancellationRequested && r.Attempts > 0
	case "cancelling":
		return r.Code == "" && r.CancellationRequested && r.Attempts > 0
	case "succeeded":
		return r.Code == "" && r.Attempts > 0 && r.Publication != nil && r.Publication.valid() && r.Publication.Selection == r.Selection
	case "cancelled":
		return r.Code == "cancelled" && r.CancellationRequested
	case "failed", "interrupted":
		return r.Code != "" && r.Code != "cancelled" && r.Attempts > 0
	}
	return false
}

func cloneIndexJob(r indexJobRecord) indexJobRecord {
	if r.Publication != nil {
		p := *r.Publication
		r.Publication = &p
	}
	return r
}

// The final catalog is checked by its owner before producing this proof.
// This pure identity check cannot itself qualify filesystem durability.
func indexPublicationProof(selected indexSelection, catalog projectionCatalog, digest string) (indexPublication, error) {
	if !selected.valid() || !catalog.valid() || catalog.ProjectID != selected.ProjectID || catalog.TrackerInstance != selected.TrackerInstance || catalog.ManifestHash != selected.ManifestHash || catalog.Namespace != selected.Namespace || catalog.Sequence != selected.Sequence || catalog.Epoch != selected.Epoch || catalog.Generation != selected.Generation || catalog.BuildID != selected.BuildID || !validDigest(digest) {
		return indexPublication{}, Error{ProjectionCorrupt}
	}
	state, err := json.Marshal(catalog)
	if err != nil {
		return indexPublication{}, Error{ProjectionCorrupt}
	}
	body, err := json.Marshal(projectionEnvelope{Catalog: catalog, Digest: hash(state)})
	if err != nil || hash(body) != digest {
		return indexPublication{}, Error{ProjectionCorrupt}
	}
	return indexPublication{Selection: selected, Digest: digest, Entries: len(catalog.Entries)}, nil
}

// Every transition conserves original intent. Explicit retry clears the active
// cancellation flag but retains its last cancellation generation; shutdown or
// proof-only reconciliation cannot manufacture a new attempt authorization.
func indexJobTransition(before, after indexJobRecord) bool {
	if !before.valid() || !after.valid() || after.Selection != before.Selection || after.KeyHash != before.KeyHash || after.IntentHash != before.IntentHash || after.ActorHash != before.ActorHash || after.AdmittedGeneration != before.AdmittedGeneration {
		return false
	}
	if reflect.DeepEqual(before, after) {
		return true
	}
	if before.Generation == math.MaxInt64 || after.Generation != before.Generation+1 || before.State == "succeeded" {
		return false
	}
	start, cancel, retry := false, false, false
	switch before.State {
	case "queued":
		start = after.State == "running"
		cancel = after.State == "cancelled"
		if !start && !cancel {
			return false
		}
	case "running":
		start = after.State == "running" // verified owner restart
		cancel = after.State == "cancelling"
		if !start && !cancel && after.State != "succeeded" && after.State != "failed" && after.State != "interrupted" {
			return false
		}
	case "cancelling":
		if after.State != "succeeded" && after.State != "cancelled" && after.State != "interrupted" {
			return false
		}
	case "failed", "interrupted", "cancelled":
		retry = after.State == "queued"
		if !retry && after.State != "succeeded" {
			return false
		}
	default:
		return false
	}
	wantAttempts := before.Attempts
	if start {
		wantAttempts++
	}
	if after.Attempts != wantAttempts {
		return false
	}
	if cancel {
		return after.CancelFrom == before.Generation && after.RetryFrom == before.RetryFrom && after.CancellationRequested && after.ActionActorHash != ""
	}
	if retry {
		return after.RetryFrom == before.Generation && after.CancelFrom == before.CancelFrom && !after.CancellationRequested && after.ActionActorHash != ""
	}
	return after.CancelFrom == before.CancelFrom && after.RetryFrom == before.RetryFrom && after.CancellationRequested == before.CancellationRequested && after.ActionActorHash == before.ActionActorHash
}

type indexJobSnapshot struct {
	Schema          int              `json:"schema"`
	ProjectID       authority.UUID   `json:"project_id"`
	TrackerInstance authority.UUID   `json:"tracker_instance"`
	ManifestHash    string           `json:"manifest_hash"`
	Generation      uint64           `json:"generation"`
	Jobs            []indexJobRecord `json:"jobs"`
}

func (s indexJobSnapshot) valid() bool {
	if s.Schema != 1 || !s.ProjectID.Valid() || !s.TrackerInstance.Valid() || !validDigest(s.ManifestHash) || s.Generation < 1 || s.Generation > math.MaxInt64 || s.Jobs == nil || len(s.Jobs) > maxIndexJobs || uint64(len(s.Jobs)) >= s.Generation {
		return false
	}
	var last authority.UUID
	keys := map[string]bool{}
	active := 0
	for _, r := range s.Jobs {
		if !r.valid() || r.Selection.ProjectID != s.ProjectID || r.Selection.TrackerInstance != s.TrackerInstance || r.Selection.ManifestHash != s.ManifestHash || r.Selection.BuildID <= last || r.AdmittedGeneration > s.Generation || r.Generation >= s.Generation || r.Generation > s.Generation-r.AdmittedGeneration+1 || keys[r.KeyHash] {
			return false
		}
		last, keys[r.KeyHash] = r.Selection.BuildID, true
		if r.State == "queued" || r.State == "running" || r.State == "cancelling" {
			active++
		}
	}
	return active <= 1
}

type indexJobEnvelope struct {
	State  indexJobSnapshot `json:"state"`
	Digest string           `json:"digest"`
}

func (e indexJobEnvelope) Validate() error {
	body, err := json.Marshal(e.State)
	if err != nil || !e.State.valid() || hash(body) != e.Digest {
		return Error{IndexJobsCorrupt}
	}
	return nil
}

// Ledger publication retains every admitted identity. A later explicit
// bounded-retention protocol may retire proved-safe work; generic lifecycle
// updates cannot silently discard old caller-key or cancellation evidence.
func indexSnapshotTransition(before, after indexJobSnapshot) bool {
	if !after.valid() {
		return false
	}
	if before.Generation == 0 {
		return after.Generation == 1 && len(after.Jobs) == 0
	}
	if !before.valid() || before.Generation == math.MaxInt64 || after.Generation != before.Generation+1 || after.ProjectID != before.ProjectID || after.TrackerInstance != before.TrackerInstance || after.ManifestHash != before.ManifestHash {
		return false
	}
	for _, old := range before.Jobs {
		found := false
		for _, next := range after.Jobs {
			if next.Selection.BuildID == old.Selection.BuildID {
				if !indexJobTransition(old, next) {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, next := range after.Jobs {
		found := false
		for _, old := range before.Jobs {
			found = found || old.Selection.BuildID == next.Selection.BuildID
		}
		if !found && (next.State != "queued" || next.Generation != 1 || next.AdmittedGeneration != after.Generation || next.Attempts != 0 || next.CancellationRequested || next.CancelFrom != 0 || next.RetryFrom != 0 || next.ActionActorHash != "") {
			return false
		}
	}
	return true
}
