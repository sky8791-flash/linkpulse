package domain

import (
	"errors"
	"time"
)

var (
	ErrNotFound          = errors.New("link not found")
	ErrConflict          = errors.New("link already exists")
	ErrExpired           = errors.New("link has expired")
	ErrInactive          = errors.New("link is inactive")
	ErrInvalidTargetURL  = errors.New("invalid target URL")
	ErrInvalidAlias      = errors.New("invalid alias")
	ErrInvalidExpiration = errors.New("invalid expiration")
)

// Link is the persisted representation of a short link.
type Link struct {
	ID        int64
	Code      string
	TargetURL string
	ExpiresAt *time.Time
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LinkPatch uses explicit set flags so PATCH can distinguish an omitted field
// from an explicitly cleared expiration time.
type LinkPatch struct {
	TargetURL *string
	ExpiresAt OptionalTime
	IsActive  *bool
}

type OptionalTime struct {
	Set   bool
	Value *time.Time
}

func (l Link) AvailabilityError(now time.Time) error {
	if !l.IsActive {
		return ErrInactive
	}
	if l.ExpiresAt != nil && !now.Before(*l.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

type LinkPage struct {
	Items      []Link
	NextCursor *int64
}

type ClickEvent struct {
	LinkID      int64
	OccurredAt  time.Time
	VisitorHash string
	Referrer    string
	Device      string
	Browser     string
}

type Breakdown struct {
	Name   string `json:"name"`
	Clicks int64  `json:"clicks"`
}

type DailyStat struct {
	Date           string `json:"date"`
	Clicks         int64  `json:"clicks"`
	UniqueVisitors int64  `json:"unique_visitors"`
}

type ClickStats struct {
	Code           string      `json:"code"`
	From           string      `json:"from"`
	To             string      `json:"to"`
	TotalClicks    int64       `json:"total_clicks"`
	UniqueVisitors int64       `json:"unique_visitors"`
	Referrers      []Breakdown `json:"referrers"`
	Devices        []Breakdown `json:"devices"`
	Daily          []DailyStat `json:"daily"`
}
