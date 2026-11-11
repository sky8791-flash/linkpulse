package analytics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/domain"
)

type fakeWriter struct {
	mu        sync.Mutex
	events    []domain.ClickEvent
	failures  int
	callCount int
}

func (w *fakeWriter) InsertClicks(_ context.Context, events []domain.ClickEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callCount++
	if w.failures > 0 {
		w.failures--
		return errors.New("temporary write failure")
	}
	w.events = append(w.events, events...)
	return nil
}

func (w *fakeWriter) snapshot() ([]domain.ClickEvent, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]domain.ClickEvent(nil), w.events...), w.callCount
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCollectorBatchesAndDrainsOnShutdown(t *testing.T) {
	writer := &fakeWriter{}
	collector := NewCollector(writer, CollectorConfig{
		QueueSize:     10,
		BatchSize:     3,
		FlushInterval: time.Hour,
		WriteRetries:  1,
	}, testLogger())
	collector.Start()

	for i := 0; i < 5; i++ {
		if !collector.Record(domain.ClickEvent{LinkID: int64(i + 1)}) {
			t.Fatal("Record() unexpectedly dropped event")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := collector.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	events, _ := writer.snapshot()
	if len(events) != 5 {
		t.Fatalf("persisted %d events, want 5", len(events))
	}
	if collector.Record(domain.ClickEvent{}) {
		t.Fatal("Record() accepted an event after shutdown")
	}
}

func TestCollectorDropsWhenQueueIsFull(t *testing.T) {
	collector := NewCollector(&fakeWriter{}, CollectorConfig{
		QueueSize:     1,
		BatchSize:     1,
		FlushInterval: time.Hour,
		WriteRetries:  1,
	}, testLogger())
	if !collector.Record(domain.ClickEvent{LinkID: 1}) {
		t.Fatal("first event was dropped")
	}
	if collector.Record(domain.ClickEvent{LinkID: 2}) {
		t.Fatal("full queue accepted another event")
	}
	if collector.Dropped() != 1 {
		t.Fatalf("Dropped() = %d", collector.Dropped())
	}

	collector.Start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := collector.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestCollectorRetriesFailedBatch(t *testing.T) {
	writer := &fakeWriter{failures: 2}
	collector := NewCollector(writer, CollectorConfig{
		QueueSize:      2,
		BatchSize:      1,
		FlushInterval:  time.Hour,
		WriteRetries:   3,
		RetryBaseDelay: time.Millisecond,
	}, testLogger())
	collector.Start()
	collector.Record(domain.ClickEvent{LinkID: 1})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := collector.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	events, calls := writer.snapshot()
	if len(events) != 1 || calls != 3 {
		t.Fatalf("events = %d, calls = %d", len(events), calls)
	}
}
