package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/analytics"
	"github.com/sky8791-flash/linkpulse/internal/cache"
	"github.com/sky8791-flash/linkpulse/internal/domain"
	"github.com/sky8791-flash/linkpulse/internal/httpapi"
	"github.com/sky8791-flash/linkpulse/internal/link"
	"github.com/sky8791-flash/linkpulse/internal/store/postgres"
	"github.com/sky8791-flash/linkpulse/internal/testutil"
)

func TestEndToEndCreateRedirectAndPersistAnalytics(t *testing.T) {
	pool := testutil.NewPostgresSchema(t)
	store := postgres.NewWithPool(pool)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := analytics.NewCollector(store, analytics.CollectorConfig{
		QueueSize:     10,
		BatchSize:     2,
		FlushInterval: time.Hour,
		WriteRetries:  1,
	}, logger)
	collector.Start()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = collector.Shutdown(shutdownCtx)
	})

	links := link.NewService(store, store, cache.NewTTL(100, time.Minute), time.UTC)
	handler := httpapi.NewHandler(httpapi.Dependencies{
		Links:             links,
		Recorder:          collector,
		Ready:             store.Ready,
		APIKey:            "integration-api-key",
		BaseURL:           "http://short.test",
		VisitorHashSecret: "integration-visitor-secret",
		StatsLocation:     time.UTC,
		Logger:            logger,
	})

	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/links",
		bytes.NewBufferString(`{"target_url":"https://example.com/e2e","custom_alias":"e2ego"}`))
	createRequest.Header.Set("Authorization", "Bearer integration-api-key")
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}

	redirectRequest := httptest.NewRequest(http.MethodGet, "/e2ego", nil)
	redirectRequest.RemoteAddr = "192.0.2.44:4321"
	redirectRequest.Header.Set("User-Agent", "Mozilla/5.0 Chrome/131.0")
	redirectRequest.Header.Set("Referer", "https://news.example.com/story")
	redirectResponse := httptest.NewRecorder()
	handler.ServeHTTP(redirectResponse, redirectRequest)
	if redirectResponse.Code != http.StatusFound {
		t.Fatalf("redirect status = %d", redirectResponse.Code)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := collector.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("collector Shutdown() error = %v", err)
	}

	statsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/links/e2ego/stats", nil)
	statsRequest.Header.Set("Authorization", "Bearer integration-api-key")
	statsResponse := httptest.NewRecorder()
	handler.ServeHTTP(statsResponse, statsRequest)
	if statsResponse.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body = %s", statsResponse.Code, statsResponse.Body.String())
	}
	var stats domain.ClickStats
	if err := json.Unmarshal(statsResponse.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.TotalClicks != 1 || stats.UniqueVisitors != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	var visitorHash string
	if err := pool.QueryRow(ctx, "SELECT visitor_hash FROM click_events LIMIT 1").Scan(&visitorHash); err != nil {
		t.Fatalf("read persisted click: %v", err)
	}
	if len(visitorHash) != 64 || strings.Contains(visitorHash, "192.0.2.44") {
		t.Fatalf("persisted visitor hash = %q", visitorHash)
	}
}
