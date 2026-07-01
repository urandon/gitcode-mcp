package gitcode

import (
	"context"
	"math"
	"time"
)

type RateLimitEventType string

const (
	RateLimitEventThrottleWaitStarted     RateLimitEventType = "throttle_wait_started"
	RateLimitEventThrottleWaitCompleted   RateLimitEventType = "throttle_wait_completed"
	RateLimitEventResponseRateLimited     RateLimitEventType = "response_rate_limited"
	RateLimitEventRetryAfterWaitStarted   RateLimitEventType = "retry_after_wait_started"
	RateLimitEventRetryAfterWaitCompleted RateLimitEventType = "retry_after_wait_completed"
)

type RateLimitEvent struct {
	Type          RateLimitEventType
	Method        string
	Endpoint      string
	Attempt       int
	Wait          time.Duration
	ResumeAt      time.Time
	RawRetryAfter string
	RPS           float64
	Burst         int
}

type RateLimitObserver func(RateLimitEvent)

type rateLimitObserverContextKey struct{}

func WithRateLimitObserver(ctx context.Context, observer RateLimitObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, rateLimitObserverContextKey{}, observer)
}

func emitRateLimitEvent(ctx context.Context, ev RateLimitEvent) {
	observer, _ := ctx.Value(rateLimitObserverContextKey{}).(RateLimitObserver)
	if observer != nil {
		observer(ev)
	}
}

type clientRateLimiter struct {
	tokens float64
	last   time.Time
	rps    float64
	burst  int
}

func newClientRateLimiter(rps float64, burst int) *clientRateLimiter {
	if rps <= 0 || math.IsNaN(rps) || math.IsInf(rps, 0) {
		return nil
	}
	if burst <= 0 {
		burst = 1
	}
	return &clientRateLimiter{tokens: float64(burst), rps: rps, burst: burst}
}

func (l *clientRateLimiter) reserve(now time.Time) (time.Duration, time.Time) {
	if l == nil {
		return 0, now
	}
	if l.last.IsZero() {
		l.last = now
	}
	if now.After(l.last) {
		elapsed := now.Sub(l.last).Seconds()
		l.tokens = math.Min(float64(l.burst), l.tokens+elapsed*l.rps)
		l.last = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return 0, now
	}
	needed := (1 - l.tokens) / l.rps
	wait := time.Duration(math.Ceil(needed * float64(time.Second)))
	if wait < 0 {
		wait = 0
	}
	l.tokens--
	return wait, now.Add(wait)
}
