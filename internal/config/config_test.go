package config

import (
	"strings"
	"testing"
	"time"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://linkpulse:secret@localhost:5432/linkpulse")
	t.Setenv("API_KEY", "test-api-key-123456")
	t.Setenv("BASE_URL", "http://localhost:8080/")
	t.Setenv("VISITOR_HASH_SECRET", "visitor-secret-123456")
}

func TestLoadDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("CACHE_SIZE", "")
	t.Setenv("CACHE_TTL", "")
	t.Setenv("CLICK_QUEUE_SIZE", "")
	t.Setenv("CLICK_BATCH_SIZE", "")
	t.Setenv("CLICK_FLUSH_INTERVAL", "")
	t.Setenv("CLICK_WRITE_RETRIES", "")
	t.Setenv("PUBLIC_RATE_LIMIT", "")
	t.Setenv("PUBLIC_RATE_BURST", "")
	t.Setenv("ADMIN_RATE_LIMIT", "")
	t.Setenv("ADMIN_RATE_BURST", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("STATS_TIMEZONE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.CacheSize != 10000 || cfg.CacheTTL != time.Minute {
		t.Fatalf("cache defaults = %d, %s", cfg.CacheSize, cfg.CacheTTL)
	}
	if cfg.ClickQueueSize != 4096 || cfg.ClickBatchSize != 100 {
		t.Fatalf("collector defaults = %d, %d", cfg.ClickQueueSize, cfg.ClickBatchSize)
	}
	if cfg.StatsLocation == nil || cfg.StatsLocation.String() != "Asia/Shanghai" {
		t.Fatalf("StatsLocation = %v", cfg.StatsLocation)
	}
}

func TestLoadRejectsMissingAndInvalidValues(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("API_KEY", "short")
	t.Setenv("BASE_URL", "ftp://localhost")
	t.Setenv("CACHE_SIZE", "0")
	t.Setenv("PUBLIC_RATE_LIMIT", "-1")
	t.Setenv("CLICK_FLUSH_INTERVAL", "soon")
	t.Setenv("STATS_TIMEZONE", "Mars/Olympus")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() returned nil error")
	}
	for _, fragment := range []string{
		"DATABASE_URL is required",
		"API_KEY must contain",
		"BASE_URL must",
		"CACHE_SIZE must",
		"PUBLIC_RATE_LIMIT must",
		"CLICK_FLUSH_INTERVAL must",
		"STATS_TIMEZONE",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not include %q", err, fragment)
		}
	}
}
