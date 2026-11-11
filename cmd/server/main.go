package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/analytics"
	"github.com/sky8791-flash/linkpulse/internal/cache"
	"github.com/sky8791-flash/linkpulse/internal/config"
	"github.com/sky8791-flash/linkpulse/internal/httpapi"
	"github.com/sky8791-flash/linkpulse/internal/link"
	"github.com/sky8791-flash/linkpulse/internal/ratelimit"
	"github.com/sky8791-flash/linkpulse/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer startupCancel()
	store, err := postgres.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}

	linkCache := cache.NewTTL(cfg.CacheSize, cfg.CacheTTL)
	collector := analytics.NewCollector(store, analytics.CollectorConfig{
		QueueSize:      cfg.ClickQueueSize,
		BatchSize:      cfg.ClickBatchSize,
		FlushInterval:  cfg.ClickFlushInterval,
		WriteRetries:   cfg.ClickWriteRetries,
		RetryBaseDelay: cfg.ClickRetryBaseDelay,
	}, logger)
	collector.Start()

	links := link.NewService(store, store, linkCache, cfg.StatsLocation)
	handler := httpapi.NewHandler(httpapi.Dependencies{
		Links:             links,
		Recorder:          collector,
		Ready:             store.Ready,
		APIKey:            cfg.APIKey,
		BaseURL:           cfg.BaseURL,
		VisitorHashSecret: cfg.VisitorHashSecret,
		StatsLocation:     cfg.StatsLocation,
		PublicLimiter:     ratelimit.New(cfg.PublicRateLimit, cfg.PublicRateBurst),
		AdminLimiter:      ratelimit.New(cfg.AdminRateLimit, cfg.AdminRateBurst),
		Logger:            logger,
	})

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "address", cfg.HTTPAddr, "base_url", cfg.BaseURL)
		serverErrors <- httpServer.ListenAndServe()
	}()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	select {
	case <-signalCtx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped unexpectedly", "error", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP shutdown failed", "error", err)
	}
	if err := collector.Shutdown(shutdownCtx); err != nil {
		logger.Error("click collector shutdown failed", "error", err)
	}
	hits, misses := linkCache.Counters()
	logger.Info("cache counters at shutdown",
		"hits", hits,
		"misses", misses,
		"dropped_clicks", collector.Dropped(),
	)
	store.Close()
	logger.Info("shutdown complete")
}
