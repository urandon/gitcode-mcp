//go:build darwin || linux

package observability

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestTransportReadsDoNotWaitForCollectorLock(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	flush(t, c, true)
	c.mu.Lock()
	before := c.coverage
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.mu.Lock()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 64; i++ {
			_, err := c.QueryContext(ctx, Filter{}, "", 50)
			var q QueryError
			if !errors.As(err, &q) || q.Code != "observation_busy" || q.Reason != "ledger_busy" {
				done <- errors.New("busy ledger did not return fixed error")
				return
			}
			if _, err := c.CoverageContext(ctx); !errors.As(err, &q) || q.Code != "observation_busy" {
				done <- errors.New("busy coverage read did not return fixed error")
				return
			}
		}
		done <- nil
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second):
		cancel()
		c.mu.Unlock()
		<-done
		t.Fatal("observation read waited for an execution-independent lock")
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, c.Coverage()) || c.admissionDrops.Load() != 0 {
		t.Fatal("busy read changed diagnostic evidence")
	}
}

func TestTransportReadsValidateAndCancelBeforeObservation(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	flush(t, c, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	var q QueryError
	if _, err := c.QueryContext(ctx, Filter{Component: "synthetic-invalid-selector"}, "", 50); !errors.As(err, &q) || q.Code != "invalid_query" {
		t.Fatal("invalid selector reached observation access")
	}
	if _, err := c.QueryContext(ctx, Filter{}, "", 50); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled query attempted a collector read")
	}
	if _, err := c.CoverageContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled coverage attempted a collector read")
	}
}

func TestTransportMetricsDoNotWaitSampleOrCountReadLoss(t *testing.T) {
	m := NewMetrics("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Unix(1700000000, 0))
	before := m.Snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.mu.Lock()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 64; i++ {
			_, err := m.SnapshotContext(ctx)
			var q QueryError
			if !errors.As(err, &q) || q.Code != "observation_busy" || q.Reason != "metrics_busy" {
				done <- errors.New("busy metrics did not return fixed error")
				return
			}
		}
		done <- nil
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second):
		cancel()
		m.mu.Unlock()
		<-done
		t.Fatal("metrics read waited for snapshot lock")
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := m.SnapshotContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled metrics read returned a snapshot")
	}
	after, err := m.SnapshotContext(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) || after.Dropped != 0 || !after.SampledAt.IsZero() {
		t.Fatal("transport read sampled, changed evidence or counted loss")
	}
	for _, series := range after.Series {
		if series.Value != nil || len(series.History) != 0 {
			t.Fatal("unobserved metrics invented a current value or history")
		}
	}
}

func TestTransportQueryReturnsIndependentSnapshot(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	flush(t, c, true)
	p, err := c.QueryContext(context.Background(), Filter{}, "", 200)
	if err != nil || len(p.Events) == 0 {
		t.Fatal("retained snapshot unavailable")
	}
	before := p.Events[0]
	p.Events[0].Message = "synthetic-consumer-mutation"
	again, err := c.QueryContext(context.Background(), Filter{}, "", 200)
	if err != nil || again.Events[0] != before {
		t.Fatal("returned query slice mutated retained evidence")
	}
}
