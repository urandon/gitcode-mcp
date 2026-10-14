package localauthority

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"gitcode-mcp/internal/authority"
)

// Separate non-disposable intent: never reset together with a catalog.
type projectionRepairStore struct {
	mu                                                      releaseMutex
	store                                                   *Store
	root, directory                                         *os.File
	rootIdentity, directoryIdentity, fileIdentity, fileHash string
	state                                                   projectionRepairSnapshot
	closed, restartRequired                                 bool
	fence                                                   Code
	fault                                                   func(string) error
	barrierFault                                            func(string) error
}

func openProjectionRepair(ctx context.Context, s *Store) (*projectionRepairStore, error) {
	if s == nil || s.lease == nil || s.lease.runtimeGuard == nil {
		return nil, Error{ReadOnly}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.verifyPhysicalBinding() != nil {
		return nil, Error{RepairCorrupt}
	}
	root, err := openPrivateDirectory(s.directory)
	if err != nil {
		return nil, err
	}
	q := &projectionRepairStore{store: s, root: root}
	ok := false
	defer func() {
		if !ok {
			_ = q.close()
		}
	}()
	q.rootIdentity, err = physicalIdentity(root)
	if err != nil || q.rootIdentity != s.lease.runtimeDirectoryIdentity {
		return nil, Error{RepairCorrupt}
	}
	q.directory, _, err = openRepairSubdirectory(root, repairDirectory, false)
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
		body, exists, id, err := q.readFile("state.json")
		if err != nil {
			return nil, err
		}
		// Absence cannot distinguish interrupted first provisioning from loss of
		// an acknowledged intent. Preserve ambiguity; never adopt it as empty.
		if !exists {
			return nil, Error{RepairUncertain}
		}
		if exists {
			var e projectionRepairEnvelope
			if authority.DecodeStoredJSON(body, &e, maxRepairSnapshotBytes) != nil || e.State.ProjectID != s.manifest.ID || e.State.TrackerInstance != s.instance || e.State.ManifestHash != manifestHash(s.manifest) {
				return nil, Error{RepairCorrupt}
			}
			status, err := s.Status(ctx)
			if err != nil {
				return nil, err
			}
			for _, r := range e.State.Records {
				if r.RootIdentity != q.rootIdentity || r.Plan.Sequence > status.AuthorityGeneration {
					return nil, Error{RepairCorrupt}
				}
			}
			q.state, q.fileHash, q.fileIdentity = e.State, hash(body), id
		}
		if _, _, err = q.inventory(ctx); err != nil {
			return nil, err
		}
		if exists {
			f, exists, err := openRepairMetadata(q.directory, "state.json", false)
			if err != nil || !exists {
				return nil, Error{RepairCorrupt}
			}
			err = q.syncBarrier(ctx, "repair_reopen_file", func() error { return syncFile(f) })
			_ = f.Close()
			if err != nil {
				return nil, err
			}
		}
		if err = q.syncBarrier(ctx, "repair_reopen_directory", q.directory.Sync); err != nil {
			return nil, err
		}
		if err = q.syncBarrier(ctx, "repair_reopen_root", q.root.Sync); err != nil {
			return nil, err
		}
	}
	if err = q.verifyExpected(); err != nil {
		return nil, err
	}
	ok = true
	return q, nil
}

func (q *projectionRepairStore) readFile(name string) ([]byte, bool, string, error) {
	f, exists, err := openRepairMetadata(q.directory, name, false)
	if err != nil || !exists {
		return nil, exists, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 1 || info.Size() > maxRepairSnapshotBytes {
		return nil, true, "", Error{RepairCorrupt}
	}
	body, err := io.ReadAll(io.LimitReader(f, maxRepairSnapshotBytes+1))
	if err != nil || int64(len(body)) != info.Size() || len(body) > maxRepairSnapshotBytes {
		return nil, true, "", Error{RepairCorrupt}
	}
	id, err := physicalIdentity(f)
	return body, true, id, err
}

func (q *projectionRepairStore) verifyExpected() error {
	if q.closed || q.root == nil {
		return Error{RepairUncertain}
	}
	if q.store.verifyPhysicalBinding() != nil {
		return Error{RepairCorrupt}
	}
	id, err := physicalIdentity(q.root)
	if err != nil || id != q.rootIdentity || id != q.store.lease.runtimeDirectoryIdentity {
		return Error{RepairCorrupt}
	}
	current, _, err := openRepairSubdirectory(q.root, repairDirectory, false)
	if err != nil {
		return err
	}
	if current != nil {
		defer current.Close()
	}
	if q.directory == nil {
		if current != nil || q.fileHash != "" || q.state.Generation != 0 {
			return Error{RepairCorrupt}
		}
		return nil
	}
	if current == nil {
		return Error{RepairCorrupt}
	}
	id, err = physicalIdentity(current)
	held, e := physicalIdentity(q.directory)
	if err != nil || e != nil || id != held || id != q.directoryIdentity {
		return Error{RepairCorrupt}
	}
	body, exists, identity, err := q.readFile("state.json")
	if err != nil || exists != (q.fileHash != "") || exists && (hash(body) != q.fileHash || identity != q.fileIdentity) {
		return Error{RepairCorrupt}
	}
	return nil
}

func (q *projectionRepairStore) lock(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !q.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	if q.fence != "" || q.closed {
		q.mu.Unlock()
		return Error{RepairUncertain}
	}
	if err := q.verifyExpected(); err != nil {
		q.fence = RepairCorrupt
		q.mu.Unlock()
		return err
	}
	return nil
}

func (q *projectionRepairStore) pending() bool {
	for _, r := range q.state.Records {
		if r.State != "confirmed" {
			return true
		}
	}
	return false
}

func (q *projectionRepairStore) allowIndex(ctx context.Context) error {
	if err := q.lock(ctx); err != nil {
		return err
	}
	defer q.mu.Unlock()
	if q.pending() || q.restartRequired {
		return Error{RepairUncertain}
	}
	_, _, err := q.inventory(ctx)
	return err
}

func (q *projectionRepairStore) inventory(ctx context.Context) (int, int64, error) {
	if q.directory == nil {
		return 0, 0, nil
	}
	scan, err := scanProjectionNamespace(q.directory)
	if err != nil {
		return 0, 0, Error{RepairCorrupt}
	}
	defer scan.Close()
	entries, err := scan.ReadDir(maxProjectionRepairs + maxRepairStages + 2)
	if err != nil && err != io.EOF || len(entries) > maxProjectionRepairs+maxRepairStages+1 {
		return 0, 0, Error{Capacity}
	}
	stages, bytes := 0, int64(0)
	found := map[authority.UUID]bool{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		name := entry.Name()
		if id := repairQuarantineID(name); id.Valid() {
			var record *projectionRepairRecord
			for i := range q.state.Records {
				if q.state.Records[i].ID == id {
					record = &q.state.Records[i]
					break
				}
			}
			if record == nil {
				return 0, 0, Error{RepairCorrupt}
			}
			if err := q.verifyQuarantine(ctx, *record, false); err != nil {
				return 0, 0, err
			}
			found[id] = true
			continue
		}
		f, exists, err := openRepairMetadata(q.directory, name, false)
		if err != nil || !exists {
			return 0, 0, Error{RepairCorrupt}
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil || info.Size() < 0 || info.Size() > maxRepairSnapshotBytes {
			return 0, 0, Error{RepairCorrupt}
		}
		if name != "state.json" {
			stages++
			bytes += info.Size()
		}
	}
	for _, r := range q.state.Records {
		if r.State != "prepared" && !found[r.ID] {
			return 0, 0, Error{RepairCorrupt}
		}
	}
	if stages > maxRepairStages || bytes > maxRepairStageBytes {
		return 0, 0, Error{Capacity}
	}
	return stages, bytes, nil
}

func (q *projectionRepairStore) verifyQuarantine(ctx context.Context, r projectionRepairRecord, durable bool) error {
	dir, _, err := openRepairSubdirectory(q.directory, repairQuarantineName(r.ID), false)
	if err != nil || dir == nil {
		return Error{RepairCorrupt}
	}
	defer dir.Close()
	image, err := inspectRepairDirectory(ctx, q.store, q.rootIdentity, dir)
	if err != nil {
		return err
	}
	if image.DirectoryIdentity != r.ProjectionIdentity || image.Fingerprint != r.ProjectionFingerprint || image.Files != r.Plan.Files || image.Bytes != r.Plan.Bytes {
		return Error{RepairCorrupt}
	}
	if !durable {
		return nil
	}
	names, err := projectionRepairNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		f, exists, err := openProjectionFile(dir, name, false)
		if err != nil || !exists {
			return Error{RepairCorrupt}
		}
		err = q.syncBarrier(ctx, "repair_quarantine_file", func() error { return syncFile(f) })
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	if err = q.syncBarrier(ctx, "repair_quarantine_directory", dir.Sync); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "repair_quarantine_parent", q.directory.Sync); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "repair_quarantine_root", q.root.Sync); err != nil {
		return err
	}
	after, err := inspectRepairDirectory(ctx, q.store, q.rootIdentity, dir)
	if err != nil {
		return err
	}
	if after != image {
		return Error{RepairCorrupt}
	}
	return q.verifyExpected()
}

func (q *projectionRepairStore) syncBarrier(ctx context.Context, phase string, run func() error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.barrierFault != nil && q.barrierFault(phase) != nil {
		return Error{DurabilityUnavailable}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if run() != nil {
		return Error{DurabilityUnavailable}
	}
	return ctx.Err()
}
func (q *projectionRepairStore) inject(ctx context.Context, phase string) error {
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

func (q *projectionRepairStore) ensureNamespace(ctx context.Context) error {
	if q.directory == nil {
		dir, created, err := openRepairSubdirectory(q.root, repairDirectory, true)
		if err != nil {
			return err
		}
		if !created {
			_ = dir.Close()
			return Error{RepairCorrupt}
		}
		if err = lockExclusive(dir); err != nil {
			_ = dir.Close()
			return err
		}
		id, err := physicalIdentity(dir)
		if err != nil {
			_ = dir.Close()
			return err
		}
		q.directory, q.directoryIdentity = dir, id
	}
	return q.syncBarrier(ctx, "repair_namespace_root", q.root.Sync)
}

// Caller holds all admission locks. Memory advances only after final durable
// exact readback. Prepublication stages are bounded evidence, never execution.
func (q *projectionRepairStore) publish(ctx context.Context, next projectionRepairSnapshot) (err error) {
	defer func() {
		if err != nil && q.state.Generation == 0 && q.directory != nil {
			q.fence = RepairUncertain
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.fence != "" {
		return Error{RepairUncertain}
	}
	if !repairSnapshotTransition(q.state, next) || next.ProjectID != q.store.manifest.ID || next.TrackerInstance != q.store.instance || next.ManifestHash != manifestHash(q.store.manifest) {
		return Error{RepairCorrupt}
	}
	if err = q.verifyExpected(); err != nil {
		q.fence = RepairCorrupt
		return err
	}
	state, _ := json.Marshal(next)
	body, err := json.Marshal(projectionRepairEnvelope{next, hash(state)})
	if err != nil || len(body) > maxRepairSnapshotBytes {
		return Error{Capacity}
	}
	if err = q.ensureNamespace(ctx); err != nil {
		return err
	}
	count, total, err := q.inventory(ctx)
	if err != nil {
		return err
	}
	if count >= maxRepairStages || total+int64(len(body)) > maxRepairStageBytes {
		return Error{Capacity}
	}
	id, err := authority.NewUUID()
	if err != nil {
		return err
	}
	name := "stage-" + string(id) + ".json"
	f, _, err := openRepairMetadata(q.directory, name, true)
	if err != nil {
		return err
	}
	defer f.Close()
	if n, e := f.Write(body); e != nil || n != len(body) {
		return Error{DurabilityUnavailable}
	}
	if err = q.inject(ctx, "repair_stage_write"); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "repair_stage_file", func() error { return syncFile(f) }); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "repair_stage_directory", q.directory.Sync); err != nil {
		return err
	}
	if err = q.inject(ctx, "repair_stage_ready"); err != nil {
		return err
	}
	if err = q.verifyExpected(); err != nil {
		q.fence = RepairCorrupt
		return err
	}
	actual, exists, stageID, err := q.readFile(name)
	held, e := physicalIdentity(f)
	if err != nil || e != nil || !exists || stageID != held || hash(actual) != hash(body) {
		q.fence = RepairCorrupt
		return Error{RepairCorrupt}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = publishRepairMetadata(q.directory, name); err != nil {
		q.fence = RepairUncertain
		return err
	}
	defer func() {
		if err != nil {
			q.fence = RepairUncertain
		}
	}()
	if err = q.inject(ctx, "repair_state_rename"); err != nil {
		return err
	}
	if err = q.syncBarrier(ctx, "repair_state_directory", q.directory.Sync); err != nil {
		return err
	}
	oldState, oldHash, oldID := q.state, q.fileHash, q.fileIdentity
	next.Records = append([]projectionRepairRecord(nil), next.Records...)
	q.state, q.fileHash, q.fileIdentity = next, hash(body), stageID
	if err = q.verifyExpected(); err == nil {
		err = q.inject(ctx, "repair_state_acknowledgement")
	}
	if err != nil {
		q.state, q.fileHash, q.fileIdentity = oldState, oldHash, oldID
	}
	return err
}

func (q *projectionRepairStore) close() error {
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
