package localauthority

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync/atomic"

	"gitcode-mcp/internal/authority"
)

const maxIndexSnapshotBytes = 512 << 10
const maxIndexStages = 4
const maxIndexStageBytes = 2 << 20

// This ledger is not disposable catalog state: it retains caller and
// cancellation intent across derived-index failures. Missing observation does
// not create it, and existing uncertain metadata is never treated as first boot.
type indexQueue struct {
	mu                              releaseMutex
	store                           *Store
	root, directory                 *os.File
	rootIdentity, directoryIdentity string
	fileHash, fileIdentity          string
	state                           indexJobSnapshot
	closed                          bool
	fence                           Code
	terminalUncertain               atomic.Bool // joined effect cannot acquire metadata before its proof deadline
	wake                            chan struct{}
	fault                           func(string) error
	barrierFault                    func(string) error
}

func openIndexQueue(ctx context.Context, s *Store) (*indexQueue, error) {
	return openIndexQueueWithHooks(ctx, s, nil, nil)
}

func openIndexQueueWithHooks(ctx context.Context, s *Store, fault, barrierFault func(string) error) (*indexQueue, error) {
	if s == nil || s.lease == nil || s.lease.runtimeGuard == nil || s.lease.runtimeDirectoryIdentity == "" {
		return nil, authority.Error{Code: authority.InitializationRequired}
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
	q := &indexQueue{store: s, root: root, wake: make(chan struct{}, 1), fault: fault, barrierFault: barrierFault}
	ok := false
	defer func() {
		if !ok {
			_ = q.close()
		}
	}()
	q.rootIdentity, err = physicalIdentity(root)
	if err != nil || q.rootIdentity != s.lease.runtimeDirectoryIdentity {
		return nil, Error{IndexJobsCorrupt}
	}
	q.directory, _, err = openIndexJobNamespace(root, false)
	if err != nil {
		return nil, err
	}
	if q.directory != nil {
		if err = lockExclusive(q.directory); err != nil {
			return nil, err
		}
		q.directoryIdentity, err = physicalIdentity(q.directory)
		if err != nil {
			return nil, err
		}
		if _, _, err = q.inventory(ctx); err != nil {
			return nil, err
		}
		body, exists, identity, err := q.readFile("state.json")
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, Error{IndexJobsUncertain}
		}
		var envelope indexJobEnvelope
		if authority.DecodeJSON(body, &envelope) != nil {
			return nil, Error{IndexJobsCorrupt}
		}
		q.state, q.fileHash, q.fileIdentity = envelope.State, hash(body), identity
		if err = q.validateSource(ctx, q.state); err != nil {
			return nil, err
		}
		if err = q.verifyExpected(); err != nil {
			return nil, err
		}
		f, exists, err := openIndexJobFile(q.directory, "state.json", false)
		if err != nil || !exists {
			return nil, Error{IndexJobsCorrupt}
		}
		id, err := physicalIdentity(f)
		if err == nil && id == q.fileIdentity {
			err = q.syncBarrier(ctx, "index_reopen_file", func() error { return syncFile(f) })
		} else {
			err = Error{IndexJobsCorrupt}
		}
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		if err = q.syncBarrier(ctx, "index_reopen_directory", q.directory.Sync); err != nil {
			return nil, err
		}
		if err = q.syncBarrier(ctx, "index_reopen_root", q.root.Sync); err != nil {
			return nil, err
		}
	}
	if err = q.verifyExpected(); err != nil {
		return nil, err
	}
	ok = true
	return q, nil
}

func (q *indexQueue) validateSource(ctx context.Context, next indexJobSnapshot) error {
	if !q.matchesSource(next) {
		return Error{IndexJobsCorrupt}
	}
	status, err := q.store.Status(ctx)
	if err != nil {
		return err
	}
	for _, r := range next.Jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.Selection.Sequence > status.AuthorityGeneration {
			return Error{IndexJobsCorrupt}
		}
	}
	return nil
}

func (q *indexQueue) matchesSource(next indexJobSnapshot) bool {
	return next.valid() && next.ProjectID == q.store.manifest.ID && next.TrackerInstance == q.store.instance && next.ManifestHash == manifestHash(q.store.manifest)
}

func (q *indexQueue) verifyExpected() error {
	if q.closed || q.root == nil {
		return Error{IndexJobsUncertain}
	}
	if q.store.verifyPhysicalBinding() != nil {
		return Error{IndexJobsCorrupt}
	}
	id, err := physicalIdentity(q.root)
	if err != nil || id != q.rootIdentity || id != q.store.lease.runtimeDirectoryIdentity {
		return Error{IndexJobsCorrupt}
	}
	current, _, err := openIndexJobNamespace(q.root, false)
	if err != nil {
		return err
	}
	if current != nil {
		defer current.Close()
	}
	if q.directory == nil {
		if current != nil || q.fileHash != "" || q.state.Generation != 0 {
			return Error{IndexJobsCorrupt}
		}
		return nil
	}
	if current == nil {
		return Error{IndexJobsCorrupt}
	}
	id, err = physicalIdentity(current)
	held, e := physicalIdentity(q.directory)
	if err != nil || e != nil || id != held || id != q.directoryIdentity {
		return Error{IndexJobsCorrupt}
	}
	body, exists, identity, err := q.readFile("state.json")
	if err != nil || exists != (q.fileHash != "") || exists && (hash(body) != q.fileHash || identity != q.fileIdentity) {
		return Error{IndexJobsCorrupt}
	}
	return nil
}

func (q *indexQueue) readFile(name string) ([]byte, bool, string, error) {
	f, exists, err := openIndexJobFile(q.directory, name, false)
	if err != nil || !exists {
		return nil, exists, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 1 || info.Size() > maxIndexSnapshotBytes {
		return nil, true, "", Error{IndexJobsCorrupt}
	}
	body, err := io.ReadAll(io.LimitReader(f, maxIndexSnapshotBytes+1))
	if err != nil || len(body) > maxIndexSnapshotBytes {
		return nil, true, "", Error{IndexJobsCorrupt}
	}
	id, err := physicalIdentity(f)
	return body, true, id, err
}

func (q *indexQueue) inventory(ctx context.Context) (count int, total int64, err error) {
	scan, err := scanIndexJobNamespace(q.directory)
	if err != nil {
		return 0, 0, err
	}
	defer scan.Close()
	entries, err := scan.ReadDir(maxIndexStages + 2)
	if err != nil && err != io.EOF {
		return 0, 0, Error{IndexJobsCorrupt}
	}
	if len(entries) > maxIndexStages+1 {
		return 0, 0, Error{Capacity}
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return count, total, ctx.Err()
		}
		f, exists, err := openIndexJobFile(q.directory, entry.Name(), false)
		if err != nil || !exists {
			return count, total, Error{IndexJobsCorrupt}
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil || info.Size() < 0 || info.Size() > maxIndexSnapshotBytes {
			return count, total, Error{IndexJobsCorrupt}
		}
		if entry.Name() != "state.json" {
			count++
			total += info.Size()
		}
	}
	if count > maxIndexStages || total > maxIndexStageBytes {
		return count, total, Error{Capacity}
	}
	return count, total, nil
}

func (q *indexQueue) inject(ctx context.Context, phase string) error {
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

func (q *indexQueue) syncBarrier(ctx context.Context, phase string, sync func() error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.barrierFault != nil && q.barrierFault(phase) != nil {
		return Error{DurabilityUnavailable}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if sync() != nil {
		return Error{DurabilityUnavailable}
	}
	return ctx.Err()
}

func (q *indexQueue) ensureNamespace(ctx context.Context) error {
	if q.directory != nil {
		return q.syncBarrier(ctx, "index_namespace_root", q.root.Sync)
	}
	directory, created, err := openIndexJobNamespace(q.root, true)
	if err != nil {
		return err
	}
	if !created {
		_ = directory.Close()
		return Error{IndexJobsCorrupt}
	}
	if err = lockExclusive(directory); err != nil {
		_ = directory.Close()
		return err
	}
	id, err := physicalIdentity(directory)
	if err != nil {
		_ = directory.Close()
		return err
	}
	q.directory, q.directoryIdentity = directory, id
	return q.syncBarrier(ctx, "index_namespace_root", q.root.Sync)
}

// Caller holds the ledger mutex. Memory advances only after exact durable
// final readback. Uncertainty fences this ledger, never the source authority.
func (q *indexQueue) publish(ctx context.Context, next indexJobSnapshot) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.fence != "" {
		return Error{q.fence}
	}
	if q.terminalUncertain.Load() {
		return Error{IndexJobsUncertain}
	}
	if !indexSnapshotTransition(q.state, next) {
		return Error{IndexJobsCorrupt}
	}
	if !q.matchesSource(next) {
		return Error{IndexJobsCorrupt}
	}
	// Only new admission introduces a source selector. Conserved lifecycle
	// changes need no SQL lookup and cancellation stays independent of the
	// worker-held source mutex. Reopen validates every retained selector.
	if len(next.Jobs) > len(q.state.Jobs) {
		if err = q.validateSource(ctx, next); err != nil {
			return err
		}
	}
	if err = q.verifyExpected(); err != nil {
		q.fence = IndexJobsCorrupt
		return err
	}
	state, _ := json.Marshal(next)
	body, err := json.Marshal(indexJobEnvelope{State: next, Digest: hash(state)})
	if err != nil || len(body) > maxIndexSnapshotBytes {
		return Error{Capacity}
	}
	if err = q.ensureNamespace(ctx); err != nil {
		return err
	}
	count, total, err := q.inventory(ctx)
	if err != nil {
		return err
	}
	if count >= maxIndexStages || total+int64(len(body)) > maxIndexStageBytes {
		return Error{Capacity}
	}
	id, err := authority.NewUUID()
	if err != nil {
		return err
	}
	name := "stage-" + string(id) + ".json"
	f, _, err := openIndexJobFile(q.directory, name, true)
	if err != nil {
		return err
	}
	defer f.Close()
	if n, e := f.Write(body); e != nil || n != len(body) {
		return Error{DurabilityUnavailable}
	}
	if err = q.inject(ctx, "index_stage_write"); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "index_stage_file", func() error { return syncFile(f) }); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "index_stage_directory", q.directory.Sync); err != nil {
		return err
	}
	if err = q.inject(ctx, "index_stage_ready"); err != nil {
		return err
	}
	if err = q.verifyExpected(); err != nil {
		q.fence = IndexJobsCorrupt
		return err
	}
	actual, exists, stageID, err := q.readFile(name)
	held, e := physicalIdentity(f)
	if err != nil || e != nil || !exists || stageID != held || hash(actual) != hash(body) {
		q.fence = IndexJobsCorrupt
		return Error{IndexJobsCorrupt}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.terminalUncertain.Load() {
		return Error{IndexJobsUncertain}
	}
	boundary := capturePhasePublication(ctx, q.store.manifest.ID, "index_job", indexPhaseOutcome(q.state, next))
	if err = publishIndexJobFile(q.directory, name); err != nil {
		q.fence = IndexJobsUncertain
		return err
	}
	defer func() {
		if err != nil {
			q.fence = IndexJobsUncertain
		}
	}()
	if err = q.inject(ctx, "index_state_rename"); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "index_state_directory", q.directory.Sync); err != nil {
		return err
	}
	if q.terminalUncertain.Load() {
		return Error{IndexJobsUncertain}
	}
	oldState, oldHash, oldID := q.state, q.fileHash, q.fileIdentity
	q.state, q.fileHash, q.fileIdentity = cloneIndexSnapshot(next), hash(body), stageID
	if err = q.verifyExpected(); err == nil {
		boundary.confirm()
		err = q.inject(ctx, "index_state_acknowledgement")
	}
	if err == nil && q.terminalUncertain.Load() {
		err = Error{IndexJobsUncertain}
	}
	if err != nil {
		q.state, q.fileHash, q.fileIdentity = oldState, oldHash, oldID
	}
	return err
}

func (q *indexQueue) close() error {
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
	q.root, q.directory = nil, nil
	return err
}
