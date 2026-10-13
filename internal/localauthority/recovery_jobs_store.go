package localauthority

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"sort"

	"gitcode-mcp/internal/authority"
)

// A queue is constructed only from an already prepared coordinator writer.
// It owns no account/config/provider discovery, source body or source repair.
type recoveryQueue struct {
	mu                              releaseMutex
	store                           *Store
	root, directory                 *os.File
	rootIdentity, directoryIdentity string
	state                           recoveryJobSnapshot
	fileHash, fileIdentity          string
	fence                           Code
	closed                          bool
	wake                            chan struct{}
	fault                           func(string) error // private test injection; never serialized/configured
}

func newRecoveryQueue(ctx context.Context, s *Store, fault func(string) error) (*recoveryQueue, error) {
	if s == nil || s.lease == nil || s.lease.runtimeGuard == nil || s.lease.runtimeDirectoryIdentity == "" {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if s.schema < 2 {
		return nil, Error{KnowledgeMigrationRequired}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		return nil, err
	}
	root, err := openPrivateDirectory(s.directory)
	if err != nil {
		return nil, err
	}
	id, err := physicalIdentity(root)
	if err != nil || id != s.lease.runtimeDirectoryIdentity {
		_ = root.Close()
		return nil, Error{JobsCorrupt}
	}
	q := &recoveryQueue{store: s, root: root, rootIdentity: id, wake: make(chan struct{}, 1), fault: fault}
	ok := false
	defer func() {
		if !ok {
			_ = q.close()
		}
	}()
	var created bool
	q.directory, created, err = openRecoveryJobNamespace(root, true)
	if err != nil {
		return nil, err
	}
	if err = lockExclusive(q.directory); err != nil {
		return nil, err
	}
	q.directoryIdentity, err = physicalIdentity(q.directory)
	if err != nil {
		return nil, err
	}
	if created {
		if root.Sync() != nil {
			return nil, Error{DurabilityUnavailable}
		}
		if err = q.inject(ctx, "jobs_namespace_directory"); err != nil {
			return nil, err
		}
	}
	if err = q.verify(); err != nil {
		return nil, err
	}
	if _, _, err = q.stages(ctx); err != nil {
		return nil, err
	}
	body, exists, identity, err := q.readState()
	if err != nil {
		return nil, err
	}
	if !exists {
		// An existing namespace with no final state is not permission to erase
		// unknown history/cancellation. Preserve it for explicit recovery.
		if !created {
			return nil, Error{JobsUncertain}
		}
		empty := recoveryJobSnapshot{Schema: 1, ProjectID: s.manifest.ID, TrackerInstance: s.instance, ManifestHash: manifestHash(s.manifest), Generation: 1, Jobs: []recoveryJobRecord{}}
		if err = q.publish(ctx, empty); err != nil {
			return nil, err
		}
	} else {
		var envelope recoveryJobEnvelope
		if authority.DecodeJSON(body, &envelope) != nil || envelope.State.ProjectID != s.manifest.ID || envelope.State.TrackerInstance != s.instance || envelope.State.ManifestHash != manifestHash(s.manifest) {
			return nil, Error{JobsCorrupt}
		}
		q.state, q.fileHash, q.fileIdentity = envelope.State, hash(body), identity
		if err = s.validateRecoveryJobTargets(ctx, q.state); err != nil {
			return nil, err
		}
		// A validated owner reopen requalifies a possibly unacknowledged rename.
		f, exists, err := openRecoveryJobFile(q.directory, "state.json", false)
		if err != nil || !exists {
			return nil, Error{JobsCorrupt}
		}
		err = syncFile(f)
		_ = f.Close()
		if err != nil || q.directory.Sync() != nil {
			return nil, Error{JobsUncertain}
		}
		if err = q.verifyExpected(); err != nil {
			return nil, err
		}
	}
	ok = true
	return q, nil
}

func (q *recoveryQueue) inject(ctx context.Context, phase string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.fault != nil {
		if err := q.fault(phase); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (q *recoveryQueue) verify() error {
	if q.closed || q.root == nil || q.directory == nil {
		return Error{JobsUncertain}
	}
	if err := q.store.verifyPhysicalBinding(); err != nil {
		return Error{JobsCorrupt}
	}
	root, err := physicalIdentity(q.root)
	if err != nil || root != q.rootIdentity || root != q.store.lease.runtimeDirectoryIdentity {
		return Error{JobsCorrupt}
	}
	actual, _, err := openRecoveryJobNamespace(q.root, false)
	if err != nil {
		return Error{JobsCorrupt}
	}
	id, err := physicalIdentity(actual)
	_ = actual.Close()
	retained, e := physicalIdentity(q.directory)
	if err != nil || e != nil || id != q.directoryIdentity || retained != id {
		return Error{JobsCorrupt}
	}
	return nil
}

func (q *recoveryQueue) readState() ([]byte, bool, string, error) {
	f, exists, err := openRecoveryJobFile(q.directory, "state.json", false)
	if err != nil || !exists {
		return nil, exists, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 1 || info.Size() > MaxRecoveryJobSnapshotBytes {
		return nil, true, "", Error{JobsCorrupt}
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxRecoveryJobSnapshotBytes+1))
	if err != nil || len(body) > MaxRecoveryJobSnapshotBytes {
		return nil, true, "", Error{JobsCorrupt}
	}
	identity, err := physicalIdentity(f)
	if err != nil {
		return nil, true, "", Error{JobsCorrupt}
	}
	return body, true, identity, nil
}

func (q *recoveryQueue) verifyExpected() error {
	if err := q.verify(); err != nil {
		return err
	}
	body, exists, identity, err := q.readState()
	if err != nil || exists != (q.fileHash != "") || exists && (hash(body) != q.fileHash || identity != q.fileIdentity) {
		return Error{JobsCorrupt}
	}
	return nil
}

// Count/byte caps include unpublished evidence. It is never used as authority,
// selected by recency, silently cleaned or substituted for the final state.
func (q *recoveryQueue) stages(ctx context.Context) (int, int64, error) {
	scan, err := scanRecoveryJobDirectory(q.directory)
	if err != nil {
		return 0, 0, err
	}
	defer scan.Close()
	entries, err := scan.ReadDir(MaxRecoveryJobStages + 2)
	if err != nil && err != io.EOF {
		return 0, 0, Error{JobsCorrupt}
	}
	if len(entries) > MaxRecoveryJobStages+1 {
		return 0, 0, Error{Capacity}
	}
	count, total := 0, int64(0)
	for _, entry := range entries {
		if ctx.Err() != nil {
			return count, total, ctx.Err()
		}
		if !recoveryJobFileName(entry.Name()) {
			return count, total, Error{JobsCorrupt}
		}
		f, exists, err := openRecoveryJobFile(q.directory, entry.Name(), false)
		if err != nil || !exists {
			return count, total, Error{JobsCorrupt}
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil || info.Size() < 0 || info.Size() > MaxRecoveryJobSnapshotBytes {
			return count, total, Error{JobsCorrupt}
		}
		if entry.Name() != "state.json" {
			count++
			total += info.Size()
		}
	}
	if count > MaxRecoveryJobStages || total > MaxRecoveryJobStageBytes {
		return count, total, Error{Capacity}
	}
	return count, total, nil
}

// Caller holds queue ownership. No in-memory admission becomes visible before
// final publication barriers. Any ambiguity fences ONLY this queue, not source.
func (q *recoveryQueue) publish(ctx context.Context, next recoveryJobSnapshot) (err error) {
	if q.fence != "" {
		return Error{q.fence}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !next.valid() || !recoverySnapshotTransition(q.state, next) || next.ProjectID != q.store.manifest.ID || next.TrackerInstance != q.store.instance || next.ManifestHash != manifestHash(q.store.manifest) || q.state.Generation != 0 && next.Generation != q.state.Generation+1 || q.state.Generation == 0 && next.Generation != 1 {
		return Error{JobsCorrupt}
	}
	if err = q.verifyExpected(); err != nil {
		q.fence = JobsCorrupt
		return err
	}
	stateBody, _ := json.Marshal(next)
	body, err := json.Marshal(recoveryJobEnvelope{next, hash(stateBody)})
	if err != nil || len(body) > MaxRecoveryJobSnapshotBytes {
		return Error{Capacity}
	}
	count, total, err := q.stages(ctx)
	if err != nil {
		return err
	}
	if count >= MaxRecoveryJobStages || total+int64(len(body)) > MaxRecoveryJobStageBytes {
		return Error{Capacity}
	}
	id, err := authority.NewUUID()
	if err != nil {
		return err
	}
	stage := "stage-" + string(id) + ".json"
	f, _, err := openRecoveryJobFile(q.directory, stage, true)
	if err != nil {
		return err
	}
	defer f.Close()
	if n, e := f.Write(body); e != nil || n != len(body) {
		return Error{DurabilityUnavailable}
	}
	if err = q.inject(ctx, "jobs_stage_write"); err != nil {
		return err
	}
	if err = syncFile(f); err != nil {
		return err
	}
	if err = q.inject(ctx, "jobs_stage_file"); err != nil {
		return err
	}
	if q.directory.Sync() != nil {
		return Error{DurabilityUnavailable}
	}
	if err = q.inject(ctx, "jobs_stage_directory"); err != nil {
		return err
	}
	if err = q.verifyExpected(); err != nil {
		q.fence = JobsCorrupt
		return err
	}
	stageIdentity, err := q.verifyStage(stage, f, body)
	if err != nil {
		q.fence = JobsCorrupt
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = publishRecoveryJobFile(q.directory, stage); err != nil {
		q.fence = JobsUncertain
		return err
	}
	published := true
	defer func() {
		if published && err != nil {
			q.fence = JobsUncertain
		}
	}()
	if err = q.inject(ctx, "jobs_state_rename"); err != nil {
		return err
	}
	if q.directory.Sync() != nil {
		return Error{DurabilityUnavailable}
	}
	if err = q.inject(ctx, "jobs_state_directory"); err != nil {
		return err
	}
	if err = q.verify(); err != nil {
		return err
	}
	actual, exists, identity, err := q.readState()
	if err != nil || !exists || hash(actual) != hash(body) || identity != stageIdentity {
		return Error{JobsCorrupt}
	}
	if err = q.inject(ctx, "jobs_state_acknowledgement"); err != nil {
		return err
	}
	q.state, q.fileHash, q.fileIdentity = next, hash(body), identity
	published = false
	return nil
}

func (q *recoveryQueue) verifyStage(name string, retained *os.File, expected []byte) (string, error) {
	f, exists, err := openRecoveryJobFile(q.directory, name, false)
	if err != nil || !exists {
		return "", Error{JobsCorrupt}
	}
	defer f.Close()
	id, err := physicalIdentity(f)
	held, e := physicalIdentity(retained)
	if err != nil || e != nil || id != held {
		return "", Error{JobsCorrupt}
	}
	info, err := f.Stat()
	if err != nil || info.Size() != int64(len(expected)) {
		return "", Error{JobsCorrupt}
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxRecoveryJobSnapshotBytes+1))
	if err != nil || hash(body) != hash(expected) {
		return "", Error{JobsCorrupt}
	}
	return id, nil
}

func (q *recoveryQueue) nextState() (recoveryJobSnapshot, error) {
	if q.fence != "" {
		return recoveryJobSnapshot{}, Error{q.fence}
	}
	if q.closed || q.state.Generation >= math.MaxInt64 {
		return recoveryJobSnapshot{}, Error{Capacity}
	}
	next := q.state
	next.Generation++
	next.Jobs = make([]recoveryJobRecord, len(q.state.Jobs))
	for i, r := range q.state.Jobs {
		next.Jobs[i] = cloneRecoveryRecord(r)
	}
	return next, nil
}

func recoveryRecordIndex(records []recoveryJobRecord, id authority.UUID) (int, bool) {
	i := sort.Search(len(records), func(i int) bool { return records[i].View.JobID >= id })
	return i, i < len(records) && records[i].View.JobID == id
}

func (q *recoveryQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *recoveryQueue) unlock() {
	q.mu.Unlock()
}

func (q *recoveryQueue) close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	var err error
	if q.directory != nil && q.directory.Close() != nil {
		err = Error{DurabilityUnavailable}
	}
	if q.root != nil && q.root.Close() != nil {
		err = Error{DurabilityUnavailable}
	}
	q.directory, q.root = nil, nil
	return err
}
