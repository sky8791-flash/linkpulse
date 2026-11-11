package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/analytics"
	"github.com/sky8791-flash/linkpulse/internal/domain"
	"github.com/sky8791-flash/linkpulse/internal/store/postgres"
	"github.com/sky8791-flash/linkpulse/internal/testutil"
)

func TestStoreLifecycleAndClickStats(t *testing.T) {
	pool := testutil.NewPostgresSchema(t)
	store := postgres.NewWithPool(pool)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	expiresAt := now.Add(24 * time.Hour)
	created, err := store.Create(ctx, domain.Link{
		Code: "go1234", TargetURL: "https://example.com", ExpiresAt: &expiresAt, IsActive: true,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == 0 || created.ExpiresAt == nil || !created.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("created link = %+v", created)
	}

	if _, err := store.Create(ctx, domain.Link{
		Code: "go1234", TargetURL: "https://example.org", IsActive: true,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate Create() error = %v", err)
	}

	second, err := store.Create(ctx, domain.Link{
		Code: "go5678", TargetURL: "https://example.org", IsActive: true,
	})
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}

	page, err := store.List(ctx, 0, 1)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Items) != 1 || page.NextCursor == nil || page.Items[0].ID != second.ID {
		t.Fatalf("first page = %+v", page)
	}
	page, err = store.List(ctx, *page.NextCursor, 1)
	if err != nil {
		t.Fatalf("second List() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != created.ID {
		t.Fatalf("second page = %+v", page)
	}

	target := "https://example.com/updated"
	updated, err := store.Update(ctx, "go1234", domain.LinkPatch{
		TargetURL: &target,
		ExpiresAt: domain.OptionalTime{Set: true},
		IsActive:  boolPointer(false),
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.TargetURL != target || updated.ExpiresAt != nil || updated.IsActive {
		t.Fatalf("updated link = %+v", updated)
	}

	events := []domain.ClickEvent{
		{
			LinkID: created.ID, OccurredAt: now.Add(-2 * time.Hour),
			VisitorHash: analytics.VisitorHash("secret", "192.0.2.1"),
			Referrer:    "example.com", Device: "desktop", Browser: "Chrome",
		},
		{
			LinkID: created.ID, OccurredAt: now.Add(-time.Hour),
			VisitorHash: analytics.VisitorHash("secret", "192.0.2.1"),
			Referrer:    "example.com", Device: "mobile", Browser: "Safari",
		},
		{
			LinkID: created.ID, OccurredAt: now.Add(-time.Hour),
			VisitorHash: analytics.VisitorHash("secret", "192.0.2.2"),
			Referrer:    "", Device: "desktop", Browser: "Firefox",
		},
	}
	if err := store.InsertClicks(ctx, events); err != nil {
		t.Fatalf("InsertClicks() error = %v", err)
	}

	stats, err := store.ClickStats(ctx, created.ID, now.Add(-24*time.Hour), now.Add(time.Hour), time.UTC)
	if err != nil {
		t.Fatalf("ClickStats() error = %v", err)
	}
	if stats.TotalClicks != 3 || stats.UniqueVisitors != 2 {
		t.Fatalf("stats totals = %+v", stats)
	}
	if len(stats.Referrers) != 2 || len(stats.Devices) != 2 {
		t.Fatalf("stats breakdowns = %+v", stats)
	}
	if len(stats.Daily) != 2 {
		t.Fatalf("daily buckets = %d", len(stats.Daily))
	}

	if err := store.Delete(ctx, "go1234"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.GetByCode(ctx, "go1234"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get after delete error = %v", err)
	}
	if err := store.Delete(ctx, "missing"); err != nil {
		t.Fatalf("idempotent Delete() error = %v", err)
	}
}

func boolPointer(value bool) *bool {
	return &value
}
