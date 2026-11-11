package cache

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/domain"
)

type LinkCache interface {
	Get(code string) (domain.Link, bool)
	Set(link domain.Link)
	Delete(code string)
}

type TTL struct {
	mu      sync.Mutex
	maxSize int
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*list.Element
	order   *list.List
	hits    atomic.Uint64
	misses  atomic.Uint64
}

type entry struct {
	code      string
	link      domain.Link
	expiresAt time.Time
}

func NewTTL(maxSize int, ttl time.Duration) *TTL {
	return NewTTLWithClock(maxSize, ttl, time.Now)
}

func NewTTLWithClock(maxSize int, ttl time.Duration, now func() time.Time) *TTL {
	if maxSize < 1 {
		panic("cache size must be positive")
	}
	return &TTL{
		maxSize: maxSize,
		ttl:     ttl,
		now:     now,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

func (c *TTL) Get(code string) (domain.Link, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[code]
	if !ok {
		c.misses.Add(1)
		return domain.Link{}, false
	}
	value := element.Value.(*entry)
	if !c.now().Before(value.expiresAt) {
		c.removeElement(element)
		c.misses.Add(1)
		return domain.Link{}, false
	}
	c.hits.Add(1)
	return value.link, true
}

func (c *TTL) Set(link domain.Link) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	expiresAt := now.Add(c.ttl)
	if link.ExpiresAt != nil && link.ExpiresAt.Before(expiresAt) {
		expiresAt = *link.ExpiresAt
	}
	if !now.Before(expiresAt) {
		c.remove(link.Code)
		return
	}

	if element, ok := c.entries[link.Code]; ok {
		element.Value.(*entry).link = link
		element.Value.(*entry).expiresAt = expiresAt
		c.order.MoveToBack(element)
		return
	}

	element := c.order.PushBack(&entry{code: link.Code, link: link, expiresAt: expiresAt})
	c.entries[link.Code] = element
	c.evict(now)
}

func (c *TTL) Delete(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(code)
}

func (c *TTL) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *TTL) Counters() (hits, misses uint64) {
	return c.hits.Load(), c.misses.Load()
}

func (c *TTL) evict(now time.Time) {
	for element := c.order.Front(); element != nil && len(c.entries) > c.maxSize; {
		next := element.Next()
		value := element.Value.(*entry)
		if !now.Before(value.expiresAt) || len(c.entries) > c.maxSize {
			c.removeElement(element)
		}
		element = next
	}
}

func (c *TTL) remove(code string) {
	if element, ok := c.entries[code]; ok {
		c.removeElement(element)
	}
}

func (c *TTL) removeElement(element *list.Element) {
	value := element.Value.(*entry)
	delete(c.entries, value.code)
	c.order.Remove(element)
}
