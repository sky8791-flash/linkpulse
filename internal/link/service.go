package link

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/domain"
)

const (
	generatedCodeLength = 7
	maxTargetURLLength  = 2048
	maxAliasLength      = 32
	minAliasLength      = 4
	maxCodeAttempts     = 5
)

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var reservedAliases = map[string]struct{}{
	"api":          {},
	"assets":       {},
	"healthz":      {},
	"readyz":       {},
	"openapi.yaml": {},
	"favicon.ico":  {},
}

type Repository interface {
	Create(ctx context.Context, link domain.Link) (domain.Link, error)
	GetByCode(ctx context.Context, code string) (domain.Link, error)
	Update(ctx context.Context, code string, patch domain.LinkPatch) (domain.Link, error)
	Delete(ctx context.Context, code string) error
	List(ctx context.Context, cursor int64, limit int) (domain.LinkPage, error)
}

type StatsReader interface {
	ClickStats(ctx context.Context, linkID int64, from, to time.Time, location *time.Location) (domain.ClickStats, error)
}

type LinkCache interface {
	Get(code string) (domain.Link, bool)
	Set(link domain.Link)
	Delete(code string)
}

type Service struct {
	repo          Repository
	stats         StatsReader
	cache         LinkCache
	now           func() time.Time
	generateCode  func() (string, error)
	statsLocation *time.Location
}

type Option func(*Service)

func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		service.now = now
	}
}

func WithCodeGenerator(generate func() (string, error)) Option {
	return func(service *Service) {
		service.generateCode = generate
	}
}

func NewService(repo Repository, stats StatsReader, cache LinkCache, statsLocation *time.Location, options ...Option) *Service {
	if statsLocation == nil {
		statsLocation = time.UTC
	}
	service := &Service{
		repo:          repo,
		stats:         stats,
		cache:         cache,
		now:           time.Now,
		generateCode:  randomCode,
		statsLocation: statsLocation,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

type CreateInput struct {
	TargetURL   string
	CustomAlias string
	ExpiresAt   *time.Time
}

func (s *Service) Create(ctx context.Context, input CreateInput) (domain.Link, error) {
	target, err := ValidateTargetURL(input.TargetURL)
	if err != nil {
		return domain.Link{}, err
	}
	if err := s.validateExpiration(input.ExpiresAt); err != nil {
		return domain.Link{}, err
	}

	if input.CustomAlias != "" {
		if err := ValidateAlias(input.CustomAlias); err != nil {
			return domain.Link{}, err
		}
		return s.repo.Create(ctx, domain.Link{
			Code:      input.CustomAlias,
			TargetURL: target,
			ExpiresAt: input.ExpiresAt,
			IsActive:  true,
		})
	}

	var lastErr error
	for attempt := 0; attempt < maxCodeAttempts; attempt++ {
		code, err := s.generateCode()
		if err != nil {
			return domain.Link{}, fmt.Errorf("generate short code: %w", err)
		}
		created, err := s.repo.Create(ctx, domain.Link{
			Code:      code,
			TargetURL: target,
			ExpiresAt: input.ExpiresAt,
			IsActive:  true,
		})
		if err == nil {
			return created, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			return domain.Link{}, err
		}
		lastErr = err
	}
	return domain.Link{}, fmt.Errorf("could not allocate unique short code after %d attempts: %w", maxCodeAttempts, lastErr)
}

func (s *Service) Resolve(ctx context.Context, code string) (domain.Link, error) {
	if !isLookupCode(code) {
		return domain.Link{}, domain.ErrNotFound
	}

	if cached, ok := s.cache.Get(code); ok {
		if err := cached.AvailabilityError(s.now()); err != nil {
			return domain.Link{}, err
		}
		return cached, nil
	}

	found, err := s.repo.GetByCode(ctx, code)
	if err != nil {
		return domain.Link{}, err
	}
	if err := found.AvailabilityError(s.now()); err != nil {
		return domain.Link{}, err
	}
	s.cache.Set(found)
	return found, nil
}

func (s *Service) Get(ctx context.Context, code string) (domain.Link, error) {
	if !isLookupCode(code) {
		return domain.Link{}, domain.ErrNotFound
	}
	return s.repo.GetByCode(ctx, code)
}

func (s *Service) List(ctx context.Context, cursor int64, limit int) (domain.LinkPage, error) {
	if cursor < 0 {
		return domain.LinkPage{}, fmt.Errorf("cursor must be non-negative")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return s.repo.List(ctx, cursor, limit)
}

func (s *Service) Update(ctx context.Context, code string, patch domain.LinkPatch) (domain.Link, error) {
	if !isLookupCode(code) {
		return domain.Link{}, domain.ErrNotFound
	}
	if patch.TargetURL != nil {
		target, err := ValidateTargetURL(*patch.TargetURL)
		if err != nil {
			return domain.Link{}, err
		}
		patch.TargetURL = &target
	}
	if patch.ExpiresAt.Set {
		if err := s.validateExpiration(patch.ExpiresAt.Value); err != nil {
			return domain.Link{}, err
		}
	}
	updated, err := s.repo.Update(ctx, code, patch)
	if err == nil {
		s.cache.Delete(code)
	}
	return updated, err
}

func (s *Service) Delete(ctx context.Context, code string) error {
	if !isLookupCode(code) {
		return nil
	}
	if err := s.repo.Delete(ctx, code); err != nil {
		return err
	}
	s.cache.Delete(code)
	return nil
}

func (s *Service) Stats(ctx context.Context, code string, from, to time.Time) (domain.ClickStats, error) {
	found, err := s.Get(ctx, code)
	if err != nil {
		return domain.ClickStats{}, err
	}
	if from.IsZero() || to.IsZero() {
		return domain.ClickStats{}, errors.New("stats date range is required")
	}
	if !from.Before(to) {
		return domain.ClickStats{}, errors.New("stats start must be before end")
	}
	return s.stats.ClickStats(ctx, found.ID, from, to, s.statsLocation)
}

func (s *Service) validateExpiration(expiresAt *time.Time) error {
	if expiresAt != nil && !expiresAt.After(s.now()) {
		return domain.ErrInvalidExpiration
	}
	return nil
}

func ValidateTargetURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxTargetURLLength || strings.ContainsAny(raw, " \t\r\n") {
		return "", domain.ErrInvalidTargetURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil {
		return "", domain.ErrInvalidTargetURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", domain.ErrInvalidTargetURL
	}
	return parsed.String(), nil
}

func ValidateAlias(alias string) error {
	if len(alias) < minAliasLength || len(alias) > maxAliasLength || !aliasPattern.MatchString(alias) {
		return domain.ErrInvalidAlias
	}
	if _, reserved := reservedAliases[strings.ToLower(alias)]; reserved {
		return fmt.Errorf("%w: reserved alias", domain.ErrInvalidAlias)
	}
	return nil
}

func isLookupCode(code string) bool {
	return len(code) >= minAliasLength && len(code) <= maxAliasLength && aliasPattern.MatchString(code)
}

func randomCode() (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	result := make([]byte, generatedCodeLength)
	limit := big.NewInt(int64(len(alphabet)))
	for i := range result {
		index, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		result[i] = alphabet[index.Int64()]
	}
	return string(result), nil
}
