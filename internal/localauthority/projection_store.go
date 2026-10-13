package localauthority

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"

	"gitcode-mcp/internal/authority"
)

// Private writer admission consumed by the coordinator's index-job cohort.
// Existing metadata is durability-qualified; missing state is not provisioned.
type projectionIndex struct {
	mu                              releaseMutex
	store                           *Store
	root, directory                 *os.File
	rootIdentity, directoryIdentity string
	fileHash, fileIdentity          string
	catalog                         *projectionCatalog
	fence                           Code
	closed                          bool
	fault                           func(string) error // private deterministic test seam
	barrierFault                    func(string) error // private pre-sync failure seam
}

func openProjection(ctx context.Context, s *Store) (*projectionIndex, error) {
	return openProjectionWithBarrierFault(ctx, s, nil)
}

func openProjectionWithBarrierFault(ctx context.Context, s *Store, fault func(string) error) (*projectionIndex, error) {
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
	p := &projectionIndex{store: s, root: root, barrierFault: fault}
	ok := false
	defer func() {
		if !ok {
			_ = p.close()
		}
	}()
	p.rootIdentity, err = physicalIdentity(root)
	if err != nil || p.rootIdentity != s.lease.runtimeDirectoryIdentity {
		return nil, Error{ProjectionCorrupt}
	}
	p.directory, _, err = openProjectionNamespace(root, false)
	if err != nil {
		return nil, err
	}
	if p.directory != nil {
		if err = lockExclusive(p.directory); err != nil {
			return nil, err
		}
		p.directoryIdentity, err = physicalIdentity(p.directory)
		if err != nil {
			return nil, err
		}
		if _, _, err = p.inventory(ctx); err != nil {
			return nil, err
		}
		body, exists, identity, err := p.readFile("catalog.json")
		if err != nil {
			return nil, err
		}
		if exists {
			var e projectionEnvelope
			if authority.DecodeStoredJSON(body, &e, MaxProjectionBytes) != nil || e.Catalog.ProjectID != s.manifest.ID || e.Catalog.TrackerInstance != s.instance || e.Catalog.ManifestHash != manifestHash(s.manifest) {
				return nil, Error{ProjectionCorrupt}
			}
			status, err := s.Status(ctx)
			if err != nil {
				return nil, err
			}
			if e.Catalog.Sequence > status.AuthorityGeneration {
				return nil, Error{ProjectionCorrupt}
			}
			p.catalog, p.fileHash, p.fileIdentity = &e.Catalog, hash(body), identity
		}
	}
	if err = p.verifyExpected(); err != nil {
		return nil, err
	}
	if p.directory != nil {
		if p.catalog != nil {
			f, exists, err := openProjectionFile(p.directory, "catalog.json", false)
			if err != nil || !exists {
				return nil, Error{ProjectionCorrupt}
			}
			id, err := physicalIdentity(f)
			if err == nil && id == p.fileIdentity {
				err = p.syncBarrier(ctx, "projection_reopen_file", func() error { return syncFile(f) })
			} else {
				err = Error{ProjectionCorrupt}
			}
			_ = f.Close()
			if err != nil {
				return nil, err
			}
		}
		if err = p.syncBarrier(ctx, "projection_reopen_directory", p.directory.Sync); err != nil {
			return nil, err
		}
		if err = p.syncBarrier(ctx, "projection_reopen_root", p.root.Sync); err != nil {
			return nil, err
		}
		if err = p.verifyExpected(); err != nil {
			return nil, err
		}
	}
	ok = true
	return p, nil
}

func (p *projectionIndex) verifyExpected() error {
	if p.closed || p.root == nil {
		return Error{ProjectionUncertain}
	}
	if p.store.verifyPhysicalBinding() != nil {
		return Error{ProjectionCorrupt}
	}
	id, err := physicalIdentity(p.root)
	if err != nil || id != p.rootIdentity || id != p.store.lease.runtimeDirectoryIdentity {
		return Error{ProjectionCorrupt}
	}
	current, _, err := openProjectionNamespace(p.root, false)
	if err != nil {
		return err
	}
	if current != nil {
		defer current.Close()
	}
	if p.directory == nil {
		if current != nil || p.fileHash != "" || p.catalog != nil {
			return Error{ProjectionCorrupt}
		}
		return nil
	}
	if current == nil {
		return Error{ProjectionCorrupt}
	}
	id, err = physicalIdentity(current)
	held, e := physicalIdentity(p.directory)
	if err != nil || e != nil || id != held || id != p.directoryIdentity {
		return Error{ProjectionCorrupt}
	}
	body, exists, identity, err := p.readFile("catalog.json")
	if err != nil || exists != (p.catalog != nil) || exists && (hash(body) != p.fileHash || identity != p.fileIdentity) {
		return Error{ProjectionCorrupt}
	}
	return nil
}

func (p *projectionIndex) readFile(name string) ([]byte, bool, string, error) {
	f, exists, err := openProjectionFile(p.directory, name, false)
	if err != nil || !exists {
		return nil, exists, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 1 || info.Size() > MaxProjectionBytes {
		return nil, true, "", Error{ProjectionCorrupt}
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxProjectionBytes+1))
	if err != nil || len(body) > MaxProjectionBytes {
		return nil, true, "", Error{ProjectionCorrupt}
	}
	id, err := physicalIdentity(f)
	return body, true, id, err
}

func (p *projectionIndex) inventory(ctx context.Context) (count int, total int64, err error) {
	scan, err := scanProjectionNamespace(p.directory)
	if err != nil {
		return 0, 0, err
	}
	defer scan.Close()
	entries, err := scan.ReadDir(MaxProjectionStages + 2)
	if err != nil && err != io.EOF {
		return 0, 0, Error{ProjectionCorrupt}
	}
	if len(entries) > MaxProjectionStages+1 {
		return 0, 0, Error{Capacity}
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return count, total, ctx.Err()
		}
		f, exists, err := openProjectionFile(p.directory, e.Name(), false)
		if err != nil || !exists {
			return count, total, Error{ProjectionCorrupt}
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil || info.Size() < 0 || info.Size() > MaxProjectionBytes {
			return count, total, Error{ProjectionCorrupt}
		}
		if e.Name() != "catalog.json" {
			count++
			total += info.Size()
		}
	}
	if count > MaxProjectionStages || total > MaxProjectionStageBytes {
		return count, total, Error{Capacity}
	}
	return count, total, nil
}

func (p *projectionIndex) newBuilder(ctx context.Context, sequence uint64) (*projectionBuilder, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if sequence > math.MaxInt64 {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	if !p.mu.TryLock() {
		return nil, authority.Error{Code: authority.Conflict}
	}
	defer p.mu.Unlock()
	if p.closed {
		return nil, Error{ProjectionUncertain}
	}
	if p.fence != "" {
		return nil, Error{p.fence}
	}
	if err := p.verifyExpected(); err != nil {
		p.fence = ProjectionCorrupt
		return nil, err
	}
	status, err := p.store.Status(ctx)
	if err != nil {
		return nil, err
	}
	if status.Readiness != "ready" {
		return nil, Error{ProjectionUncertain}
	}
	if sequence > status.AuthorityGeneration {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	catalog := projectionCatalog{Schema: 1, Namespace: projectionNamespace, ProjectID: p.store.manifest.ID, TrackerInstance: p.store.instance, ManifestHash: manifestHash(p.store.manifest), Sequence: sequence, Generation: 1, Entries: []projectionEntry{}}
	if p.catalog != nil {
		if p.catalog.Sequence > sequence {
			return nil, authority.Error{Code: authority.Conflict}
		}
		if p.catalog.Generation >= math.MaxInt64 {
			return nil, Error{Capacity}
		}
		catalog.Epoch, catalog.Generation = p.catalog.Epoch, p.catalog.Generation+1
	} else {
		catalog.Epoch, err = authority.NewUUID()
		if err != nil {
			return nil, err
		}
	}
	catalog.BuildID, err = authority.NewUUID()
	if err != nil {
		return nil, err
	}
	return &projectionBuilder{owner: p, catalog: catalog, baseHash: p.fileHash, maxEntries: MaxProjectionEntries}, nil
}

func (p *projectionIndex) view(ctx context.Context) (*projectionCatalog, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !p.mu.TryLock() {
		return nil, authority.Error{Code: authority.Conflict}
	}
	defer p.mu.Unlock()
	if p.closed {
		return nil, Error{ProjectionUncertain}
	}
	if p.fence != "" {
		return nil, Error{p.fence}
	}
	if err := p.verifyExpected(); err != nil {
		p.fence = ProjectionCorrupt
		return nil, err
	}
	if p.catalog == nil {
		return nil, nil
	}
	c := cloneProjection(*p.catalog)
	return &c, nil
}

func (p *projectionIndex) inject(ctx context.Context, phase string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.fault != nil {
		if err := p.fault(phase); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (p *projectionIndex) ensureNamespace(ctx context.Context) error {
	if p.directory != nil {
		// A previous mkdir may have succeeded while its parent barrier failed.
		// Retry that barrier even when the retained descriptor already exists.
		return p.syncBarrier(ctx, "projection_namespace_root", p.root.Sync)
	}
	directory, created, err := openProjectionNamespace(p.root, true)
	if err != nil {
		return err
	}
	if !created {
		_ = directory.Close()
		return Error{ProjectionCorrupt}
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
	p.directory, p.directoryIdentity = directory, id
	if err = p.syncBarrier(ctx, "projection_namespace_root", p.root.Sync); err != nil {
		return err
	}
	return p.inject(ctx, "projection_namespace")
}

func (p *projectionIndex) syncBarrier(ctx context.Context, phase string, sync func() error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.barrierFault != nil && p.barrierFault(phase) != nil {
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

// Only this owner's complete builder is a publish input, not peer/caller JSON.
// Failed derived publication never fences, changes or reconstructs authority.
func (p *projectionIndex) publish(ctx context.Context, b *projectionBuilder) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if b == nil || !b.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	defer b.mu.Unlock()
	if b.owner != p || !b.done || b.failed || !b.catalog.valid() {
		return Error{ProjectionCorrupt}
	}
	if !p.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	defer p.mu.Unlock()
	if p.closed {
		return Error{ProjectionUncertain}
	}
	if p.fence != "" {
		return Error{p.fence}
	}
	if b.baseHash != p.fileHash {
		return authority.Error{Code: authority.Conflict}
	}
	if err = p.verifyExpected(); err != nil {
		p.fence = ProjectionCorrupt
		return err
	}
	status, err := p.store.Status(ctx)
	if err != nil {
		return err
	}
	if status.Readiness != "ready" || b.catalog.Sequence > status.AuthorityGeneration {
		return Error{ProjectionUncertain}
	}
	c := cloneProjection(b.catalog)
	if p.catalog == nil && c.Generation != 1 || p.catalog != nil && (c.Epoch != p.catalog.Epoch || c.Generation != p.catalog.Generation+1 || c.Sequence < p.catalog.Sequence) {
		return Error{ProjectionCorrupt}
	}
	stateBody, _ := json.Marshal(c)
	body, err := json.Marshal(projectionEnvelope{Catalog: c, Digest: hash(stateBody)})
	if err != nil || len(body) > MaxProjectionBytes {
		return Error{Capacity}
	}
	if err = p.ensureNamespace(ctx); err != nil {
		return err
	}
	count, total, err := p.inventory(ctx)
	if err != nil {
		return err
	}
	if count >= MaxProjectionStages || total+int64(len(body)) > MaxProjectionStageBytes {
		return Error{Capacity}
	}
	id, err := authority.NewUUID()
	if err != nil {
		return err
	}
	stage := "stage-" + string(id) + ".json"
	f, _, err := openProjectionFile(p.directory, stage, true)
	if err != nil {
		return err
	}
	defer f.Close()
	if n, e := f.Write(body); e != nil || n != len(body) {
		return Error{DurabilityUnavailable}
	}
	if err = p.inject(ctx, "projection_stage_write"); err != nil {
		return err
	}
	if err = syncFile(f); err != nil {
		return err
	}
	if err = p.inject(ctx, "projection_stage_file"); err != nil {
		return err
	}
	if p.directory.Sync() != nil {
		return Error{DurabilityUnavailable}
	}
	if err = p.inject(ctx, "projection_stage_directory"); err != nil {
		return err
	}
	if err = p.verifyExpected(); err != nil {
		p.fence = ProjectionCorrupt
		return err
	}
	actual, exists, stageID, err := p.readFile(stage)
	heldID, e := physicalIdentity(f)
	if err != nil || e != nil || !exists || stageID != heldID || hash(actual) != hash(body) {
		p.fence = ProjectionCorrupt
		return Error{ProjectionCorrupt}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = publishProjectionFile(p.directory, stage); err != nil {
		p.fence = ProjectionUncertain
		return err
	}
	defer func() {
		if err != nil {
			p.fence = ProjectionUncertain
		}
	}()
	if err = p.inject(ctx, "projection_catalog_rename"); err != nil {
		return err
	}
	if p.directory.Sync() != nil {
		return Error{DurabilityUnavailable}
	}
	if err = p.inject(ctx, "projection_catalog_directory"); err != nil {
		return err
	}
	// Check namespace/root again without expecting the replaced old final file.
	oldCatalog, oldHash, oldID := p.catalog, p.fileHash, p.fileIdentity
	p.catalog, p.fileHash, p.fileIdentity = &c, hash(body), stageID
	if err = p.verifyExpected(); err != nil {
		p.catalog, p.fileHash, p.fileIdentity = oldCatalog, oldHash, oldID
		return err
	}
	if err = p.inject(ctx, "projection_catalog_acknowledgement"); err != nil {
		p.catalog, p.fileHash, p.fileIdentity = oldCatalog, oldHash, oldID
		return err
	}
	return nil
}

func (p *projectionIndex) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.directory != nil && p.directory.Close() != nil {
		err = Error{DurabilityUnavailable}
	}
	if p.root != nil && p.root.Close() != nil {
		err = Error{DurabilityUnavailable}
	}
	p.root, p.directory = nil, nil
	return err
}
