package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	HTTPAddr string

	DatabaseURL       string
	APIKey            string
	BaseURL           string
	VisitorHashSecret string
	StatsTimezone     string
	StatsLocation     *time.Location

	CacheSize int
	CacheTTL  time.Duration

	ClickQueueSize      int
	ClickBatchSize      int
	ClickFlushInterval  time.Duration
	ClickWriteRetries   int
	ClickRetryBaseDelay time.Duration

	PublicRateLimit float64
	PublicRateBurst int
	AdminRateLimit  float64
	AdminRateBurst  int

	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:            env("HTTP_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		APIKey:              os.Getenv("API_KEY"),
		BaseURL:             os.Getenv("BASE_URL"),
		VisitorHashSecret:   os.Getenv("VISITOR_HASH_SECRET"),
		StatsTimezone:       env("STATS_TIMEZONE", "Asia/Shanghai"),
		CacheTTL:            1 * time.Minute,
		ClickFlushInterval:  1 * time.Second,
		ClickRetryBaseDelay: 100 * time.Millisecond,
		ShutdownTimeout:     10 * time.Second,
	}

	var errs []error
	cfg.CacheSize, errs = positiveInt(errs, "CACHE_SIZE", 10000)
	cfg.ClickQueueSize, errs = positiveInt(errs, "CLICK_QUEUE_SIZE", 4096)
	cfg.ClickBatchSize, errs = positiveInt(errs, "CLICK_BATCH_SIZE", 100)
	cfg.ClickWriteRetries, errs = nonNegativeInt(errs, "CLICK_WRITE_RETRIES", 3)
	cfg.PublicRateBurst, errs = positiveInt(errs, "PUBLIC_RATE_BURST", 200)
	cfg.AdminRateBurst, errs = positiveInt(errs, "ADMIN_RATE_BURST", 40)

	cfg.CacheTTL, errs = duration(errs, "CACHE_TTL", cfg.CacheTTL)
	cfg.ClickFlushInterval, errs = duration(errs, "CLICK_FLUSH_INTERVAL", cfg.ClickFlushInterval)
	cfg.ShutdownTimeout, errs = duration(errs, "SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout)

	cfg.PublicRateLimit, errs = positiveFloat(errs, "PUBLIC_RATE_LIMIT", 100)
	cfg.AdminRateLimit, errs = positiveFloat(errs, "ADMIN_RATE_LIMIT", 20)

	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if cfg.APIKey == "" {
		errs = append(errs, errors.New("API_KEY is required"))
	}
	if cfg.BaseURL == "" {
		errs = append(errs, errors.New("BASE_URL is required"))
	}
	if cfg.VisitorHashSecret == "" {
		errs = append(errs, errors.New("VISITOR_HASH_SECRET is required"))
	}

	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.BaseURL != "" {
		u, err := url.Parse(cfg.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, errors.New("BASE_URL must be an absolute http or https URL"))
		}
	}

	if len(cfg.APIKey) < 16 {
		errs = append(errs, errors.New("API_KEY must contain at least 16 characters"))
	}
	if len(cfg.VisitorHashSecret) < 16 {
		errs = append(errs, errors.New("VISITOR_HASH_SECRET must contain at least 16 characters"))
	}

	location, err := time.LoadLocation(cfg.StatsTimezone)
	if err != nil {
		errs = append(errs, fmt.Errorf("STATS_TIMEZONE: %w", err))
	} else {
		cfg.StatsLocation = location
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func positiveInt(errs []error, key string, fallback int) (int, []error) {
	return intValue(errs, key, fallback, false)
}

func nonNegativeInt(errs []error, key string, fallback int) (int, []error) {
	return intValue(errs, key, fallback, true)
}

func intValue(errs []error, key string, fallback int, allowZero bool) (int, []error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, errs
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || (!allowZero && value == 0) {
		return fallback, append(errs, fmt.Errorf("%s must be a %s integer", key, map[bool]string{true: "non-negative", false: "positive"}[allowZero]))
	}
	return value, errs
}

func positiveFloat(errs []error, key string, fallback float64) (float64, []error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, errs
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return fallback, append(errs, fmt.Errorf("%s must be a positive number", key))
	}
	return value, errs
}

func duration(errs []error, key string, fallback time.Duration) (time.Duration, []error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, errs
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fallback, append(errs, fmt.Errorf("%s must be a positive duration", key))
	}
	return value, errs
}
