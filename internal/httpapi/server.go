package httpapi

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/analytics"
	"github.com/sky8791-flash/linkpulse/internal/link"
	"github.com/sky8791-flash/linkpulse/internal/ratelimit"
)

//go:embed openapi.yaml web/*
var embedded embed.FS

type Dependencies struct {
	Links             *link.Service
	Recorder          analytics.ClickRecorder
	Ready             func(context.Context) error
	APIKey            string
	BaseURL           string
	VisitorHashSecret string
	StatsLocation     *time.Location
	PublicLimiter     *ratelimit.Limiter
	AdminLimiter      *ratelimit.Limiter
	Logger            *slog.Logger
	Now               func() time.Time
}

type server struct {
	Dependencies
}

func NewHandler(deps Dependencies) http.Handler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.StatsLocation == nil {
		deps.StatsLocation = time.UTC
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	api := &server{Dependencies: deps}
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", http.HandlerFunc(api.health))
	mux.Handle("GET /readyz", http.HandlerFunc(api.ready))
	mux.Handle("GET /openapi.yaml", http.HandlerFunc(api.openapi))

	admin := func(handler http.HandlerFunc) http.Handler {
		return api.adminMiddleware(handler)
	}
	mux.Handle("POST /api/v1/links", admin(api.createLink))
	mux.Handle("GET /api/v1/links", admin(api.listLinks))
	mux.Handle("GET /api/v1/links/{code}", admin(api.getLink))
	mux.Handle("PATCH /api/v1/links/{code}", admin(api.updateLink))
	mux.Handle("DELETE /api/v1/links/{code}", admin(api.deleteLink))
	mux.Handle("GET /api/v1/links/{code}/stats", admin(api.linkStats))

	webRoot, err := fs.Sub(embedded, "web")
	if err != nil {
		panic(err)
	}
	webFiles := http.FileServer(http.FS(webRoot))
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", webFiles))
	mux.HandleFunc("GET /{$}", api.dashboard)

	mux.Handle("GET /{code}", api.publicMiddleware(http.HandlerFunc(api.redirect)))

	return api.requestID(api.securityHeaders(api.accessLog(api.recoverer(mux))))
}

func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return time.Now().UTC().Format("20060102150405.000000000")
}
