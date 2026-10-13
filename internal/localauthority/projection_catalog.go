package localauthority

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	"gitcode-mcp/internal/authority"
)

const projectionNamespace = "canonical-content-v1"
const MaxProjectionEntries = 4096
const MaxProjectionBytes = 2 << 20
const MaxProjectionStages = 4
const MaxProjectionStageBytes = 8 << 20
const projectionStepBudget = 128

// This is content identity metadata, not full-text/vector coverage or a copy
// of source bytes. A citation is always hydrated from canonical authority.
type projectionEntry struct {
	Citation authority.Citation `json:"citation"`
	Sequence uint64             `json:"sequence"`
	Digest   string             `json:"digest"`
	State    string             `json:"state"`
}

func (e projectionEntry) valid(project authority.UUID, sequence uint64) bool {
	if e.Citation.Validate() != nil || e.Citation.ProjectID != project || e.Sequence < e.Citation.Revision || e.Sequence > sequence || !validDigest(e.Digest) {
		return false
	}
	switch e.State {
	case "active":
		return true
	case "tombstone":
		return e.Citation.Revision > 1 && (e.Citation.Kind == authority.Document || e.Citation.Kind == authority.Link)
	case "archived":
		return e.Citation.Kind == authority.Label || e.Citation.Kind == authority.Milestone
	}
	return false
}

type projectionCatalog struct {
	Schema          int               `json:"schema"`
	Namespace       string            `json:"namespace"`
	ProjectID       authority.UUID    `json:"project_id"`
	TrackerInstance authority.UUID    `json:"tracker_instance"`
	ManifestHash    string            `json:"manifest_hash"`
	Epoch           authority.UUID    `json:"epoch"`
	BuildID         authority.UUID    `json:"build_id"`
	Generation      uint64            `json:"generation"`
	Sequence        uint64            `json:"sequence"`
	Entries         []projectionEntry `json:"entries"`
}

func (p projectionCatalog) valid() bool {
	if p.Schema != 1 || p.Namespace != projectionNamespace || !p.ProjectID.Valid() || !p.TrackerInstance.Valid() || !validDigest(p.ManifestHash) || !p.Epoch.Valid() || !p.BuildID.Valid() || p.Generation < 1 || p.Generation > math.MaxInt64 || p.Sequence > math.MaxInt64 || p.Entries == nil || len(p.Entries) > MaxProjectionEntries || uint64(len(p.Entries)) > p.Sequence {
		return false
	}
	var last authority.UUID
	for _, e := range p.Entries {
		if !e.valid(p.ProjectID, p.Sequence) || e.Citation.EntityID <= last {
			return false
		}
		last = e.Citation.EntityID
	}
	return true
}

type projectionEnvelope struct {
	Catalog projectionCatalog `json:"catalog"`
	Digest  string            `json:"digest"`
}

func (p projectionEnvelope) Validate() error {
	body, err := json.Marshal(p.Catalog)
	if err != nil || !p.Catalog.valid() || hash(body) != p.Digest {
		return Error{ProjectionCorrupt}
	}
	return nil
}

func cloneProjection(p projectionCatalog) projectionCatalog {
	p.Entries = append([]projectionEntry{}, p.Entries...)
	return p
}

func projectedEntry(v SnapshotEntity) (projectionEntry, error) {
	e := projectionEntry{Citation: v.Entity.Citation, Sequence: v.Entity.ProjectSequence, Digest: v.ContentDigest, State: "active"}
	if v.Entity.Validate() != nil || hash(v.Entity.Payload) != v.ContentDigest {
		return e, Error{ProjectionCorrupt}
	}
	if v.Entity.Tombstone {
		e.State = "tombstone"
	} else {
		switch v.Entity.Citation.Kind {
		case authority.Label:
			var p authority.LabelPayload
			if authority.DecodeJSON(v.Entity.Payload, &p) != nil {
				return e, Error{ProjectionCorrupt}
			}
			if p.Archived {
				e.State = "archived"
			}
		case authority.Milestone:
			var p authority.MilestonePayload
			if authority.DecodeJSON(v.Entity.Payload, &p) != nil {
				return e, Error{ProjectionCorrupt}
			}
			if p.Archived {
				e.State = "archived"
			}
		}
	}
	return e, nil
}

// The segmented builder is owned by one future daemon job. Only complete
// canonical traversal can publish; a failure permanently rejects this candidate.
// It retains citations/digests only, never the transient Snapshot payloads.
type projectionBuilder struct {
	mu       releaseMutex
	owner    *projectionIndex
	catalog  projectionCatalog
	baseHash string
	cursor   string
	done     bool
	failed   bool
	// Production intake fixes this to MaxProjectionEntries. Private smaller
	// fixture limits exercise the real capacity branch without slow huge stores.
	maxEntries int
}

func (b *projectionBuilder) step(ctx context.Context, budget int) (scanned int, err error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if budget < 1 || budget > projectionStepBudget {
		return 0, authority.Error{Code: authority.InvalidContract}
	}
	if !b.mu.TryLock() {
		return 0, authority.Error{Code: authority.Conflict}
	}
	defer b.mu.Unlock()
	if b.owner == nil || b.failed || b.maxEntries < 1 || b.maxEntries > MaxProjectionEntries {
		return 0, Error{ProjectionCorrupt}
	}
	if b.done {
		return 0, nil
	}
	defer func() {
		if err != nil {
			b.failed = true
		}
	}()
	for scanned < budget {
		limit := budget - scanned
		if limit > 25 {
			limit = 25
		}
		sequence := b.catalog.Sequence
		page, e := b.owner.store.Snapshot(ctx, SnapshotRequest{ProjectID: b.catalog.ProjectID, Limit: limit, Cursor: b.cursor, AsOf: &sequence})
		if e != nil {
			return scanned, e
		}
		if page.ProjectID != b.catalog.ProjectID || page.Sequence != sequence || page.Scanned > limit || page.Scanned < len(page.Entities) || page.NextCursor != "" && (page.Scanned == 0 || page.NextCursor == b.cursor) {
			return scanned, Error{ProjectionCorrupt}
		}
		scanned += page.Scanned
		for _, source := range page.Entities {
			if len(b.catalog.Entries) >= b.maxEntries {
				return scanned, Error{Capacity}
			}
			entry, e := projectedEntry(source)
			if e != nil || len(b.catalog.Entries) > 0 && entry.Citation.EntityID <= b.catalog.Entries[len(b.catalog.Entries)-1].Citation.EntityID {
				return scanned, Error{ProjectionCorrupt}
			}
			b.catalog.Entries = append(b.catalog.Entries, entry)
		}
		b.cursor = page.NextCursor
		if b.cursor == "" {
			b.done = true
			break
		}
	}
	return scanned, nil
}

// Comparison is bounded to the supplied canonical snapshot page. Reusable
// includes a newer citation with identical content; it is NOT proof of vectors.
type projectionComparison struct {
	Scanned, Reusable, Stale, Missing, Inactive int
	Partial                                     bool
}

func compareProjection(catalog projectionCatalog, page SnapshotPage) (projectionComparison, error) {
	c := projectionComparison{Scanned: page.Scanned, Partial: page.NextCursor != ""}
	if !catalog.valid() || catalog.ProjectID != page.ProjectID || catalog.Sequence > page.Sequence || page.Sequence > math.MaxInt64 || page.Scanned < len(page.Entities) || page.Scanned > 100 || page.Scanned < 0 {
		return c, Error{ProjectionCorrupt}
	}
	var last authority.UUID
	for _, source := range page.Entities {
		e, err := projectedEntry(source)
		if err != nil || !e.valid(page.ProjectID, page.Sequence) || e.Citation.EntityID <= last {
			return c, Error{ProjectionCorrupt}
		}
		last = e.Citation.EntityID
		if e.State != "active" {
			c.Inactive++
			continue
		}
		i := sort.Search(len(catalog.Entries), func(i int) bool { return catalog.Entries[i].Citation.EntityID >= e.Citation.EntityID })
		if i == len(catalog.Entries) || catalog.Entries[i].Citation.EntityID != e.Citation.EntityID || catalog.Entries[i].State != "active" {
			c.Missing++
		} else if catalog.Entries[i].Digest != e.Digest || catalog.Entries[i].Citation.Kind != e.Citation.Kind {
			c.Stale++
		} else {
			c.Reusable++
		}
	}
	return c, nil
}
