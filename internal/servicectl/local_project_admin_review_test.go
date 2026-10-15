package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestReviewNormalAdminDoesNotAdvertiseLocalUI(t *testing.T) {
	m := newTestManager(t, "darwin")
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(m, jobs, filepath.Join(t.TempDir(), "registry.json"))
	snapshot, err := m.adminObservation(context.Background(), jobs, maintenance, time.Now())
	if err != nil {
		t.Fatal("snapshot unavailable")
	}
	found := false
	for _, cap := range snapshot.Capabilities {
		if cap.Category == "local_project" {
			found = true
			if cap.UIEnabled || cap.UIReason == "" {
				t.Fatal("normal host advertises unavailable local UI")
			}
		}
	}
	if !found {
		t.Fatal("unavailable local cohort disappeared from the catalog")
	}
}

func TestReviewIndexReceiptCancellationDoesNotWaitForOtherRequest(t *testing.T) {
	m, _ := localFixtureManager(t)
	host, err := service.NewLocalProjects(context.Background(), m.EffectiveConfig.LocalProjects, func(ref string) (string, error) { return config.LocalProjectStorePath(m.Source, ref) })
	if err != nil {
		t.Fatal("host unavailable")
	}
	receipts := NewAdminControlReceiptManager("")
	receipts.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(service.LocalProjectIndexPlanRequest{LocalProjectIndexRequest: service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 1}, IdempotencyKey: "review-key", PlanID: "unpublished"})
	done := make(chan error, 1)
	go func() { _, err := localProjectAdminProvider(host, receipts)(ctx, "index-apply", raw); done <- err }()
	select {
	case err := <-done:
		receipts.mu.Unlock()
		if err == nil {
			t.Fatal("canceled request acknowledged")
		}
	case <-time.After(150 * time.Millisecond):
		receipts.mu.Unlock()
		<-done
		t.Fatal("canceled Admin request waits for unrelated receipt owner")
	}
}

type checkedReceiptContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

func (c *checkedReceiptContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestAdminControlReceiptCancellationAfterAdmissionStartsDoesNotPublish(t *testing.T) {
	receipts := NewAdminControlReceiptManager("")
	receipts.mu.Lock()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &checkedReceiptContext{Context: base, checked: make(chan struct{})}
	done := make(chan error, 1)
	called := false
	go func() {
		_, err := receipts.Apply(ctx, "bounded-index", "lab", "canceled-key", struct{}{}, func() (map[string]any, error) { called = true; return map[string]any{}, nil })
		done <- err
	}()
	// The initial Err result was captured while the context was still active.
	// Cancellation now must unblock admission without releasing the other owner.
	<-ctx.checked
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || called || len(receipts.receipts) != 0 {
			t.Error("canceled receipt admission mutated or lost its context")
		}
	case <-time.After(time.Second):
		receipts.mu.Unlock()
		<-done
		t.Fatal("pending receipt admission did not observe cancellation")
	}
	receipts.mu.Unlock()
	result, err := receipts.Apply(context.Background(), "bounded-index", "lab", "canceled-key", struct{}{}, func() (map[string]any, error) { return map[string]any{"completed": true}, nil })
	if err != nil || result["replayed"] != false || result["completed"] != true {
		t.Fatal("canceled admission consumed the legitimate retry key")
	}
}
