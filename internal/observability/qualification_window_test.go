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

func TestQualificationStartupAcknowledgementFollowsFreshBootPublication(t *testing.T) {
	for _, retained := range []bool{false, true} {
		name := "fresh"
		if retained {
			name = "same-ledger-restart"
		}
		t.Run(name, func(t *testing.T) {
			dir := testDirectory(t)
			var previousBoot string
			if retained {
				previous := New(Config{Directory: dir, ManagedOutput: true})
				flush(t, previous, true)
				previousBoot = previous.BootID()
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce, appendOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c := newCollectorWithQualification(Config{Directory: dir, ManagedOutput: true}, func(path string) (*storage, []Event, bool, error) {
				s, events, unclean, err := openStorage(path)
				if err == nil {
					s.appendFault = func() error {
						appendOnce.Do(func() {
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
							}
						})
						return ctx.Err()
					}
				}
				return s, events, unclean, err
			}, true)
			defer func() { unblock(); flush(t, c, true) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("fresh Boot append did not reach its private storage barrier")
			}
			if c.Coverage().State != "ready" {
				t.Fatal("startup control did not expose loaded ready coverage")
			}
			c.mu.Lock()
			freshBoots, oldBoots := 0, 0
			for _, e := range c.events {
				if e.Code == Boot && e.BootID == c.BootID() {
					freshBoots++
				}
				if e.Code == Boot && e.BootID == previousBoot {
					oldBoots++
				}
			}
			c.mu.Unlock()
			if freshBoots != 0 || retained && oldBoots != 1 {
				t.Fatal("startup control did not preserve the unpublished fresh Boot boundary")
			}
			type result struct {
				resume func() bool
				err    error
			}
			done := make(chan result, 1)
			go func() {
				resume, err := c.PauseWriterForQualification(ctx)
				done <- result{resume, err}
			}()
			await(t, func() bool { return len(c.qualificationRequests) == 1 || ctx.Err() != nil })
			if ctx.Err() != nil {
				t.Fatal("startup acknowledgement did not queue within its bound")
			}
			select {
			case r := <-done:
				if r.resume != nil {
					r.resume()
				}
				t.Fatal("ready coverage incorrectly acknowledged an unfinished Boot append")
			default:
			}
			unblock()
			select {
			case r := <-done:
				if r.err != nil || r.resume == nil {
					t.Fatal("completed Boot append did not acknowledge the writer boundary")
				}
				defer r.resume()
				page, err := c.Query(Filter{Code: Boot}, "", 50)
				freshBoots = 0
				for _, e := range page.Events {
					if e.BootID == c.BootID() {
						freshBoots++
					}
				}
				if err != nil || page.HasMore || freshBoots != 1 || !r.resume() {
					t.Fatal("startup acknowledgement lacked the real fresh Boot publication")
				}
			case <-ctx.Done():
				t.Fatal("startup acknowledgement did not join")
			}
		})
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

func TestQualificationWindowAutomaticReleaseWinsCallerOverlap(t *testing.T) {
	for _, ending := range []string{"deadline", "close"} {
		t.Run(ending, func(t *testing.T) {
			c := qualificationCollector(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			resume, err := c.PauseWriterForQualification(ctx)
			if err != nil {
				t.Fatal("qualification window unavailable")
			}
			defer resume()
			request := c.qualificationOwner.Load()
			if request == nil || request.state.Load() != qualificationActive {
				t.Fatal("writer did not acknowledge the active state")
			}
			if !c.Emit(JobAdmitted, qualificationAdmission()) {
				t.Fatal("actual one-shot queue admission unavailable")
			}
			checked, continueRelease := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(continueRelease) }) }
			defer unblock()
			result := make(chan bool, 1)
			go func() {
				result <- request.release(func() {}, c.stop, func() {
					close(checked)
					<-continueRelease
				})
			}()
			select {
			case <-checked:
			case <-time.After(3 * time.Second):
				t.Fatal("release precondition did not join")
			}
			if ending == "close" {
				flush(t, c, true)
			} else {
				<-ctx.Done()
				await(t, func() bool { return c.qualificationOwner.Load() == nil })
			}
			if request.state.Load() != qualificationAutomatic {
				t.Fatal("automatic writer release did not win the terminal transition")
			}
			unblock()
			select {
			case controlled := <-result:
				if controlled || resume() {
					t.Fatal("late overlapping caller falsely reported controlled release")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("overlapping release did not join")
			}
			flush(t, c, true)
			page, err := c.Query(Filter{Code: JobAdmitted}, "", 50)
			if err != nil || len(page.Events) != 1 || request.state.Load() != qualificationAutomatic {
				t.Fatal("automatic winner lost actual entry or changed terminal cause")
			}
		})
	}
}

func TestQualificationWindowControlledReleaseWinsLaterStop(t *testing.T) {
	c := qualificationCollector(t)
	resume, err := c.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("qualification window unavailable")
	}
	request := c.qualificationOwner.Load()
	if request == nil || !resume() {
		t.Fatal("controlled release did not win")
	}
	flush(t, c, true)
	if request.state.Load() != qualificationReleased || resume() {
		t.Fatal("later writer stop changed the release winner")
	}
}
