package cache

import (
	"testing"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/domain"
)

func TestCacheTTLExpiryAndCounters(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	cache := NewTTLWithClock(10, time.Minute, func() time.Time { return now })
	cache.Set(domain.Link{Code: "go1234", TargetURL: "https://example.com"})

	if link, ok := cache.Get("go1234"); !ok || link.TargetURL != "https://example.com" {
		t.Fatalf("cache hit = %+v, %v", link, ok)
	}
	now = now.Add(time.Minute)
	if _, ok := cache.Get("go1234"); ok {
		t.Fatal("expired link remained in cache")
	}
	hits, misses := cache.Counters()
	if hits != 1 || misses != 1 {
		t.Fatalf("counters = %d hits, %d misses", hits, misses)
	}
}

func TestCacheUsesEarlierLinkExpiration(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	cache := NewTTLWithClock(10, time.Minute, func() time.Time { return now })
	expiresAt := now.Add(10 * time.Second)
	cache.Set(domain.Link{Code: "go1234", ExpiresAt: &expiresAt})

	now = now.Add(11 * time.Second)
	if _, ok := cache.Get("go1234"); ok {
		t.Fatal("link outlived its own expiration")
	}
}

func TestCacheCapacityAndDelete(t *testing.T) {
	now := time.Now()
	cache := NewTTLWithClock(2, time.Minute, func() time.Time { return now })
	cache.Set(domain.Link{Code: "one1"})
	cache.Set(domain.Link{Code: "two2"})
	cache.Set(domain.Link{Code: "three3"})

	if cache.Len() != 2 {
		t.Fatalf("Len() = %d", cache.Len())
	}
	if _, ok := cache.Get("one1"); ok {
		t.Fatal("oldest entry was not evicted")
	}
	cache.Delete("two2")
	if _, ok := cache.Get("two2"); ok {
		t.Fatal("deleted entry remained in cache")
	}
}
