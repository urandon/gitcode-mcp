//go:build darwin || linux

package observability

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func qualificationCollector(t *testing.T) *Collector {
	t.Helper()
	c := NewQualificationCollector(Config{Directory: testDirectory(t), ManagedOutput: true})
	t.Cleanup(func() { flush(t, c, true) })
	await(t, func() bool {
		page, err := c.Query(Filter{Code: Boot}, "", 50)
		return err == nil && len(page.Events) == 1
	})
	return c
}

func qualificationAdmission() Context {
	return Context{Job: KnownRef("job", "fixture-window"), Correlation: KnownRef("correlation", "fixture-window"), ExecutionDetails: ExecutionDetails{Phase: "admission", Outcome: "queued"}}
}

func TestQualificationWindowIsDisabledByDefault(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	defer flush(t, c, true)
	resume, err := c.PauseWriterForQualification(context.Background())
	var typed QueryError
	if resume != nil || !errors.As(err, &typed) || typed.Reason != "qualification_disabled" || c.qualificationRequests != nil {
		t.Fatal("ordinary collector admitted a qualification pause")
	}
}

func TestQualificationWindowEnqueuesOnceAndPreservesQueueLimit(t *testing.T) {
	c := qualificationCollector(t)
	resume, err := c.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("qualification writer boundary unavailable")
	}
	defer resume()
	if !c.mu.TryLock() {
		t.Fatal("paused writer retained its collector mutex")
	}
	before := c.sequence
	c.mu.Unlock()
	for n := 0; n < MaxEvents; n++ {
		if !c.Emit(JobAdmitted, qualificationAdmission()) {
			t.Fatal("one-shot fixture emission rejected before queue capacity")
		}
	}
	if c.Emit(JobAdmitted, qualificationAdmission()) {
		t.Fatal("qualification window bypassed the real queue bound")
	}
	c.mu.Lock()
	if c.sequence != before || c.queued != MaxEvents || c.coverage.Dropped != 1 || c.coverage.Reason != "queue_overflow" {
		c.mu.Unlock()
		t.Fatal("paused writer changed persistence or loss conservation")
	}
	c.mu.Unlock()
	if !resume() || resume() { // cleanup retry is harmless, never another emission
		t.Fatal("qualification release did not distinguish active from released")
	}
	flush(t, c, true)
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, event := range c.events {
		if event.Code == JobAdmitted {
			count++
		}
	}
	if count != MaxEvents || c.queued != 0 {
		t.Fatal("released writer did not persist exactly the admitted entries")
	}
}

func TestQualificationWindowExclusiveCancellationAndShutdown(t *testing.T) {
	for _, stop := range []string{"cancel", "close"} {
		t.Run(stop, func(t *testing.T) {
			c := qualificationCollector(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resume, err := c.PauseWriterForQualification(ctx)
			if err != nil {
				t.Fatal("qualification window unavailable")
			}
			defer resume()
			other, err := c.PauseWriterForQualification(context.Background())
			var typed QueryError
			if other != nil || !errors.As(err, &typed) || typed.Reason != "qualification_window_active" {
				t.Fatal("overlapping qualification owner admitted")
			}
			if !c.Emit(JobAdmitted, qualificationAdmission()) {
				t.Fatal("actual bounded queue admission failed")
			}
			if stop == "cancel" {
				cancel()
				await(t, func() bool { return c.qualificationOwner.Load() == nil })
			}
			flush(t, c, true) // also releases an active window on stop
			page, err := c.Query(Filter{Code: JobAdmitted}, "", 50)
			if err != nil || len(page.Events) != 1 || c.qualificationOwner.Load() != nil {
				t.Fatal("cancel/shutdown lost queued work or retained the window")
			}
			if resume() {
				t.Fatal("cancelled/closed window was reported as active")
			}
			late, err := c.PauseWriterForQualification(context.Background())
			if late != nil || !errors.As(err, &typed) || typed.Reason != "collector_closed" {
				t.Fatal("closed collector acknowledged a writer window")
			}
		})
	}
}

func TestQualificationWindowDeadlineResumesWithoutCallerCleanup(t *testing.T) {
	c := qualificationCollector(t)
	resume, err := c.pauseWriterForQualification(context.Background(), time.Second)
	if err != nil {
		t.Fatal("bounded qualification window unavailable")
	}
	defer resume()
	if !c.Emit(JobAdmitted, qualificationAdmission()) {
		t.Fatal("actual queue admission unavailable")
	}
	await(t, func() bool { return c.qualificationOwner.Load() == nil })
	await(t, func() bool {
		page, err := c.Query(Filter{Code: JobAdmitted}, "", 50)
		return err == nil && len(page.Events) == 1
	})
	if resume() {
		t.Fatal("expired window was reported as controlled admission")
	}
	// Expiry frees ownership, not just the serial writer's wait.
	next, err := c.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("expired window poisoned a later owner")
	}
	next()
}

func TestQualificationWindowCancellationBeforeAcknowledgement(t *testing.T) {
	openGate := make(chan struct{})
	var once sync.Once
	releaseOpen := func() { once.Do(func() { close(openGate) }) }
	c := newCollectorWithQualification(Config{Directory: testDirectory(t), ManagedOutput: true}, func(path string) (*storage, []Event, bool, error) {
		<-openGate
		return openStorage(path)
	}, true)
	t.Cleanup(func() { releaseOpen(); flush(t, c, true) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		resume, err := c.PauseWriterForQualification(ctx)
		if resume != nil {
			resume()
		}
		done <- err
	}()
	await(t, func() bool { return len(c.qualificationRequests) == 1 })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("unacknowledged window did not preserve cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unacknowledged window did not join")
	}
	releaseOpen()
	await(t, func() bool {
		page, err := c.Query(Filter{Code: Boot}, "", 50)
		return err == nil && len(page.Events) == 1
	})
	await(t, func() bool { return len(c.qualificationRequests) == 0 })
	resume, err := c.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("cancelled request poisoned a later window")
	}
	resume()
}
