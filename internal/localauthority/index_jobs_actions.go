package localauthority

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"sort"

	"gitcode-mcp/internal/authority"
)

// Internal transport contracts remain unadvertised until owned execution and
// exact protected-response verification are wired into the runtime cohort.
type IndexJobRequest struct {
	ProjectID      authority.UUID `json:"project_id"`
	Sequence       uint64         `json:"sequence"`
	IdempotencyKey string         `json:"idempotency_key"`
	WriteMode      string         `json:"write_mode"`
}

func (r IndexJobRequest) Validate() error {
	if r.Sequence > math.MaxInt64 {
		return authority.Error{Code: authority.InvalidContract}
	}
	return (authority.WriteIntent{ProjectID: r.ProjectID, WriteMode: r.WriteMode, IdempotencyKey: r.IdempotencyKey}).Validate(true)
}

type IndexJobAction struct {
	ProjectID          authority.UUID `json:"project_id"`
	JobID              authority.UUID `json:"job_id"`
	ExpectedGeneration uint64         `json:"expected_generation"`
	WriteMode          string         `json:"write_mode"`
}

func (r IndexJobAction) Validate() error {
	if !r.ProjectID.Valid() || !r.JobID.Valid() || r.ExpectedGeneration < 1 || r.ExpectedGeneration > math.MaxInt64 || r.WriteMode != "local" {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// Public protected view intentionally excludes caller/actor hashes, base file
// selectors and original request bytes. It certifies metadata only, not vectors.
type IndexJobView struct {
	Schema            int            `json:"schema"`
	ProjectID         authority.UUID `json:"project_id"`
	JobID             authority.UUID `json:"job_id"`
	Type              string         `json:"type"`
	State             string         `json:"state"`
	Generation        uint64         `json:"generation"`
	Sequence          uint64         `json:"sequence"`
	Attempts          int            `json:"attempts"`
	Code              string         `json:"code"`
	CatalogGeneration uint64         `json:"catalog_generation"`
	Published         bool           `json:"published"`
}

func (v IndexJobView) Validate() error {
	if v.Schema != 1 || !v.ProjectID.Valid() || !v.JobID.Valid() || v.Type != "content_catalog" || !indexJobGenerationValid(v.State, v.Attempts, v.Generation) || v.Sequence > math.MaxInt64 || v.CatalogGeneration < 1 || v.CatalogGeneration > math.MaxInt64 || !indexJobCode(v.Code) || v.Published != (v.State == "succeeded") {
		return authority.Error{Code: authority.InvalidContract}
	}
	switch v.State {
	case "queued":
		if v.Code != "" || v.Attempts >= maxIndexAttempts {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "running", "cancelling", "succeeded":
		if v.Code != "" || v.Attempts == 0 || v.Generation < 2 {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "cancelled":
		if v.Code != "cancelled" || v.Generation < 2 {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "failed", "interrupted":
		if v.Code == "" || v.Code == "cancelled" || v.Attempts == 0 || v.Generation < 2 {
			return authority.Error{Code: authority.InvalidContract}
		}
	default:
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func indexJobView(r indexJobRecord) IndexJobView {
	return IndexJobView{Schema: 1, ProjectID: r.Selection.ProjectID, JobID: r.Selection.BuildID, Type: "content_catalog", State: r.State, Generation: r.Generation, Sequence: r.Selection.Sequence, Attempts: r.Attempts, Code: r.Code, CatalogGeneration: r.Selection.Generation, Published: r.Publication != nil}
}

func indexIntentHashes(r IndexJobRequest) (key, intent string) {
	key = hash([]byte(string(r.ProjectID) + "\x00index\x00" + r.IdempotencyKey))
	body, _ := json.Marshal(struct {
		ProjectID authority.UUID
		Sequence  uint64
		Namespace string
	}{r.ProjectID, r.Sequence, projectionNamespace})
	return key, hash(body)
}

func cloneIndexSnapshot(s indexJobSnapshot) indexJobSnapshot {
	s.Jobs = append([]indexJobRecord{}, s.Jobs...)
	for i, r := range s.Jobs {
		s.Jobs[i] = cloneIndexJob(r)
	}
	return s
}

func (q *indexQueue) lock(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.terminalUncertain.Load() {
		return Error{IndexJobsUncertain}
	}
	if !q.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	if q.fence != "" || q.closed || q.terminalUncertain.Load() {
		code := q.fence
		if code == "" {
			code = IndexJobsUncertain
		}
		q.mu.Unlock()
		return Error{code}
	}
	if err := q.verifyExpected(); err != nil {
		q.fence = IndexJobsCorrupt
		q.mu.Unlock()
		return err
	}
	return nil
}

func (q *indexQueue) retained(ctx context.Context, id authority.UUID) (indexJobRecord, bool, error) {
	if err := q.lock(ctx); err != nil {
		return indexJobRecord{}, false, err
	}
	defer q.mu.Unlock()
	for _, r := range q.state.Jobs {
		if r.Selection.BuildID == id {
			return cloneIndexJob(r), true, nil
		}
	}
	return indexJobRecord{}, false, nil
}

func (q *indexQueue) byKey(ctx context.Context, req IndexJobRequest) (indexJobRecord, bool, error) {
	if err := req.Validate(); err != nil {
		return indexJobRecord{}, false, err
	}
	if err := q.lock(ctx); err != nil {
		return indexJobRecord{}, false, err
	}
	defer q.mu.Unlock()
	key, intent := indexIntentHashes(req)
	for _, r := range q.state.Jobs {
		if r.KeyHash == key {
			if r.IntentHash != intent {
				return indexJobRecord{}, false, authority.Error{Code: authority.Conflict}
			}
			return cloneIndexJob(r), true, nil
		}
	}
	return indexJobRecord{}, false, nil
}

func (q *indexQueue) admit(ctx context.Context, req IndexJobRequest, selection indexSelection, actor Actor) (indexJobRecord, error) {
	if req.Validate() != nil || !selection.valid() || selection.ProjectID != req.ProjectID || selection.Sequence != req.Sequence || !actor.valid() {
		return indexJobRecord{}, authority.Error{Code: authority.InvalidContract}
	}
	if err := q.lock(ctx); err != nil {
		return indexJobRecord{}, err
	}
	defer q.mu.Unlock()
	key, intent := indexIntentHashes(req)
	for _, r := range q.state.Jobs {
		if r.KeyHash == key {
			if r.IntentHash != intent {
				return indexJobRecord{}, authority.Error{Code: authority.Conflict}
			}
			return cloneIndexJob(r), nil
		}
	}
	for _, r := range q.state.Jobs {
		if r.State == "queued" || r.State == "running" || r.State == "cancelling" {
			return indexJobRecord{}, authority.Error{Code: authority.Conflict}
		}
	}
	if len(q.state.Jobs) >= maxIndexJobs || q.state.Generation >= math.MaxInt64 {
		return indexJobRecord{}, Error{Capacity}
	}
	if q.state.Generation == 0 {
		empty := indexJobSnapshot{Schema: 1, ProjectID: q.store.manifest.ID, TrackerInstance: q.store.instance, ManifestHash: manifestHash(q.store.manifest), Generation: 1, Jobs: []indexJobRecord{}}
		if err := q.publish(ctx, empty); err != nil {
			return indexJobRecord{}, err
		}
	}
	next := cloneIndexSnapshot(q.state)
	next.Generation++
	r := indexJobRecord{Selection: selection, KeyHash: key, IntentHash: intent, ActorHash: jobActorHash(req.ProjectID, actor), State: "queued", Generation: 1, AdmittedGeneration: next.Generation}
	next.Jobs = append(next.Jobs, r)
	sort.Slice(next.Jobs, func(i, j int) bool { return next.Jobs[i].Selection.BuildID < next.Jobs[j].Selection.BuildID })
	if err := q.publish(ctx, next); err != nil {
		return indexJobRecord{}, err
	}
	return cloneIndexJob(r), nil
}

func (q *indexQueue) transition(ctx context.Context, id authority.UUID, generation uint64, change func(*indexJobRecord), uncertainty *bool) (indexJobRecord, error) {
	if uncertainty != nil {
		*uncertainty = false
	}
	if err := q.lock(ctx); err != nil {
		return indexJobRecord{}, err
	}
	defer q.mu.Unlock()
	if q.state.Generation >= math.MaxInt64 {
		return indexJobRecord{}, Error{Capacity}
	}
	next := cloneIndexSnapshot(q.state)
	for i, before := range next.Jobs {
		if before.Selection.BuildID != id {
			continue
		}
		if before.Generation != generation {
			return indexJobRecord{}, indexGenerationChanged{}
		}
		change(&next.Jobs[i])
		if before.State != "queued" && next.Jobs[i].State == "queued" {
			for j, other := range next.Jobs {
				if j != i && (other.State == "queued" || other.State == "running" || other.State == "cancelling") {
					return indexJobRecord{}, authority.Error{Code: authority.Conflict}
				}
			}
		}
		if reflect.DeepEqual(before, next.Jobs[i]) {
			return cloneIndexJob(before), nil
		}
		next.Jobs[i].Generation++
		next.Generation++
		if err := q.publish(ctx, next); err != nil {
			if uncertainty != nil {
				*uncertainty = q.fence == IndexJobsUncertain
			}
			return indexJobRecord{}, err
		}
		return cloneIndexJob(next.Jobs[i]), nil
	}
	return indexJobRecord{}, authority.Error{Code: authority.NoAuthority}
}

func (q *indexQueue) records(ctx context.Context) ([]indexJobRecord, error) {
	if err := q.lock(ctx); err != nil {
		return nil, err
	}
	defer q.mu.Unlock()
	return cloneIndexSnapshot(q.state).Jobs, nil
}

func (q *indexQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
