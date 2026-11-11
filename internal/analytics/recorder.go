package analytics

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/domain"
)

type ClickWriter interface {
	InsertClicks(ctx context.Context, events []domain.ClickEvent) error
}

type ClickRecorder interface {
	Record(event domain.ClickEvent) bool
	Dropped() uint64
}

type Collector struct {
	store        ClickWriter
	queue        chan domain.ClickEvent
	batchSize    int
	flushEvery   time.Duration
	retries      int
	retryDelay   time.Duration
	logger       *slog.Logger
	dropped      atomic.Uint64
	mu           sync.RWMutex
	closed       bool
	done         chan struct{}
	shutdownOnce sync.Once
}

type CollectorConfig struct {
	QueueSize      int
	BatchSize      int
	FlushInterval  time.Duration
	WriteRetries   int
	RetryBaseDelay time.Duration
}

func NewCollector(store ClickWriter, cfg CollectorConfig, logger *slog.Logger) *Collector {
	if cfg.QueueSize < 1 || cfg.BatchSize < 1 || cfg.FlushInterval <= 0 {
		panic("invalid collector configuration")
	}
	if cfg.WriteRetries < 1 {
		cfg.WriteRetries = 1
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = 100 * time.Millisecond
	}
	return &Collector{
		store:      store,
		queue:      make(chan domain.ClickEvent, cfg.QueueSize),
		batchSize:  cfg.BatchSize,
		flushEvery: cfg.FlushInterval,
		retries:    cfg.WriteRetries,
		retryDelay: cfg.RetryBaseDelay,
		logger:     logger,
		done:       make(chan struct{}),
	}
}

func (c *Collector) Start() {
	go c.run()
}

func (c *Collector) Record(event domain.ClickEvent) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		c.dropped.Add(1)
		return false
	}
	select {
	case c.queue <- event:
		return true
	default:
		c.dropped.Add(1)
		return false
	}
}

func (c *Collector) Dropped() uint64 {
	return c.dropped.Load()
}

func (c *Collector) Shutdown(ctx context.Context) error {
	c.shutdownOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.queue)
		c.mu.Unlock()
	})

	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Collector) run() {
	defer close(c.done)

	ticker := time.NewTicker(c.flushEvery)
	defer ticker.Stop()

	batch := make([]domain.ClickEvent, 0, c.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.persist(batch)
		batch = batch[:0]
	}

	for {
		select {
		case event, ok := <-c.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, event)
			if len(batch) >= c.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (c *Collector) persist(events []domain.ClickEvent) {
	batch := append([]domain.ClickEvent(nil), events...)
	var lastErr error
	for attempt := 1; attempt <= c.retries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.store.InsertClicks(ctx, batch)
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		if attempt < c.retries {
			c.logger.Warn("click batch write failed; retrying",
				"attempt", attempt,
				"events", len(batch),
				"error", err,
			)
			time.Sleep(time.Duration(attempt) * c.retryDelay)
		}
	}

	c.dropped.Add(uint64(len(batch)))
	c.logger.Error("click batch dropped after retries",
		"events", len(batch),
		"error", errors.Unwrap(lastErr),
		"last_error", lastErr,
	)
}
