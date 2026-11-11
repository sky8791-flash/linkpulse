package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterBurstRefillAndIsolation(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	limiter := NewWithClock(2, 2, func() time.Time { return now })

	if !limiter.Allow("one") || !limiter.Allow("one") {
		t.Fatal("burst requests were rejected")
	}
	if limiter.Allow("one") {
		t.Fatal("request above burst was accepted")
	}
	if !limiter.Allow("two") {
		t.Fatal("limiter was not isolated by key")
	}

	now = now.Add(500 * time.Millisecond)
	if !limiter.Allow("one") {
		t.Fatal("token was not refilled")
	}
	if limiter.RetryAfter() != time.Second {
		t.Fatalf("RetryAfter() = %s", limiter.RetryAfter())
	}
}
