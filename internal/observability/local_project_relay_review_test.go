package observability

import (
	"runtime"
	"testing"
)

func TestReviewRelaySealCannotRollBackObservedLoss(t *testing.T) {
	old := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(old)
	for i := 0; i < 200000; i++ {
		r := NewLocalProjectRelay([]string{"synthetic-project"})
		r.loss.Store(7)
		done := make(chan struct{})
		go func() { r.Seal(); close(done) }()
		for {
			if r.Lost() < 7 {
				<-done
				t.Fatal("sealing regressed a previously observed cumulative loss")
			}
			select {
			case <-done:
				goto next
			default:
			}
		}
	next:
	}
}
