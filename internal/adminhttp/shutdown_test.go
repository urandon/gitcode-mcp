package adminhttp

import (
	"context"
	"testing"
	"time"
)

func TestWaitStoppedJoinsObservationPoller(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	c := New(Config{Snapshot: func(context.Context) (ObservationSnapshot, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return ObservationSnapshot{}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Start(ctx); err != nil {
		t.Fatal("start loopback fixture")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("poller did not start")
	}
	cancel()
	deadline, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if c.WaitStopped(deadline) {
		t.Fatal("blocked poller incorrectly reported stopped")
	}
	close(release)
	joined, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if !c.WaitStopped(joined) {
		t.Fatal("released poller did not join")
	}
}
func TestWaitStoppedBeforeStartIsNonblocking(t *testing.T) {
	c := New(Config{})
	if !c.WaitStopped(context.Background()) {
		t.Fatal("unstarted HTTP controller cannot stop")
	}
}
