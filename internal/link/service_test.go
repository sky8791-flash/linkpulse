package link

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/domain"
)

type fakeRepository struct {
	links        map[string]domain.Link
	nextID       int64
	getCalls     int
	createErrors []error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{links: make(map[string]domain.Link), nextID: 1}
}

func (r *fakeRepository) Create(_ context.Context, link domain.Link) (domain.Link, error) {
	if len(r.createErrors) > 0 {
		err := r.createErrors[0]
		r.createErrors = r.createErrors[1:]
		if err != nil {
			return domain.Link{}, err
		}
	}
	if _, exists := r.links[link.Code]; exists {
		return domain.Link{}, domain.ErrConflict
	}
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	link.ID = r.nextID
	link.CreatedAt = now
	link.UpdatedAt = now
	r.nextID++
	r.links[link.Code] = link
	return link, nil
}

func (r *fakeRepository) GetByCode(_ context.Context, code string) (domain.Link, error) {
	r.getCalls++
	link, ok := r.links[code]
	if !ok {
		return domain.Link{}, domain.ErrNotFound
	}
	return link, nil
}

func (r *fakeRepository) Update(_ context.Context, code string, patch domain.LinkPatch) (domain.Link, error) {
	link, ok := r.links[code]
	if !ok {
		return domain.Link{}, domain.ErrNotFound
	}
	if patch.TargetURL != nil {
		link.TargetURL = *patch.TargetURL
	}
	if patch.ExpiresAt.Set {
		link.ExpiresAt = patch.ExpiresAt.Value
	}
	if patch.IsActive != nil {
		link.IsActive = *patch.IsActive
	}
	r.links[code] = link
	return link, nil
}

func (r *fakeRepository) Delete(_ context.Context, code string) error {
	delete(r.links, code)
	return nil
}

func (r *fakeRepository) List(_ context.Context, cursor int64, limit int) (domain.LinkPage, error) {
	items := make([]domain.Link, 0, len(r.links))
	for _, link := range r.links {
		if cursor == 0 || link.ID < cursor {
			items = append(items, link)
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return domain.LinkPage{Items: items}, nil
}

type fakeStats struct {
	stats domain.ClickStats
	err   error
}

func (s fakeStats) ClickStats(_ context.Context, _ int64, _, _ time.Time, _ *time.Location) (domain.ClickStats, error) {
	return s.stats, s.err
}

type fakeCache struct {
	links   map[string]domain.Link
	deletes []string
}

func newFakeCache() *fakeCache {
	return &fakeCache{links: make(map[string]domain.Link)}
}

func (c *fakeCache) Get(code string) (domain.Link, bool) {
	link, ok := c.links[code]
	return link, ok
}

func (c *fakeCache) Set(link domain.Link) {
	c.links[link.Code] = link
}

func (c *fakeCache) Delete(code string) {
	delete(c.links, code)
	c.deletes = append(c.deletes, code)
}

func TestCreateValidatesInputAndAllocatesCode(t *testing.T) {
	repo := newFakeRepository()
	cache := newFakeCache()
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	service := NewService(repo, fakeStats{}, cache, time.UTC,
		WithClock(func() time.Time { return now }),
		WithCodeGenerator(func() (string, error) { return "go12345", nil }),
	)

	created, err := service.Create(context.Background(), CreateInput{TargetURL: "https://example.com/path"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Code != "go12345" || created.TargetURL != "https://example.com/path" || !created.IsActive {
		t.Fatalf("created link = %+v", created)
	}

	_, err = service.Create(context.Background(), CreateInput{TargetURL: "javascript:alert(1)"})
	if !errors.Is(err, domain.ErrInvalidTargetURL) {
		t.Fatalf("invalid URL error = %v", err)
	}
	_, err = service.Create(context.Background(), CreateInput{
		TargetURL:   "https://example.com",
		CustomAlias: "api",
	})
	if !errors.Is(err, domain.ErrInvalidAlias) {
		t.Fatalf("reserved alias error = %v", err)
	}
}

func TestCreateRetriesGeneratedCodeCollision(t *testing.T) {
	repo := newFakeRepository()
	repo.createErrors = []error{domain.ErrConflict, nil}
	codes := []string{"same12", "other7"}
	index := 0
	service := NewService(repo, fakeStats{}, newFakeCache(), time.UTC,
		WithCodeGenerator(func() (string, error) {
			code := codes[index]
			index++
			return code, nil
		}),
	)

	created, err := service.Create(context.Background(), CreateInput{TargetURL: "https://example.com"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Code != "other7" || index != 2 {
		t.Fatalf("created code = %q after %d attempts", created.Code, index)
	}
}

func TestResolveUsesCacheAndEnforcesAvailability(t *testing.T) {
	repo := newFakeRepository()
	cache := newFakeCache()
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	repo.links["go1234"] = domain.Link{
		ID: 1, Code: "go1234", TargetURL: "https://example.com", IsActive: true,
	}
	cache.Set(repo.links["go1234"])
	service := NewService(repo, fakeStats{}, cache, time.UTC, WithClock(func() time.Time { return now }))

	if _, err := service.Resolve(context.Background(), "go1234"); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if repo.getCalls != 0 {
		t.Fatalf("cache hit queried database %d times", repo.getCalls)
	}

	inactive := repo.links["go1234"]
	inactive.IsActive = false
	cache.Set(inactive)
	if _, err := service.Resolve(context.Background(), "go1234"); !errors.Is(err, domain.ErrInactive) {
		t.Fatalf("inactive error = %v", err)
	}

	expiredAt := now.Add(-time.Second)
	expired := repo.links["go1234"]
	expired.ExpiresAt = &expiredAt
	cache.Set(expired)
	if _, err := service.Resolve(context.Background(), "go1234"); !errors.Is(err, domain.ErrExpired) {
		t.Fatalf("expired error = %v", err)
	}
}

func TestUpdateAndDeleteInvalidateCache(t *testing.T) {
	repo := newFakeRepository()
	cache := newFakeCache()
	repo.links["go1234"] = domain.Link{ID: 1, Code: "go1234", TargetURL: "https://example.com", IsActive: true}
	cache.Set(repo.links["go1234"])
	service := NewService(repo, fakeStats{}, cache, time.UTC,
		WithClock(func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }),
	)

	target := "https://example.org/updated"
	if _, err := service.Update(context.Background(), "go1234", domain.LinkPatch{TargetURL: &target}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if _, ok := cache.Get("go1234"); ok {
		t.Fatal("update did not invalidate cache")
	}
	if err := service.Delete(context.Background(), "go1234"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(cache.deletes) != 2 {
		t.Fatalf("cache invalidations = %v", cache.deletes)
	}
}

func TestRandomCodeFormat(t *testing.T) {
	code, err := randomCode()
	if err != nil {
		t.Fatalf("randomCode() error = %v", err)
	}
	if len(code) != generatedCodeLength || !aliasPattern.MatchString(code) {
		t.Fatalf("randomCode() = %q", code)
	}
}

func TestValidateTargetURLRejectsUnsafeValues(t *testing.T) {
	invalid := []string{
		"",
		"example.com",
		"javascript:alert(1)",
		"https://user:pass@example.com",
		"https://example.com/a b",
		strings.Repeat("a", maxTargetURLLength+1),
	}
	for _, value := range invalid {
		if _, err := ValidateTargetURL(value); !errors.Is(err, domain.ErrInvalidTargetURL) {
			t.Errorf("ValidateTargetURL(%q) error = %v", value, err)
		}
	}
}

func BenchmarkResolveCacheHit(b *testing.B) {
	repo := newFakeRepository()
	cache := newFakeCache()
	repo.links["go1234"] = domain.Link{ID: 1, Code: "go1234", TargetURL: "https://example.com", IsActive: true}
	cache.Set(repo.links["go1234"])
	service := NewService(repo, fakeStats{}, cache, time.UTC)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := service.Resolve(ctx, "go1234"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResolveCacheHitParallel(b *testing.B) {
	repo := newFakeRepository()
	cache := newFakeCache()
	repo.links["go1234"] = domain.Link{ID: 1, Code: "go1234", TargetURL: "https://example.com", IsActive: true}
	cache.Set(repo.links["go1234"])
	service := NewService(repo, fakeStats{}, cache, time.UTC)
	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := service.Resolve(ctx, "go1234"); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
