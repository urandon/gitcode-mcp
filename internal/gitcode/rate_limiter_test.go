package gitcode

import (
	"testing"
	"time"
)

func TestEffectiveRateLimitRetryAfterUsesExponentialFallbackForZero(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if got := effectiveRateLimitRetryAfter("0", now, 1); got != time.Second {
		t.Fatalf("attempt 1 wait = %s, want 1s", got)
	}
	if got := effectiveRateLimitRetryAfter("0", now, 3); got != 4*time.Second {
		t.Fatalf("attempt 3 wait = %s, want 4s", got)
	}
	if got := effectiveRateLimitRetryAfter("7", now, 1); got != 7*time.Second {
		t.Fatalf("explicit retry-after wait = %s, want 7s", got)
	}
}

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
