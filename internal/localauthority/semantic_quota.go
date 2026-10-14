package localauthority

import (
	"context"
	"sync/atomic"

	"gitcode-mcp/internal/authority"
)

const (
	semanticProjectBytes   = int64(384 << 20)
	semanticProjectEntries = 8192
	semanticCohortBytes    = semanticProjectBytes * CoordinatorProjectLimit
	semanticCohortEntries  = semanticProjectEntries * CoordinatorProjectLimit
	semanticIOSlots        = 2
)

type semanticUsage struct {
	bytes   int64
	entries int
}

func (u semanticUsage) valid() bool {
	return u.bytes >= 0 && u.bytes <= semanticCohortBytes && u.entries >= 0 && u.entries <= semanticCohortEntries
}
func (u semanticUsage) within(cap semanticUsage) bool {
	return u.valid() && cap.valid() && u.bytes <= cap.bytes && u.entries <= cap.entries
}
func (u semanticUsage) add(v semanticUsage) (semanticUsage, bool) {
	if !u.valid() || !v.valid() || v.bytes > semanticCohortBytes-u.bytes || v.entries > semanticCohortEntries-u.entries {
		return semanticUsage{}, false
	}
	return semanticUsage{u.bytes + v.bytes, u.entries + v.entries}, true
}

var semanticProjectCapacity = semanticUsage{semanticProjectBytes, semanticProjectEntries}
var semanticCohortCapacity = semanticUsage{semanticCohortBytes, semanticCohortEntries}

type semanticQuotaBinding struct {
	project, instance authority.UUID
	manifest, root    string
}

func (b semanticQuotaBinding) valid() bool {
	return b.project.Valid() && b.instance.Valid() && validDigest(b.manifest) && validDigest(b.root)
}

// This private image is supplied by the future owned physical scanner. Pure
// quota validation neither proves its filesystem provenance nor index coverage.
type semanticQuotaImage struct {
	binding     semanticQuotaBinding
	usage       semanticUsage
	fingerprint string
}

func (i semanticQuotaImage) valid() bool {
	return i.binding.valid() && i.usage.valid() && validDigest(i.fingerprint)
}

// One manager belongs to one immutable prepared cohort. No component may
// construct another budget for a project: consumers receive its sealed scope.
// It owns memory bookkeeping only, not filesystem/provider/service effects.
type semanticQuota struct {
	mu        releaseMutex
	scopes    map[authority.UUID]*semanticQuotaScope
	sealed    atomic.Bool
	uncertain atomic.Bool
	ioActive  atomic.Int32
	ioSlots   [semanticIOSlots]atomic.Pointer[semanticIOState]
	closed    bool
}

type semanticQuotaScope struct {
	owner       *semanticQuota
	store       *Store
	binding     semanticQuotaBinding
	observed    bool
	image       semanticQuotaImage
	reservation *semanticReservation
	active      atomic.Pointer[semanticReservation]
}

func newSemanticQuota(ctx context.Context, stores []*Store) (*semanticQuota, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(stores) < 1 || len(stores) > CoordinatorProjectLimit {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	q := &semanticQuota{scopes: map[authority.UUID]*semanticQuotaScope{}}
	for _, s := range stores {
		b, err := semanticQuotaStoreBinding(ctx, s)
		if err != nil {
			return nil, err
		}
		if !b.valid() || q.scopes[b.project] != nil {
			return nil, authority.Error{Code: authority.Conflict}
		}
		q.scopes[b.project] = &semanticQuotaScope{owner: q, store: s, binding: b}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Claim all stores atomically before returning a scope. Never multiply a
	// writer's quota by constructing another component/manager, and never leave
	// a partial claim behind when a later cohort member is invalid or busy.
	locked := []*Store{}
	defer func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i].mu.Unlock()
		}
	}()
	for _, source := range stores {
		if !source.mu.TryLock() {
			return nil, authority.Error{Code: authority.Conflict}
		}
		locked = append(locked, source)
		if source.closed || source.semanticQuotaOwner != nil {
			return nil, authority.Error{Code: authority.Conflict}
		}
		if source.fenced || source.logicalFenced || source.lease == nil || source.lease.runtimeGuard == nil {
			return nil, Error{Corrupt}
		}
		bound := semanticQuotaBinding{source.manifest.ID, source.instance, manifestHash(source.manifest), source.lease.runtimeDirectoryIdentity}
		scope := q.scopes[bound.project]
		if scope == nil || scope.store != source || scope.binding != bound || source.manifest.Network.DataEgress != "loopback_embeddings" {
			return nil, authority.Error{Code: authority.Conflict}
		}
		if err := source.verifyPhysicalBinding(); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, source := range stores {
		source.semanticQuotaOwner = q
	}
	return q, nil
}

func semanticQuotaStoreBinding(ctx context.Context, s *Store) (semanticQuotaBinding, error) {
	if ctx.Err() != nil {
		return semanticQuotaBinding{}, ctx.Err()
	}
	if s == nil {
		return semanticQuotaBinding{}, Error{ReadOnly}
	}
	if !s.mu.TryLock() {
		return semanticQuotaBinding{}, authority.Error{Code: authority.Conflict}
	}
	defer s.mu.Unlock()
	if s.closed || s.lease == nil || s.lease.runtimeGuard == nil {
		return semanticQuotaBinding{}, Error{ReadOnly}
	}
	if s.fenced || s.logicalFenced {
		return semanticQuotaBinding{}, Error{Corrupt}
	}
	if s.manifest.Network.DataEgress != "loopback_embeddings" {
		return semanticQuotaBinding{}, authority.Error{Code: authority.PolicyDenied}
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		return semanticQuotaBinding{}, err
	}
	if ctx.Err() != nil {
		return semanticQuotaBinding{}, ctx.Err()
	}
	return semanticQuotaBinding{s.manifest.ID, s.instance, manifestHash(s.manifest), s.lease.runtimeDirectoryIdentity}, nil
}

func (q *semanticQuota) scopeFor(s *Store) (*semanticQuotaScope, error) {
	if q == nil {
		return nil, authority.Error{Code: authority.NoAuthority}
	}
	// Construction and store identity are immutable; never derive a new scope
	// from a caller UUID, cache identity or a copied binding assertion.
	for _, scope := range q.scopes {
		if scope.store == s {
			return scope, nil
		}
	}
	return nil, authority.Error{Code: authority.NoAuthority}
}

func (q *semanticQuota) owns(s *semanticQuotaScope) bool {
	return s != nil && s.owner == q && q.scopes[s.binding.project] == s
}

func (q *semanticQuota) totalLocked() (semanticUsage, bool) {
	u := semanticUsage{}
	for _, s := range q.scopes {
		if !s.observed {
			return semanticUsage{}, false
		}
		charge := s.image.usage
		if s.reservation != nil {
			charge = s.reservation.ceiling
		}
		var ok bool
		u, ok = u.add(charge)
		if !ok {
			return semanticUsage{}, false
		}
	}
	return u, true
}

// Admission observations may not replace an active envelope. Only its joined
// completion supplies the final physical image, preventing a second component
// from double-charging or freeing reserved growth while writes are still live.
func (q *semanticQuota) observe(s *semanticQuotaScope, image semanticQuotaImage) error {
	if q == nil || !q.owns(s) {
		return authority.Error{Code: authority.NoAuthority}
	}
	changed := q.mu.changed()
	if !q.mu.TryLock() {
		return projectionContention{changed}
	}
	defer q.mu.Unlock()
	if q.closed || q.sealed.Load() || q.uncertain.Load() || s.reservation != nil {
		return authority.Error{Code: authority.Conflict}
	}
	if !image.valid() || image.binding != s.binding {
		return Error{ProjectionCorrupt}
	}
	if !image.usage.within(semanticProjectCapacity) {
		q.uncertain.Store(true)
		return Error{Capacity}
	}
	s.image, s.observed = image, true
	return nil
}

type semanticReservation struct {
	owner    *semanticQuota
	scope    *semanticQuotaScope
	baseline semanticQuotaImage
	ceiling  semanticUsage
	joined   atomic.Bool
	terminal atomic.Uint32 // 0 active, 1 verified, 2 uncertain
	released bool          // owner admission guard
}

func (q *semanticQuota) reserve(s *semanticQuotaScope, growth semanticUsage) (*semanticReservation, error) {
	if q == nil || !q.owns(s) {
		return nil, authority.Error{Code: authority.NoAuthority}
	}
	changed := q.mu.changed()
	if !q.mu.TryLock() {
		return nil, projectionContention{changed}
	}
	defer q.mu.Unlock()
	if q.closed || q.sealed.Load() || q.uncertain.Load() || s.reservation != nil {
		return nil, authority.Error{Code: authority.Conflict}
	}
	if !growth.valid() {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	total, known := q.totalLocked()
	if !known {
		return nil, authority.Error{Code: authority.Conflict}
	}
	ceiling, ok := s.image.usage.add(growth)
	if !ok || !ceiling.within(semanticProjectCapacity) {
		return nil, Error{Capacity}
	}
	upper, ok := total.add(growth)
	if !ok || !upper.within(semanticCohortCapacity) {
		return nil, Error{Capacity}
	}
	r := &semanticReservation{owner: q, scope: s, baseline: s.image, ceiling: ceiling}
	s.reservation = r
	s.active.Store(r)
	return r, nil
}

func (r *semanticReservation) verifyLocked(image semanticQuotaImage) error {
	if !image.valid() || image.binding != r.scope.binding {
		return Error{ProjectionCorrupt}
	}
	if !image.usage.within(r.ceiling) {
		return Error{Capacity}
	}
	return nil
}

// Remeasurements check the frozen envelope, not observed+reserved growth. The
// group continues charging its upper ceiling until a qualified joined finish.
func (r *semanticReservation) verify(image semanticQuotaImage) error {
	if r == nil || r.owner == nil {
		return authority.Error{Code: authority.NoAuthority}
	}
	q := r.owner
	changed := q.mu.changed()
	if !q.mu.TryLock() {
		return projectionContention{changed}
	}
	defer q.mu.Unlock()
	if !q.owns(r.scope) || r.scope.reservation != r || r.released || r.joined.Load() || q.closed {
		return authority.Error{Code: authority.Conflict}
	}
	if err := r.verifyLocked(image); err != nil {
		q.uncertain.Store(true)
		return err
	}
	return nil
}

// The caller must already have joined every effect under this private token.
// An invalid or unavailable final image preserves the charge and a global
// admission fence, even if the worker itself has finished. No automatic refund.
func (r *semanticReservation) finish(image semanticQuotaImage) error {
	if r == nil || r.owner == nil {
		return authority.Error{Code: authority.NoAuthority}
	}
	q := r.owner
	changed := q.mu.changed()
	if !q.mu.TryLock() {
		return projectionContention{changed}
	}
	defer q.mu.Unlock()
	if !q.owns(r.scope) || r.scope.reservation != r || r.released || q.closed {
		return authority.Error{Code: authority.Conflict}
	}
	r.joined.Store(true)
	if err := r.verifyLocked(image); err != nil {
		r.terminal.CompareAndSwap(0, 2)
		q.uncertain.Store(true)
		return err
	}
	if q.uncertain.Load() || !r.terminal.CompareAndSwap(0, 1) {
		return authority.Error{Code: authority.Conflict}
	}
	r.scope.image = image
	r.scope.reservation = nil
	r.scope.active.Store(nil)
	r.released = true
	return nil
}

// Used after joined work cannot qualify its final inventory or admission lock
// before its proof deadline. Atomics seal new growth without waiting for that
// lock; the retained envelope is not released or converted into success.
func (r *semanticReservation) joinedUncertain() {
	if r != nil && r.owner != nil && r.owner.owns(r.scope) && r.scope.active.Load() == r && r.terminal.CompareAndSwap(0, 2) {
		r.owner.uncertain.Store(true)
		r.joined.Store(true)
	}
}

type semanticIOState struct {
	owner *semanticQuota
	slot  int
}
type semanticIOLease struct{ state *semanticIOState }

func (q *semanticQuota) acquireIO(s *semanticQuotaScope) (*semanticIOLease, error) {
	if q == nil || !q.owns(s) {
		return nil, authority.Error{Code: authority.NoAuthority}
	}
	changed := q.mu.changed()
	if !q.mu.TryLock() {
		return nil, projectionContention{changed}
	}
	defer q.mu.Unlock()
	if q.closed || q.sealed.Load() || q.uncertain.Load() || q.ioActive.Load() >= semanticIOSlots {
		return nil, authority.Error{Code: authority.Conflict}
	}
	var state *semanticIOState
	for i := range q.ioSlots {
		candidate := &semanticIOState{q, i}
		if q.ioSlots[i].CompareAndSwap(nil, candidate) {
			state = candidate
			break
		}
	}
	if state == nil {
		return nil, authority.Error{Code: authority.Conflict}
	}
	q.ioActive.Add(1)
	if q.sealed.Load() {
		q.ioSlots[state.slot].CompareAndSwap(state, nil)
		q.ioActive.Add(-1)
		return nil, authority.Error{Code: authority.Conflict}
	}
	return &semanticIOLease{state: state}, nil
}

func (l *semanticIOLease) release() error {
	if l == nil || l.state == nil || l.state.owner == nil || l.state.slot < 0 || l.state.slot >= semanticIOSlots {
		return authority.Error{Code: authority.NoAuthority}
	}
	state := l.state
	if !state.owner.ioSlots[state.slot].CompareAndSwap(state, nil) {
		return authority.Error{Code: authority.Conflict}
	}
	state.owner.ioActive.Add(-1)
	return nil
}

// Sealing never releases a physical owner or closes a Store. The actual owner
// must join its workers first; this final memory barrier refuses live tokens.
func (q *semanticQuota) seal() {
	if q != nil {
		q.sealed.Store(true)
	}
}
func (q *semanticQuota) closeJoined() error {
	if q == nil {
		return authority.Error{Code: authority.NoAuthority}
	}
	q.seal()
	changed := q.mu.changed()
	if !q.mu.TryLock() {
		return projectionContention{changed}
	}
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	if q.ioActive.Load() != 0 {
		return authority.Error{Code: authority.Conflict}
	}
	for _, s := range q.scopes {
		if s.reservation != nil && !s.reservation.joined.Load() {
			return authority.Error{Code: authority.Conflict}
		}
	}
	q.closed = true
	return nil
}
