package localauthority

import (
	"context"
	"errors"

	"gitcode-mcp/internal/authority"
)

func waitResumeIndexBuilder(ctx context.Context, p *projectionIndex, selected indexSelection) (*projectionBuilder, error) {
	for {
		b, err := p.resumeIndexBuilder(ctx, selected)
		var busy projectionContention
		if !errors.As(err, &busy) {
			return b, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-busy.changed:
		}
	}
}

func selectionForIndexBuilder(b *projectionBuilder) indexSelection {
	c := b.catalog
	return indexSelection{ProjectID: c.ProjectID, TrackerInstance: c.TrackerInstance, ManifestHash: c.ManifestHash, Namespace: c.Namespace, Sequence: c.Sequence, Epoch: c.Epoch, Generation: c.Generation, BuildID: c.BuildID, BaseDigest: b.baseHash}
}

// Resume replays only the fixed canonical intake. A new random epoch/build
// allocation is never permission to retarget the original durable job.
func (p *projectionIndex) resumeIndexBuilder(ctx context.Context, s indexSelection) (*projectionBuilder, error) {
	if !s.valid() || s.ProjectID != p.store.manifest.ID || s.TrackerInstance != p.store.instance || s.ManifestHash != manifestHash(p.store.manifest) {
		return nil, Error{ProjectionCorrupt}
	}
	b, err := p.newBuilder(ctx, s.Sequence)
	if err != nil {
		return nil, err
	}
	if b.baseHash != s.BaseDigest || b.catalog.Generation != s.Generation || s.Generation > 1 && b.catalog.Epoch != s.Epoch {
		return nil, authority.Error{Code: authority.Conflict}
	}
	b.catalog.Epoch, b.catalog.BuildID = s.Epoch, s.BuildID
	return b, nil
}

func (p *projectionIndex) indexProof(ctx context.Context, selected indexSelection) (indexPublication, bool, error) {
	if ctx.Err() != nil {
		return indexPublication{}, false, ctx.Err()
	}
	if !p.mu.TryLock() {
		return indexPublication{}, false, authority.Error{Code: authority.Conflict}
	}
	defer p.mu.Unlock()
	if p.closed {
		return indexPublication{}, false, Error{ProjectionUncertain}
	}
	if p.fence != "" {
		return indexPublication{}, false, Error{p.fence}
	}
	if err := p.verifyExpected(); err != nil {
		p.fence = ProjectionCorrupt
		return indexPublication{}, false, err
	}
	if p.catalog == nil || p.catalog.BuildID != selected.BuildID {
		return indexPublication{}, false, nil
	}
	proof, err := indexPublicationProof(selected, *p.catalog, p.fileHash)
	if err != nil {
		return indexPublication{}, false, err
	}
	return proof, true, nil
}
