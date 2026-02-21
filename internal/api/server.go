package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mattjoyce/agenticloop/internal/store"
)

// RunCreator creates and enqueues runs.
type RunCreator interface {
	Create(ctx context.Context, goal string, wakeID *string, runCtx json.RawMessage, constraints json.RawMessage) (*store.Run, bool, error)
	GetByID(ctx context.Context, id string) (*store.Run, error)
	Enqueue(runID string) error
}

// Config holds API server configuration.
type Config struct {
	Listen                  string
	Token                   string
	WorkspaceDir            string
	StreamPollInterval      time.Duration
	StreamHeartbeatInterval time.Duration
}

// Server represents the HTTP API server.
type Server struct {
	config    Config
	runs      *store.RunStore
	creator   RunCreator
	logger    *slog.Logger
	server    *http.Server
	startedAt time.Time
}

// New creates a new API server instance.
func New(config Config, runs *store.RunStore, creator RunCreator, logger *slog.Logger) *Server {
	return &Server{
		config:    config,
		runs:      runs,
		creator:   creator,
		logger:    logger,
		startedAt: time.Now(),
	}
}

// Start starts the HTTP server (blocking).
func (s *Server) Start(ctx context.Context) error {
	router := s.setupRoutes()

	s.server = &http.Server{
		Addr:         s.config.Listen,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // SSE endpoints are long-lived streams.
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("API server starting", "listen", s.config.Listen)

	errCh := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		s.logger.Info("API server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown failed: %w", err)
		}
		return ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}

// setupRoutes configures the HTTP router.
func (s *Server) setupRoutes() *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.loggingMiddleware)
	r.Use(middleware.Recoverer)

	// Unauthenticated
	r.Get("/healthz", s.handleHealthz)

	// Protected
	r.Group(func(r chi.Router) {
		r.Use(s.bearerAuth)
		r.Post("/v1/wake", s.handleWake)
		r.Get("/v1/runs", s.handleListRuns)
		r.Get("/v1/runs/{run_id}", s.handleGetRun)
		r.Get("/v1/runs/{run_id}/workspace", s.handleRunWorkspace)
		r.Get("/v1/runs/{run_id}/events", s.handleRunEvents)
	})

	return r
}

// loggingMiddleware logs HTTP requests.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}
