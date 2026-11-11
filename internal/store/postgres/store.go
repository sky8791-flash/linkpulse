package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sky8791-flash/linkpulse/internal/domain"
)

//go:embed migrations/*.sql
var migrations embed.FS

const linkReturning = `
	id,
	code,
	target_url,
	COALESCE(expires_at, 'epoch'::timestamptz),
	expires_at IS NOT NULL,
	is_active,
	created_at,
	updated_at`

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = 12
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 15 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	store := &Store{pool: pool}
	if err := store.ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func NewWithPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ready(ctx context.Context) error {
	return s.ping(ctx)
}

func (s *Store) ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := s.applyMigration(ctx, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, version string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", version, err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	var applied bool
	if err := tx.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)",
		version,
	).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %s: %w", version, err)
	}
	if applied {
		return tx.Commit(ctx)
	}

	script, err := migrations.ReadFile("migrations/" + version)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx, string(script)); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version) VALUES ($1)",
		version,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}

func (s *Store) Create(ctx context.Context, link domain.Link) (domain.Link, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO links (code, target_url, expires_at, is_active)
		VALUES ($1, $2, $3, $4)
		RETURNING `+linkReturning,
		link.Code,
		link.TargetURL,
		link.ExpiresAt,
		link.IsActive,
	)
	created, err := scanLink(row)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.Link{}, domain.ErrConflict
		}
		return domain.Link{}, fmt.Errorf("create link: %w", err)
	}
	return created, nil
}

func (s *Store) GetByCode(ctx context.Context, code string) (domain.Link, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+linkReturning+`
		FROM links
		WHERE code = $1 AND deleted_at IS NULL`,
		code,
	)
	found, err := scanLink(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Link{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Link{}, fmt.Errorf("get link: %w", err)
	}
	return found, nil
}

func (s *Store) Update(ctx context.Context, code string, patch domain.LinkPatch) (domain.Link, error) {
	var expiresAt any
	if patch.ExpiresAt.Set && patch.ExpiresAt.Value != nil {
		expiresAt = *patch.ExpiresAt.Value
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE links
		SET target_url = COALESCE($2, target_url),
			expires_at = CASE WHEN $3 THEN $4 ELSE expires_at END,
			is_active = COALESCE($5, is_active),
			updated_at = now()
		WHERE code = $1 AND deleted_at IS NULL
		RETURNING `+linkReturning,
		code,
		patch.TargetURL,
		patch.ExpiresAt.Set,
		expiresAt,
		patch.IsActive,
	)
	updated, err := scanLink(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Link{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Link{}, fmt.Errorf("update link: %w", err)
	}
	return updated, nil
}

func (s *Store) Delete(ctx context.Context, code string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE links
		SET deleted_at = COALESCE(deleted_at, now()),
			updated_at = now()
		WHERE code = $1`,
		code,
	)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	return nil
}

func (s *Store) List(ctx context.Context, cursor int64, limit int) (domain.LinkPage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+linkReturning+`
		FROM links
		WHERE deleted_at IS NULL
			AND ($1 = 0 OR id < $1)
		ORDER BY id DESC
		LIMIT $2`,
		cursor,
		limit+1,
	)
	if err != nil {
		return domain.LinkPage{}, fmt.Errorf("list links: %w", err)
	}
	defer rows.Close()

	items := make([]domain.Link, 0, limit)
	for rows.Next() {
		item, err := scanLink(rows)
		if err != nil {
			return domain.LinkPage{}, fmt.Errorf("scan listed link: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.LinkPage{}, fmt.Errorf("iterate links: %w", err)
	}

	var nextCursor *int64
	if len(items) > limit {
		items = items[:limit]
		cursorValue := items[len(items)-1].ID
		nextCursor = &cursorValue
	}
	return domain.LinkPage{Items: items, NextCursor: nextCursor}, nil
}

func (s *Store) InsertClicks(ctx context.Context, events []domain.ClickEvent) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(events))
	for _, event := range events {
		rows = append(rows, []any{
			event.LinkID,
			event.OccurredAt,
			event.VisitorHash,
			event.Referrer,
			event.Device,
			event.Browser,
		})
	}
	_, err := s.pool.CopyFrom(
		ctx,
		pgx.Identifier{"click_events"},
		[]string{"link_id", "occurred_at", "visitor_hash", "referrer", "device", "browser"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("insert click events: %w", err)
	}
	return nil
}

func (s *Store) ClickStats(
	ctx context.Context,
	linkID int64,
	from, to time.Time,
	location *time.Location,
) (domain.ClickStats, error) {
	if location == nil {
		location = time.UTC
	}

	var stats domain.ClickStats
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT visitor_hash)
		FROM click_events
		WHERE link_id = $1 AND occurred_at >= $2 AND occurred_at < $3`,
		linkID, from, to,
	).Scan(&stats.TotalClicks, &stats.UniqueVisitors); err != nil {
		return domain.ClickStats{}, fmt.Errorf("query click totals: %w", err)
	}

	referrers, err := s.breakdown(ctx, linkID, from, to, "referrer")
	if err != nil {
		return domain.ClickStats{}, err
	}
	devices, err := s.breakdown(ctx, linkID, from, to, "device")
	if err != nil {
		return domain.ClickStats{}, err
	}
	daily, err := s.dailyStats(ctx, linkID, from, to, location)
	if err != nil {
		return domain.ClickStats{}, err
	}

	fromLocal := from.In(location)
	toLocal := to.Add(-time.Nanosecond).In(location)
	stats.From = fromLocal.Format(time.DateOnly)
	stats.To = toLocal.Format(time.DateOnly)
	stats.Referrers = referrers
	stats.Devices = devices
	stats.Daily = daily
	return stats, nil
}

func (s *Store) breakdown(
	ctx context.Context,
	linkID int64,
	from, to time.Time,
	column string,
) ([]domain.Breakdown, error) {
	var expression string
	switch column {
	case "referrer":
		expression = "COALESCE(NULLIF(referrer, ''), 'direct')"
	case "device":
		expression = "device"
	default:
		return nil, fmt.Errorf("unsupported breakdown column %q", column)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+expression+` AS name, count(*) AS clicks
		FROM click_events
		WHERE link_id = $1 AND occurred_at >= $2 AND occurred_at < $3
		GROUP BY 1
		ORDER BY clicks DESC, name ASC
		LIMIT 10`,
		linkID, from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("query %s breakdown: %w", column, err)
	}
	defer rows.Close()

	items := make([]domain.Breakdown, 0, 10)
	for rows.Next() {
		var item domain.Breakdown
		if err := rows.Scan(&item.Name, &item.Clicks); err != nil {
			return nil, fmt.Errorf("scan %s breakdown: %w", column, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s breakdown: %w", column, err)
	}
	return items, nil
}

func (s *Store) dailyStats(
	ctx context.Context,
	linkID int64,
	from, to time.Time,
	location *time.Location,
) ([]domain.DailyStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT to_char((occurred_at AT TIME ZONE $4::text)::date, 'YYYY-MM-DD'),
			count(*),
			count(DISTINCT visitor_hash)
		FROM click_events
		WHERE link_id = $1 AND occurred_at >= $2 AND occurred_at < $3
		GROUP BY 1
		ORDER BY 1 ASC`,
		linkID, from, to, location.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("query daily stats: %w", err)
	}
	defer rows.Close()

	byDate := make(map[string]domain.DailyStat)
	for rows.Next() {
		var item domain.DailyStat
		if err := rows.Scan(&item.Date, &item.Clicks, &item.UniqueVisitors); err != nil {
			return nil, fmt.Errorf("scan daily stats: %w", err)
		}
		byDate[item.Date] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily stats: %w", err)
	}

	start := from.In(location)
	end := to.In(location)
	days := make([]domain.DailyStat, 0, int(end.Sub(start).Hours()/24)+1)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		item, ok := byDate[key]
		if !ok {
			item = domain.DailyStat{Date: key}
		}
		days = append(days, item)
	}
	return days, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLink(row rowScanner) (domain.Link, error) {
	var link domain.Link
	var expiresAt time.Time
	var hasExpiration bool
	if err := row.Scan(
		&link.ID,
		&link.Code,
		&link.TargetURL,
		&expiresAt,
		&hasExpiration,
		&link.IsActive,
		&link.CreatedAt,
		&link.UpdatedAt,
	); err != nil {
		return domain.Link{}, err
	}
	if hasExpiration {
		value := expiresAt.UTC()
		link.ExpiresAt = &value
	}
	return link, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
