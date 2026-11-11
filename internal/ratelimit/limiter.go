package ratelimit

import (
	"math"
	"sync"
	"time"
)

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

type Limiter struct {
	mu          sync.Mutex
	rate        float64
	burst       float64
	now         func() time.Time
	buckets     map[string]*bucket
	lastCleanup time.Time
}

func New(rate float64, burst int) *Limiter {
	return NewWithClock(rate, burst, time.Now)
}

func NewWithClock(rate float64, burst int, now func() time.Time) *Limiter {
	if rate <= 0 || burst <= 0 {
		panic("rate and burst must be positive")
	}
	return &Limiter{
		rate:        rate,
		burst:       float64(burst),
		now:         now,
		buckets:     make(map[string]*bucket),
		lastCleanup: now(),
	}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.cleanup(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
	}
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *Limiter) RetryAfter() time.Duration {
	seconds := math.Ceil(1 / l.rate)
	if seconds < 1 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}

func (l *Limiter) cleanup(now time.Time) {
	if now.Sub(l.lastCleanup) < time.Minute {
		return
	}
	for key, value := range l.buckets {
		if now.Sub(value.lastSeen) > 10*time.Minute {
			delete(l.buckets, key)
		}
	}
	l.lastCleanup = now
}
