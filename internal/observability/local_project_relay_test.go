package observability

import (
	"context"
	"sync"
	"testing"
)

func TestLocalProjectRelayBoundedClosedCohortAndSeal(t *testing.T) {
	const project = "11111111-1111-4111-8111-111111111111"
	r := NewLocalProjectRelay([]string{project})
	ctx := WithLocalProjectRelay(context.Background(), r)
	o := BindLocalProject(ctx, project)
	if !o.Bound() || o.Retained() || HasObservation(ctx) || MetricsFromContext(ctx) != nil || BindLocalProject(ctx, "foreign").Bound() {
		t.Fatal("relay widened trusted cohort or installed ordinary observation runtime")
	}
	for i := 0; i < LocalProjectRelayCapacity+1; i++ {
		o.SourceCommitted()
	}
	o.PhaseChanged("index_job", "unknown")
	r.emit(LocalProjectTransition{KnownRef("project", "foreign").value, LocalAuthorityCommitted, "authority_commit", "committed"})
	if len(r.events) != LocalProjectRelayCapacity || r.Lost() != 1 {
		t.Fatal("relay bound or closed admission changed")
	}
	r.mu.Lock()
	o.DocumentCommitted() // may not wait for an observation reader
	r.mu.Unlock()
	if r.Lost() != 2 {
		t.Fatal("relay contention lost partial coverage")
	}
	r.Seal()
	count := 0
	for transition := range r.Events() {
		if !transition.Valid() || transition.ProjectRef != o.ProjectRef() || transition.Code != LocalAuthorityCommitted {
			t.Fatal("relay retained unchecked event identity")
		}
		count++
	}
	o.SourceCommitted()
	o.UnconfirmedCommit()
	r.Seal()
	if count != LocalProjectRelayCapacity || r.Lost() != 2 {
		t.Fatal("seal changed coverage or allowed post-seal admission")
	}
}

func TestLocalProjectRelayConcurrentSealDoesNotBlockOwners(t *testing.T) {
	const project = "11111111-1111-4111-8111-111111111111"
	r := NewLocalProjectRelay([]string{project})
	o := BindLocalProject(WithLocalProjectRelay(context.Background(), r), project)
	var owners sync.WaitGroup
	for i := 0; i < 8; i++ {
		owners.Add(1)
		go func() {
			defer owners.Done()
			for j := 0; j < 100; j++ {
				o.PhaseChanged("index_job", "queued")
			}
		}()
	}
	r.Seal()
	owners.Wait()
	for v := range r.Events() {
		if !v.Valid() {
			t.Fatal("concurrent owner retained an invalid boundary")
		}
	}
	if len(r.events) != 0 {
		t.Fatal("post-seal event escaped bounded queue")
	}
}

func TestLocalProjectRelayLossLowerBoundAndInvalidCohort(t *testing.T) {
	if NewLocalProjectRelay(nil) != nil || NewLocalProjectRelay([]string{""}) != nil || NewLocalProjectRelay([]string{"same", "same"}) != nil || NewLocalProjectRelay(make([]string, 17)) != nil {
		t.Fatal("relay accepted absent, duplicate or unbounded cohort")
	}
	r := NewLocalProjectRelay([]string{"synthetic-admitted-project"})
	r.loss.Store(LocalProjectRelayMaxLoss + 100)
	if r.Lost() != LocalProjectRelayMaxLoss {
		t.Fatal("relay loss lower bound wrapped or exceeded wire cap")
	}
	r.Seal()
	r.omit() // a late producer cannot change the final seal
	if r.Lost() != LocalProjectRelayMaxLoss {
		t.Fatal("late producer changed a sealed loss lower bound")
	}
}

func TestLocalProjectRelayConcurrentLossAndSealRemainMonotonic(t *testing.T) {
	r := NewLocalProjectRelay([]string{"synthetic-admitted-project"})
	r.loss.Store(7)
	var producers sync.WaitGroup
	for i := 0; i < 8; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for j := 0; j < 1000; j++ {
				r.omit()
			}
		}()
	}
	done := make(chan struct{})
	go func() { r.Seal(); producers.Wait(); close(done) }()
	previous := uint64(7)
	for {
		current := r.Lost()
		if current < previous || current > 8007 {
			t.Fatal("concurrent native loss lower bound regressed or invented loss")
		}
		previous = current
		select {
		case <-done:
			sealed := r.Lost()
			r.omit()
			if sealed < previous || r.Lost() != sealed {
				t.Fatal("late omission changed frozen native loss")
			}
			return
		default:
		}
	}
}
