package gitcode

import (
	"testing"
	"time"
)

func TestClientRateLimiterQueuesConcurrentReservations(t *testing.T) {
	limiter := newClientRateLimiter(10, 1)
	now := time.Unix(100, 0)
	if wait, _ := limiter.reserve(now); wait != 0 {
		t.Fatalf("first reservation wait=%s, want none", wait)
	}
	if wait, _ := limiter.reserve(now); wait != 100*time.Millisecond {
		t.Fatalf("second reservation wait=%s, want 100ms", wait)
	}
	if wait, _ := limiter.reserve(now); wait != 200*time.Millisecond {
		t.Fatalf("third reservation wait=%s, want 200ms", wait)
	}
	if wait, _ := limiter.reserve(now.Add(100 * time.Millisecond)); wait != 200*time.Millisecond {
		t.Fatalf("reservation after one interval wait=%s, want 200ms", wait)
	}
}
