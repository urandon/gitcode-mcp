package localauthority

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"sort"
	"time"

	"gitcode-mcp/internal/authority"
)

// SQL-only inspection occurs under the existing project effect turn. Receipt
// acknowledgement still requires the exact Git/barrier proof in recovery.
func (s *Store) inspectRecoveryOperation(ctx context.Context, project, operation authority.UUID, observed int) (documentOperation, error) {
	var zero documentOperation
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if !project.Valid() || !operation.Valid() || observed < 1 || observed > 3 {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	if !s.mu.TryLock() {
		return zero, authority.Error{Code: authority.Conflict}
	}
	defer s.mu.Unlock()
	if s.closed || s.lease == nil {
		return zero, Error{ReadOnly}
	}
	if project != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if s.schema < 2 {
		return zero, Error{KnowledgeMigrationRequired}
	}
	if s.logicalFenced {
		return zero, Error{Corrupt}
	}
	if s.fenced {
		return zero, Error{CommitUncertain}
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		s.fenced = true
		return zero, err
	}
	if err := verifyWriter(ctx, s.db); err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, err
	}
	if err := s.verifyIdentity(ctx, project); err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, s.documentSQLError(err)
	}
	o, err := scanDocumentOperation(ctx, s.db.QueryRowContext(ctx, documentOperationQuery+" WHERE operation_id=?", operation), project)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if err != nil {
		return zero, s.documentSQLError(err)
	}
	if observed > o.Generation {
		return zero, authority.Error{Code: authority.Conflict}
	}
	return o, nil
}

// Startup binds every retained selector and successful receipt to canonical
// SQL. A self-consistent sidecar digest alone cannot invent a source operation.
func (s *Store) validateRecoveryJobTargets(ctx context.Context, snapshot recoveryJobSnapshot) error {
	if !s.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	defer s.mu.Unlock()
	return s.validateRecoveryJobTargetsLocked(ctx, snapshot)
}

func (s *Store) validateRecoveryJobTargetsLocked(ctx context.Context, snapshot recoveryJobSnapshot) error {
	for _, r := range snapshot.Jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		o, err := scanDocumentOperation(ctx, s.db.QueryRowContext(ctx, documentOperationQuery+" WHERE operation_id=?", r.View.JobID), snapshot.ProjectID)
		if err != nil || o.Intent != r.IntentHash || r.View.ObservedJournalGeneration > o.Generation {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return Error{JobsCorrupt}
		}
		if r.View.Receipt != nil && (o.Phase != "settled" || o.Result == nil || !reflect.DeepEqual(*r.View.Receipt, o.Result.Receipt)) {
			return Error{JobsCorrupt}
		}
	}
	return nil
}

func jobActorHash(project authority.UUID, actor Actor) string {
	return hash([]byte(string(project) + "\x00" + actor.ID))
}

func (q *recoveryQueue) locked(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !q.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	if q.closed || q.fence != "" {
		code := q.fence
		if code == "" {
			code = JobsUncertain
		}
		q.mu.Unlock()
		return Error{code}
	}
	return nil
}

func (q *recoveryQueue) retained(ctx context.Context, id authority.UUID) (RecoveryJobView, bool, error) {
	if err := q.locked(ctx); err != nil {
		return RecoveryJobView{}, false, err
	}
	defer q.unlock()
	i, found := recoveryRecordIndex(q.state.Jobs, id)
	if !found {
		return RecoveryJobView{}, false, nil
	}
	return cloneRecoveryRecord(q.state.Jobs[i]).View, true, nil
}

// Called only with a freshly inspected canonical pending operation. A retained
// replay does not upgrade scope from a new caller's observation or actor.
func (q *recoveryQueue) admit(ctx context.Context, o documentOperation, actor Actor) (RecoveryJobView, error) {
	if !actor.valid() || o.Phase == "settled" {
		return RecoveryJobView{}, authority.Error{Code: authority.InvalidContract}
	}
	if err := q.locked(ctx); err != nil {
		return RecoveryJobView{}, err
	}
	defer q.unlock()
	if i, found := recoveryRecordIndex(q.state.Jobs, o.ID); found {
		if q.state.Jobs[i].IntentHash != o.Intent {
			q.fence = JobsCorrupt
			return RecoveryJobView{}, Error{JobsCorrupt}
		}
		return cloneRecoveryRecord(q.state.Jobs[i]).View, nil
	}
	for _, r := range q.state.Jobs {
		if r.View.State == "queued" || r.View.State == "running" || r.View.State == "cancelling" {
			return RecoveryJobView{}, authority.Error{Code: authority.Conflict}
		}
	}
	next, err := q.nextState()
	if err != nil {
		return RecoveryJobView{}, err
	}
	if len(next.Jobs) >= MaxRecoveryJobs {
		oldest := -1
		for i, r := range next.Jobs {
			if r.View.State == "succeeded" && r.View.Receipt != nil && (oldest < 0 || r.AdmittedGeneration < next.Jobs[oldest].AdmittedGeneration) {
				oldest = i
			}
		}
		if oldest < 0 {
			return RecoveryJobView{}, Error{Capacity}
		}
		next.Jobs = append(next.Jobs[:oldest], next.Jobs[oldest+1:]...)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	r := recoveryJobRecord{View: RecoveryJobView{Schema: 1, ProjectID: q.state.ProjectID, JobID: o.ID, Type: "document_recovery", State: "queued", Generation: 1, ObservedJournalGeneration: o.Generation, CreatedAt: stamp, UpdatedAt: stamp}, IntentHash: o.Intent, ActorHash: jobActorHash(q.state.ProjectID, actor), AdmittedGeneration: next.Generation}
	next.Jobs = append(next.Jobs, r)
	sort.Slice(next.Jobs, func(i, j int) bool { return next.Jobs[i].View.JobID < next.Jobs[j].View.JobID })
	if err = q.publish(ctx, next); err != nil {
		return RecoveryJobView{}, err
	}
	return r.View, nil
}

// Only metadata control takes this short lock. It never waits on a worker or
// holds the source effect turn, so a running operation can actually be cancelled.
func (q *recoveryQueue) action(ctx context.Context, req RecoveryJobAction, actor Actor, retry bool, attempted *bool) (RecoveryJobView, error) {
	if err := req.Validate(); err != nil {
		return RecoveryJobView{}, err
	}
	if !actor.valid() {
		return RecoveryJobView{}, authority.Error{Code: authority.InvalidContract}
	}
	if err := q.locked(ctx); err != nil {
		return RecoveryJobView{}, err
	}
	defer q.unlock()
	if req.ProjectID != q.state.ProjectID {
		return RecoveryJobView{}, authority.Error{Code: authority.NoAuthority}
	}
	i, found := recoveryRecordIndex(q.state.Jobs, req.JobID)
	if !found {
		return RecoveryJobView{}, Error{JobNotFound}
	}
	old := q.state.Jobs[i]
	if retry && old.RetryFrom == req.ExpectedGeneration || !retry && old.CancelFrom == req.ExpectedGeneration {
		return cloneRecoveryRecord(old).View, nil
	}
	if req.ExpectedGeneration != old.View.Generation {
		return RecoveryJobView{}, authority.Error{Code: authority.Conflict}
	}
	if old.View.State == "succeeded" || !retry && (old.View.State == "cancelled" || old.View.State == "cancelling") {
		return cloneRecoveryRecord(old).View, nil
	}
	if retry {
		if old.View.State != "failed" && old.View.State != "cancelled" && old.View.State != "interrupted" {
			return RecoveryJobView{}, authority.Error{Code: authority.Conflict}
		}
		if old.View.Attempts >= MaxRecoveryJobAttempts {
			return RecoveryJobView{}, Error{JobRetryLimit}
		}
		for j, r := range q.state.Jobs {
			if j != i && (r.View.State == "queued" || r.View.State == "running" || r.View.State == "cancelling") {
				return RecoveryJobView{}, authority.Error{Code: authority.Conflict}
			}
		}
	} else if old.View.State != "queued" && old.View.State != "running" {
		return cloneRecoveryRecord(old).View, nil
	}
	if old.View.Generation >= math.MaxInt64 {
		return RecoveryJobView{}, Error{Capacity}
	}
	next, err := q.nextState()
	if err != nil {
		return RecoveryJobView{}, err
	}
	r := &next.Jobs[i]
	r.View.Generation++
	r.View.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.ActionActorHash = jobActorHash(req.ProjectID, actor)
	if retry {
		r.RetryFrom = old.View.Generation
		r.CancelFrom = 0
		r.View.State = "queued"
		r.View.Code = ""
	} else {
		r.CancelFrom = old.View.Generation
		if old.View.State == "queued" {
			r.View.State = "cancelled"
			r.View.Code = "cancelled"
		} else {
			r.View.State = "cancelling"
		}
	}
	err = q.publish(ctx, next)
	if attempted != nil {
		// An unpublished stage is not cancellation authority. Only a final
		// state acknowledgement or an ambiguous rename may stop the active
		// attempt; definitive pre-publication failure preserves its permission.
		*attempted = err == nil || q.fence == JobsUncertain
	}
	if err != nil {
		return RecoveryJobView{}, err
	}
	return cloneRecoveryRecord(*r).View, nil
}

func (q *recoveryQueue) records(ctx context.Context) ([]recoveryJobRecord, error) {
	if err := q.locked(ctx); err != nil {
		return nil, err
	}
	defer q.unlock()
	out := make([]recoveryJobRecord, len(q.state.Jobs))
	for i, r := range q.state.Jobs {
		out[i] = cloneRecoveryRecord(r)
	}
	return out, nil
}

// Must be called after obtaining the source effect turn, before source effect.
func (q *recoveryQueue) startAttempt(ctx context.Context, id authority.UUID) (recoveryJobRecord, error) {
	if err := q.locked(ctx); err != nil {
		return recoveryJobRecord{}, err
	}
	defer q.unlock()
	i, found := recoveryRecordIndex(q.state.Jobs, id)
	if !found {
		return recoveryJobRecord{}, Error{JobNotFound}
	}
	old := q.state.Jobs[i]
	if old.View.State != "queued" && old.View.State != "running" {
		return recoveryJobRecord{}, authority.Error{Code: authority.Conflict}
	}
	if old.View.Attempts >= MaxRecoveryJobAttempts {
		return recoveryJobRecord{}, Error{JobRetryLimit}
	}
	if old.View.Generation >= math.MaxInt64 {
		return recoveryJobRecord{}, Error{Capacity}
	}
	next, err := q.nextState()
	if err != nil {
		return recoveryJobRecord{}, err
	}
	r := &next.Jobs[i]
	r.View.State = "running"
	r.View.Generation++
	r.View.Attempts++
	r.View.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = q.publish(ctx, next); err != nil {
		return recoveryJobRecord{}, err
	}
	return cloneRecoveryRecord(*r), nil
}

// Terminal source proof is supplied only by the worker under its retained
// effect turn. Pending cancellation is not an assertion that Git was rolled back.
func (q *recoveryQueue) finish(ctx context.Context, id authority.UUID, expected uint64, state, code string, receipt *authority.MutationReceipt, reconciled bool) (RecoveryJobView, error) {
	if err := q.locked(ctx); err != nil {
		return RecoveryJobView{}, err
	}
	defer q.unlock()
	i, found := recoveryRecordIndex(q.state.Jobs, id)
	if !found {
		return RecoveryJobView{}, Error{JobNotFound}
	}
	old := q.state.Jobs[i]
	if old.View.State == "succeeded" {
		return cloneRecoveryRecord(old).View, nil
	}
	if old.View.Generation != expected && !(old.View.State == "cancelling" && old.CancelFrom == expected) {
		return RecoveryJobView{}, recoveryGenerationChanged{}
	}
	if receipt == nil && (old.View.State == "cancelled" || old.View.State == "failed" || old.View.State == "interrupted") {
		return cloneRecoveryRecord(old).View, nil
	}
	if state == "failed" && old.View.State == "cancelling" {
		state, code = "cancelled", "cancelled"
	}
	if old.View.Generation >= math.MaxInt64 {
		return RecoveryJobView{}, Error{Capacity}
	}
	next, err := q.nextState()
	if err != nil {
		return RecoveryJobView{}, err
	}
	r := &next.Jobs[i]
	r.View.Generation++
	r.View.State = state
	r.View.Code = code
	r.View.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.View.Reconciled = reconciled
	if receipt != nil {
		copy := *receipt
		copy.Replayed = false
		r.View.Receipt = &copy
	}
	if err = q.publish(ctx, next); err != nil {
		// The source call has already joined/classified. Even a definitive
		// pre-rename terminal metadata failure must not expose "running" as
		// an acknowledged live attempt after its worker exits. Preserve the
		// admitted selector for owner-reopen proof without fencing the tracker.
		if q.fence == "" {
			q.fence = JobsUncertain
		}
		return RecoveryJobView{}, err
	}
	return cloneRecoveryRecord(*r).View, nil
}
