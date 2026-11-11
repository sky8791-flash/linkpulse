package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/cache"
	"github.com/sky8791-flash/linkpulse/internal/domain"
	"github.com/sky8791-flash/linkpulse/internal/link"
	"github.com/sky8791-flash/linkpulse/internal/ratelimit"
)

const testAPIKey = "test-api-key-123456"

type memoryRepository struct {
	mu     sync.Mutex
	links  map[string]domain.Link
	nextID int64
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{links: make(map[string]domain.Link), nextID: 1}
}

func (r *memoryRepository) Create(_ context.Context, item domain.Link) (domain.Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.links[item.Code]; ok {
		return domain.Link{}, domain.ErrConflict
	}
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	item.ID = r.nextID
	item.CreatedAt = now
	item.UpdatedAt = now
	r.nextID++
	r.links[item.Code] = item
	return item, nil
}

func (r *memoryRepository) GetByCode(_ context.Context, code string) (domain.Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.links[code]
	if !ok {
		return domain.Link{}, domain.ErrNotFound
	}
	return item, nil
}

func (r *memoryRepository) Update(_ context.Context, code string, patch domain.LinkPatch) (domain.Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.links[code]
	if !ok {
		return domain.Link{}, domain.ErrNotFound
	}
	if patch.TargetURL != nil {
		item.TargetURL = *patch.TargetURL
	}
	if patch.ExpiresAt.Set {
		item.ExpiresAt = patch.ExpiresAt.Value
	}
	if patch.IsActive != nil {
		item.IsActive = *patch.IsActive
	}
	r.links[code] = item
	return item, nil
}

func (r *memoryRepository) Delete(_ context.Context, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.links, code)
	return nil
}

func (r *memoryRepository) List(_ context.Context, cursor int64, limit int) (domain.LinkPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]domain.Link, 0, len(r.links))
	for _, item := range r.links {
		if cursor == 0 || item.ID < cursor {
			items = append(items, item)
		}
	}
	if len(items) > limit {
		next := items[limit-1].ID
		return domain.LinkPage{Items: items[:limit], NextCursor: &next}, nil
	}
	return domain.LinkPage{Items: items}, nil
}

type memoryStats struct{}

func (memoryStats) ClickStats(_ context.Context, _ int64, from, to time.Time, _ *time.Location) (domain.ClickStats, error) {
	return domain.ClickStats{
		From:           from.Format(time.DateOnly),
		To:             to.Add(-time.Nanosecond).Format(time.DateOnly),
		TotalClicks:    3,
		UniqueVisitors: 2,
		Referrers:      []domain.Breakdown{{Name: "example.com", Clicks: 3}},
		Devices:        []domain.Breakdown{{Name: "desktop", Clicks: 3}},
		Daily:          []domain.DailyStat{{Date: from.Format(time.DateOnly), Clicks: 3, UniqueVisitors: 2}},
	}, nil
}

type memoryRecorder struct {
	mu     sync.Mutex
	events []domain.ClickEvent
	drop   bool
}

func (r *memoryRecorder) Record(event domain.ClickEvent) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return !r.drop
}

func (r *memoryRecorder) Dropped() uint64 {
	if r.drop {
		return 1
	}
	return 0
}

func (r *memoryRecorder) snapshot() []domain.ClickEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.ClickEvent(nil), r.events...)
}

type testServer struct {
	handler  http.Handler
	repo     *memoryRepository
	recorder *memoryRecorder
	now      time.Time
}

func newTestServer(t *testing.T, publicLimiter, adminLimiter *ratelimit.Limiter) *testServer {
	t.Helper()
	repo := newMemoryRepository()
	recorder := &memoryRecorder{}
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	links := link.NewService(repo, memoryStats{}, cache.NewTTL(100, time.Minute), time.UTC,
		link.WithClock(func() time.Time { return now }),
		link.WithCodeGenerator(func() (string, error) { return "go12345", nil }),
	)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(Dependencies{
		Links:             links,
		Recorder:          recorder,
		Ready:             func(context.Context) error { return nil },
		APIKey:            testAPIKey,
		BaseURL:           "http://short.test",
		VisitorHashSecret: "visitor-secret-123456",
		StatsLocation:     time.UTC,
		PublicLimiter:     publicLimiter,
		AdminLimiter:      adminLimiter,
		Logger:            logger,
		Now:               func() time.Time { return now },
	})
	return &testServer{handler: handler, repo: repo, recorder: recorder, now: now}
}

func adminRequest(method, path string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+testAPIKey)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestAdminAuthentication(t *testing.T) {
	tss := newTestServer(t, nil, nil)
	response := httptest.NewRecorder()
	tss.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/links", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate header")
	}
	var payload errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "unauthorized" {
		t.Fatalf("error code = %q", payload.Error.Code)
	}
}

func TestCreateListRedirectAndStats(t *testing.T) {
	tss := newTestServer(t, nil, nil)
	createBody := `{"target_url":"https://example.com/article","custom_alias":"go-notes"}`
	response := httptest.NewRecorder()
	tss.handler.ServeHTTP(response, adminRequest(http.MethodPost, "/api/v1/links", strings.NewReader(createBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}

	var created linkResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created link: %v", err)
	}
	if created.Code != "go-notes" || created.ShortURL != "http://short.test/go-notes" {
		t.Fatalf("created link = %+v", created)
	}

	redirectRequest := httptest.NewRequest(http.MethodGet, "/go-notes", nil)
	redirectRequest.RemoteAddr = "192.0.2.44:4321"
	redirectRequest.Header.Set("Referer", "https://news.example.com/story")
	redirectRequest.Header.Set("User-Agent", "Mozilla/5.0 Chrome/131.0")
	redirectResponse := httptest.NewRecorder()
	tss.handler.ServeHTTP(redirectResponse, redirectRequest)
	if redirectResponse.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, body = %s", redirectResponse.Code, redirectResponse.Body.String())
	}
	if location := redirectResponse.Header().Get("Location"); location != "https://example.com/article" {
		t.Fatalf("Location = %q", location)
	}
	if redirectResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("redirect is cacheable")
	}
	events := tss.recorder.snapshot()
	if len(events) != 1 || events[0].VisitorHash == "" || strings.Contains(events[0].VisitorHash, "192.0.2.44") {
		t.Fatalf("recorded events = %+v", events)
	}

	listResponse := httptest.NewRecorder()
	tss.handler.ServeHTTP(listResponse, adminRequest(http.MethodGet, "/api/v1/links?limit=10", nil))
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "go-notes") {
		t.Fatalf("list status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}

	statsResponse := httptest.NewRecorder()
	tss.handler.ServeHTTP(statsResponse, adminRequest(http.MethodGet, "/api/v1/links/go-notes/stats", nil))
	if statsResponse.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body = %s", statsResponse.Code, statsResponse.Body.String())
	}
	var stats domain.ClickStats
	if err := json.Unmarshal(statsResponse.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Code != "go-notes" || stats.TotalClicks != 3 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestExpiredAndInactiveLinksReturnGone(t *testing.T) {
	tss := newTestServer(t, nil, nil)
	expiredAt := tss.now.Add(-time.Minute)
	tss.repo.links["old123"] = domain.Link{
		ID: 1, Code: "old123", TargetURL: "https://example.com", ExpiresAt: &expiredAt, IsActive: true,
	}
	tss.repo.links["off123"] = domain.Link{
		ID: 2, Code: "off123", TargetURL: "https://example.com", IsActive: false,
	}

	for _, code := range []string{"old123", "off123"} {
		response := httptest.NewRecorder()
		tss.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/"+code, nil))
		if response.Code != http.StatusGone {
			t.Errorf("%s status = %d", code, response.Code)
		}
	}
}

func TestCreateValidationAndConflict(t *testing.T) {
	tss := newTestServer(t, nil, nil)
	invalidJSON := `{"target_url":"https://example.com","unknown":true}`
	response := httptest.NewRecorder()
	tss.handler.ServeHTTP(response, adminRequest(http.MethodPost, "/api/v1/links", strings.NewReader(invalidJSON)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_json") {
		t.Fatalf("invalid JSON response = %d %s", response.Code, response.Body.String())
	}

	badURL := `{"target_url":"javascript:alert(1)"}`
	response = httptest.NewRecorder()
	tss.handler.ServeHTTP(response, adminRequest(http.MethodPost, "/api/v1/links", strings.NewReader(badURL)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_target_url") {
		t.Fatalf("invalid URL response = %d %s", response.Code, response.Body.String())
	}

	tss.repo.links["taken1"] = domain.Link{ID: 1, Code: "taken1", TargetURL: "https://example.com", IsActive: true}
	conflict := `{"target_url":"https://example.com","custom_alias":"taken1"}`
	response = httptest.NewRecorder()
	tss.handler.ServeHTTP(response, adminRequest(http.MethodPost, "/api/v1/links", strings.NewReader(conflict)))
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestPatchAndDeleteLink(t *testing.T) {
	tss := newTestServer(t, nil, nil)
	tss.repo.links["go1234"] = domain.Link{ID: 1, Code: "go1234", TargetURL: "https://example.com", IsActive: true}
	patchBody := `{"is_active":false,"expires_at":null}`
	response := httptest.NewRecorder()
	tss.handler.ServeHTTP(response, adminRequest(http.MethodPatch, "/api/v1/links/go1234", bytes.NewBufferString(patchBody)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"is_active":false`) {
		t.Fatalf("patch status = %d, body = %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	tss.handler.ServeHTTP(response, adminRequest(http.MethodGet, "/go1234", nil))
	if response.Code != http.StatusGone {
		t.Fatalf("inactive redirect status = %d", response.Code)
	}

	response = httptest.NewRecorder()
	tss.handler.ServeHTTP(response, adminRequest(http.MethodDelete, "/api/v1/links/go1234", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", response.Code)
	}
}

func TestPublicRateLimitAndRequestID(t *testing.T) {
	clock := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	limiter := ratelimit.NewWithClock(0.01, 1, func() time.Time { return clock })
	tss := newTestServer(t, limiter, nil)
	tss.repo.links["go1234"] = domain.Link{ID: 1, Code: "go1234", TargetURL: "https://example.com", IsActive: true}

	first := httptest.NewRecorder()
	tss.handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/go1234", nil))
	if first.Code != http.StatusFound {
		t.Fatalf("first status = %d", first.Code)
	}
	if first.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID header")
	}
	secondRequest := httptest.NewRequest(http.MethodGet, "/go1234", nil)
	second := httptest.NewRecorder()
	tss.handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
}

func TestDashboardAndOpenAPI(t *testing.T) {
	tss := newTestServer(t, nil, nil)
	for _, path := range []string{"/", "/openapi.yaml", "/assets/styles.css", "/assets/app.js"} {
		response := httptest.NewRecorder()
		tss.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, response.Code)
		}
	}
}

func TestReadinessFailure(t *testing.T) {
	tss := newTestServer(t, nil, nil)
	links := link.NewService(newMemoryRepository(), memoryStats{}, cache.NewTTL(10, time.Minute), time.UTC)
	handler := NewHandler(Dependencies{
		Links:             links,
		Recorder:          &memoryRecorder{},
		Ready:             func(context.Context) error { return errors.New("database unavailable") },
		APIKey:            testAPIKey,
		BaseURL:           "http://short.test",
		VisitorHashSecret: "visitor-secret-123456",
		StatsLocation:     time.UTC,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d", response.Code)
	}
	_ = tss
}
