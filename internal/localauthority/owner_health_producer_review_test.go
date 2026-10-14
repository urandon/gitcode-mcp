//go:build darwin || linux

package localauthority

import (
	"context"
	"testing"
)

func TestReviewRuntimeHealthCannotCallUnqualifiedWriterReady(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	s := c.projects[testProject].store
	if _, err := s.db.ExecContext(context.Background(), "PRAGMA synchronous=OFF"); err != nil {
		t.Fatal("durability control unavailable")
	}
	if verifyWriter(context.Background(), s.db) == nil {
		t.Fatal("durability negative control not effective")
	}
	v := currentHealth(t, c)
	if v.Tracker.State == "ready" || v.Tracker.Writer == "owned" {
		t.Fatal("current health qualified a writer that fails actual durability admission")
	}
}
