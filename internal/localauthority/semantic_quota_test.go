package localauthority

import (
	"context"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"gitcode-mcp/internal/authority"
)

func semanticPreparedStore(t *testing.T) *Store {
	return semanticPreparedStoreFor(t, testProject)
}

func semanticPreparedStoreFor(t *testing.T, id authority.UUID) *Store {
	s, _ := semanticPreparedWriterFixture(t, id)
	return s
}

func semanticPreparedWriterFixture(t *testing.T, id authority.UUID) (*Store, *PreparedRegistry) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("writer persistence is not qualified on this platform")
	}
	r, err := newRegistry(privateTestDirectory(t))
	if err != nil {
		t.Fatal("prepared registry fixture failed")
	}
	dir := privateTestDirectory(t)
	m := testManifest()
	m.ID = id
	m.Network.DataEgress = "loopback_embeddings"
	s, err := Initialize(context.Background(), r, dir, m)
	if err != nil {
		t.Fatal("actual authority initialization failed")
	}
	if s.Close() != nil {
		t.Fatal("initializer close failed")
	}
	p, err := r.PrepareRuntime()
	if err != nil {
		t.Fatal("trusted runtime fixture failed")
	}
	t.Cleanup(func() {
		if p.Close() != nil {
			t.Error("prepared registry fixture retained an unjoined writer")
		}
	})
	s, err = p.OpenWriter(context.Background(), dir, id)
	if err != nil {
		t.Fatal("actual prepared writer unavailable")
	}
	t.Cleanup(func() {
		if s.Close() != nil {
			t.Error("prepared writer fixture retained unjoined work")
		}
	})
	return s, p
}

func semanticQuotaFixture(t *testing.T, usage semanticUsage) (*semanticQuota, *semanticQuotaScope, semanticQuotaImage) {
	t.Helper()
	s := semanticPreparedStore(t)
	q, err := newSemanticQuota(context.Background(), []*Store{s})
	if err != nil {
		t.Fatal("quota fixture admission failed")
	}
	scope, err := q.scopeFor(s)
	if err != nil {
		t.Fatal("quota fixture scope unavailable")
	}
	image := semanticQuotaImage{scope.binding, usage, hash([]byte("synthetic-quota-image"))}
	if q.observe(scope, image) != nil {
		t.Fatal("quota fixture observation failed")
	}
	return q, scope, image
}

func TestSemanticQuotaEnvelopeNotDoubleCharged(t *testing.T) {
	q, s, image := semanticQuotaFixture(t, semanticUsage{100, 1})
	r, err := q.reserve(s, semanticUsage{100, 2})
	if err != nil {
		t.Fatal("bounded reserve failed")
	}
	for _, u := range []semanticUsage{{100, 1}, {150, 2}, {200, 3}} {
		observed := image
		observed.usage = u
		if r.verify(observed) != nil {
			t.Fatal("reserved growth was double charged")
		}
		if q.observe(s, observed) == nil {
			t.Fatal("second component replaced an active envelope")
		}
		if _, err = q.reserve(s, semanticUsage{}); err == nil {
			t.Fatal("second component acquired the same project's budget")
		}
		q.mu.Lock()
		total, known := q.totalLocked()
		q.mu.Unlock()
		if !known || total != (semanticUsage{200, 3}) {
			t.Fatal("upper envelope was refunded before joined finish")
		}
	}
	image.usage = semanticUsage{175, 2}
	if r.finish(image) != nil {
		t.Fatal("qualified joined finish failed")
	}
	if r.finish(image) == nil {
		t.Fatal("double reservation release accepted")
	}
	r.joinedUncertain()
	if q.uncertain.Load() {
		t.Fatal("late completion poisoned an already released reservation")
	}
	q.mu.Lock()
	total, known := q.totalLocked()
	q.mu.Unlock()
	if !known || total != image.usage {
		t.Fatal("qualified actual charge not retained")
	}
	last, err := q.reserve(s, semanticUsage{25, 1})
	if err != nil {
		t.Fatal("released capacity remained unavailable")
	}
	if last.finish(image) != nil {
		t.Fatal("last fixture reservation did not join")
	}
}

func TestSemanticQuotaWriterLeaseHeldUntilJoinedAndReopen(t *testing.T) {
	for _, pending := range []string{"io", "reservation", "mutex"} {
		t.Run(pending, func(t *testing.T) {
			s, p := semanticPreparedWriterFixture(t, testProject)
			q, err := newSemanticQuota(context.Background(), []*Store{s})
			if err != nil {
				t.Fatal("prepared quota unavailable")
			}
			scope, _ := q.scopeFor(s)
			image := semanticQuotaImage{scope.binding, semanticUsage{}, hash([]byte("lifetime-fixture"))}
			if q.observe(scope, image) != nil {
				t.Fatal("fixture observation failed")
			}
			var join func() error
			switch pending {
			case "io":
				lease, e := q.acquireIO(scope)
				if e != nil {
					t.Fatal("I/O lease unavailable")
				}
				join = lease.release
			case "reservation":
				r, e := q.reserve(scope, semanticUsage{1, 1})
				if e != nil {
					t.Fatal("growth envelope unavailable")
				}
				join = func() error { return r.finish(image) }
			case "mutex":
				q.mu.Lock()
				join = func() error { q.mu.Unlock(); return nil }
			}
			if s.Close() == nil || s.closed || !q.sealed.Load() {
				t.Error("pending work released or failed to seal its writer")
			}
			if other, e := p.OpenWriter(context.Background(), s.directory, testProject); e == nil {
				t.Error("physical writer ownership escaped before joined close")
				_ = other.Close()
			}
			if p.Close() == nil {
				t.Error("preparation released a still-owned writer")
			}
			if join() != nil || s.Close() != nil {
				t.Fatal("joined writer could not close")
			}
			if _, e := q.reserve(scope, semanticUsage{}); e == nil {
				t.Error("released writer's old envelope remained admissible")
			}
			if lease, e := q.acquireIO(scope); e == nil {
				t.Error("released writer's old I/O capability remained admissible")
				_ = lease.release()
			}
			fresh, e := p.OpenWriter(context.Background(), s.directory, testProject)
			if e != nil {
				t.Fatal("joined physical owner could not reopen")
			}
			t.Cleanup(func() {
				if fresh.Close() != nil {
					t.Error("reopened writer failed to close")
				}
			})
			if _, e = newSemanticQuota(context.Background(), []*Store{fresh}); e != nil {
				t.Fatal("fresh actual writer could not claim its single budget")
			}
		})
	}
}

func TestSemanticQuotaAmbiguousFinishRetainsEnvelopeAndFence(t *testing.T) {
	q, s, image := semanticQuotaFixture(t, semanticUsage{100, 1})
	r, err := q.reserve(s, semanticUsage{100, 2})
	if err != nil {
		t.Fatal("reservation failed")
	}
	bad := image
	bad.usage = semanticUsage{201, 3}
	if r.finish(bad) == nil || !r.joined.Load() || !q.uncertain.Load() {
		t.Fatal("unqualified finish became a refund")
	}
	q.mu.Lock()
	total, known := q.totalLocked()
	q.mu.Unlock()
	if !known || total != (semanticUsage{200, 3}) || s.reservation != r {
		t.Fatal("uncertain envelope disappeared")
	}
	if _, err = q.reserve(s, semanticUsage{}); err == nil {
		t.Fatal("uncertain growth accepted")
	}
	if q.observe(s, image) == nil {
		t.Fatal("new observation erased uncertain history")
	}
	if _, err = q.acquireIO(s); err == nil {
		t.Fatal("uncertain I/O admitted")
	}
	if q.closeJoined() != nil {
		t.Fatal("already joined uncertain worker prevented safe memory seal")
	}
}

func TestSemanticQuotaBindingUnknownAndNumericBounds(t *testing.T) {
	q, s, image := semanticQuotaFixture(t, semanticUsage{})
	foreign := &semanticQuotaScope{owner: q, store: s.store, binding: s.binding}
	foreign.binding.project = semanticTestBuild
	if _, err := q.reserve(foreign, semanticUsage{}); err == nil {
		t.Fatal("copied scope assertion granted quota")
	}
	bad := image
	bad.binding.instance = testProject
	if q.observe(s, bad) == nil {
		t.Fatal("foreign inventory binding accepted")
	}
	for _, u := range []semanticUsage{{-1, 0}, {0, -1}, {math.MaxInt64, 0}, {0, math.MaxInt}, {semanticProjectBytes + 1, 0}, {0, semanticProjectEntries + 1}} {
		if _, err := q.reserve(s, u); err == nil {
			t.Fatal("negative/overflow/project limit accepted")
		}
	}
	if _, ok := (semanticUsage{semanticCohortBytes, semanticCohortEntries}).add(semanticUsage{1, 1}); ok {
		t.Fatal("cohort addition overflow accepted")
	}
	q.scopes[s.binding.project].observed = false
	if _, err := q.reserve(s, semanticUsage{}); err == nil {
		t.Fatal("unobserved cohort granted growth")
	}
}

func TestSemanticQuotaImmediateUncertaintyAndJoinSeal(t *testing.T) {
	q, s, image := semanticQuotaFixture(t, semanticUsage{})
	r, err := q.reserve(s, semanticUsage{1, 1})
	if err != nil {
		t.Fatal("reserve failed")
	}
	if q.closeJoined() == nil {
		t.Fatal("unjoined worker passed close barrier")
	}
	if _, err = q.reserve(s, semanticUsage{}); err == nil {
		t.Fatal("closing cohort accepted new growth")
	}
	q.mu.Lock()
	r.joinedUncertain()
	q.mu.Unlock()
	if !r.joined.Load() || !q.uncertain.Load() {
		t.Fatal("uncertainty waited for the admission mutex")
	}
	if r.finish(image) == nil {
		t.Fatal("uncertain proof was silently forgiven")
	}
	if q.closeJoined() != nil || !q.closed {
		t.Fatal("joined closing barrier failed")
	}
}

func TestSemanticQuotaJoinedIOSlotsAndDoubleRelease(t *testing.T) {
	q, s, _ := semanticQuotaFixture(t, semanticUsage{})
	a, err := q.acquireIO(s)
	if err != nil {
		t.Fatal("first I/O lease failed")
	}
	b, err := q.acquireIO(s)
	if err != nil {
		t.Fatal("second I/O lease failed")
	}
	if _, err = q.acquireIO(s); err == nil || q.ioActive.Load() != 2 {
		t.Fatal("I/O cohort exceeded two joined leases")
	}
	fake := semanticIOLease{state: &semanticIOState{q, a.state.slot}}
	if fake.release() == nil || q.ioActive.Load() != 2 {
		t.Fatal("forged I/O assertion released a joined lease")
	}
	copyOfA := *a
	if a.release() != nil || a.release() == nil || q.ioActive.Load() != 1 {
		t.Fatal("I/O double release corrupted the count")
	}
	if copyOfA.release() == nil || q.ioActive.Load() != 1 {
		t.Fatal("copied lease released capacity twice")
	}
	if q.closeJoined() == nil {
		t.Fatal("unjoined I/O passed closing barrier")
	}
	if _, err = q.acquireIO(s); err == nil {
		t.Fatal("sealed cohort admitted I/O")
	}
	if b.release() != nil || q.closeJoined() != nil || q.ioActive.Load() != 0 {
		t.Fatal("joined I/O did not close")
	}
}

func TestSemanticQuotaConcurrentIOSlotAdmission(t *testing.T) {
	q, s, _ := semanticQuotaFixture(t, semanticUsage{})
	var current, peak atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := q.acquireIO(s)
			if err != nil {
				return
			}
			value := current.Add(1)
			for {
				previous := peak.Load()
				if value <= previous || peak.CompareAndSwap(previous, value) {
					break
				}
			}
			current.Add(-1)
			if lease.release() != nil {
				t.Error("joined lease release failed")
			}
		}()
	}
	close(start)
	wg.Wait()
	if peak.Load() > 2 || q.ioActive.Load() != 0 {
		t.Fatal("concurrent cohort budget escaped")
	}
	if q.closeJoined() != nil {
		t.Fatal("all concurrent workers joined but seal failed")
	}
}

func TestSemanticQuotaPreparedOriginAndNoDuplicateScope(t *testing.T) {
	s := semanticPreparedStore(t)
	if _, err := newSemanticQuota(context.Background(), []*Store{s, s}); err == nil {
		t.Fatal("duplicate project multiplied quota")
	}
	if s.semanticQuotaOwner != nil {
		t.Fatal("invalid cohort left a partial quota claim")
	}
	if _, err := newSemanticQuota(context.Background(), nil); err == nil {
		t.Fatal("empty cohort admitted")
	}
	if _, err := newSemanticQuota(context.Background(), make([]*Store, CoordinatorProjectLimit+1)); err == nil {
		t.Fatal("oversized cohort admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newSemanticQuota(ctx, []*Store{s}); err != context.Canceled {
		t.Fatal("cancelled cohort admitted")
	}
	_, deny, _ := projectionFixture(t)
	if _, err := newSemanticQuota(context.Background(), []*Store{deny}); err == nil {
		t.Fatal("unprepared/deny authority admitted")
	}
	q, err := newSemanticQuota(context.Background(), []*Store{s})
	if err != nil {
		t.Fatal("valid writer claim failed")
	}
	if _, err = newSemanticQuota(context.Background(), []*Store{s}); err == nil {
		t.Fatal("second component multiplied a writer's quota")
	}
	if q.closeJoined() != nil {
		t.Fatal("empty memory owner failed close")
	}
	if _, err = newSemanticQuota(context.Background(), []*Store{s}); err == nil {
		t.Fatal("closed component recycled a live writer's budget origin")
	}
	if s.Close() != nil {
		t.Fatal("fixture close failed")
	}
	if _, err := newSemanticQuota(context.Background(), []*Store{s}); err == nil {
		t.Fatal("closed writer admitted")
	}
}

func TestSemanticQuotaCohortNeedsAllPhysicalImages(t *testing.T) {
	a, b := semanticPreparedStoreFor(t, testProject), semanticPreparedStoreFor(t, semanticTestBuild)
	q, err := newSemanticQuota(context.Background(), []*Store{a, b})
	if err != nil {
		t.Fatal("actual two-project cohort failed")
	}
	sa, _ := q.scopeFor(a)
	sb, _ := q.scopeFor(b)
	first := semanticQuotaImage{sa.binding, semanticUsage{semanticProjectBytes - 1, semanticProjectEntries - 1}, hash([]byte("first-inventory"))}
	second := semanticQuotaImage{sb.binding, semanticUsage{semanticProjectBytes, semanticProjectEntries}, hash([]byte("second-inventory"))}
	if q.observe(sa, first) != nil {
		t.Fatal("first physical image failed")
	}
	if _, err = q.reserve(sa, semanticUsage{1, 1}); err == nil {
		t.Fatal("unobserved second project vanished from quota")
	}
	if q.observe(sb, second) != nil {
		t.Fatal("second physical image failed")
	}
	r, err := q.reserve(sa, semanticUsage{1, 1})
	if err != nil {
		t.Fatal("exact project envelope refused")
	}
	if _, err = q.reserve(sb, semanticUsage{1, 0}); err == nil {
		t.Fatal("full project borrowed another project's allowance")
	}
	q.mu.Lock()
	total, known := q.totalLocked()
	q.mu.Unlock()
	if !known || total != (semanticUsage{2 * semanticProjectBytes, 2 * semanticProjectEntries}) {
		t.Fatal("cohort charge omitted another component/project")
	}
	if r.finish(first) != nil {
		t.Fatal("joined no-growth result failed")
	}
}

func TestSemanticQuotaContentionCannotForgiveUncertainWork(t *testing.T) {
	q, s, image := semanticQuotaFixture(t, semanticUsage{})
	r, err := q.reserve(s, semanticUsage{10, 1})
	if err != nil {
		t.Fatal("reserve failed")
	}
	q.mu.Lock()
	if r.finish(image) == nil {
		t.Fatal("contended finish silently released charge")
	}
	r.joinedUncertain()
	q.mu.Unlock()
	if r.finish(image) == nil || s.reservation != r || !q.uncertain.Load() {
		t.Fatal("ambiguous contended completion lost its fence")
	}
	if q.closeJoined() != nil {
		t.Fatal("joined uncertain memory owner failed close")
	}
}
