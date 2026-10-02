// Package web hosts the HTTP server: the REST API and, in development, the
// frontend dev-server proxy.
package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/store"
)

// Server wraps the HTTP mux with lifecycle management.
type Server struct {
	cfg   *config.Config
	log   *slog.Logger
	store *store.Store
	http  *http.Server
	ln    net.Listener
}

func NewServer(cfg *config.Config, log *slog.Logger, st *store.Store) *Server {
	return &Server{cfg: cfg, log: log, store: st}
}

// Routes builds the request multiplexer.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.recoverer)
	r.Use(s.requestLogger)

	// Probes, deliberately unauthenticated and cheap.
	r.Get("/-/health", s.handleHealth)
	r.Get("/-/ready", s.handleReady)

	r.Route("/api/v1", func(api chi.Router) {
		api.Get("/version", s.handleVersion)
		api.Get("/health", s.handleHealth)
	})

	// The SPA is served by nginx in production; in development Nuxt runs
	// separately and proxies /api here, so anything unmatched is a 404.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		s.writeError(w, req, http.StatusNotFound, "not_found", "the requested resource does not exist")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		s.writeError(w, req, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("%s is not allowed on this endpoint", req.Method))
	})

	return r
}

// Listen binds the address. Binding before serving lets the process report the
// real port, which matters when the port is 0 in tests.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.HTTPAddr, err)
	}
	s.ln = ln
	return nil
}

// Addr returns the bound address, or the configured one before Listen.
func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.cfg.HTTPAddr
}

func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}

	s.http = &http.Server{
		Handler:           s.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.Addr())
		errCh <- s.http.Serve(s.ln)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return s.Shutdown()
	}
}

// Shutdown drains in-flight requests before returning.
func (s *Server) Shutdown() error {
	if s.http == nil {
		return nil
	}
	s.log.Info("shutting down http server")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

type statusResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok", Version: version})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Health(r.Context()); err != nil {
		s.log.Error("readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable,
			statusResponse{Status: "unavailable", Error: "database unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ready", Version: version})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version,
		"server":  "dogit",
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request",
					"method", r.Method, "path", r.URL.Path, "panic", rec)
				s.writeError(w, r, http.StatusInternalServerError, "internal_error",
					"an unexpected error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLogger emits one structured line per request, skipping probes.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/-/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush lets streaming handlers (job logs over SSE) work through the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var version = "dev"

func SetVersion(v string) {
	if v != "" {
		version = v
	}
}
