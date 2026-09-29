package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Server wraps http.Server with graceful-shutdown semantics tuned to this
// service's design goals: drain in-flight requests in <30s, log clean exit,
// never leak goroutines.
type Server struct {
	srv *http.Server
	log *slog.Logger
}

func NewServer(addr string, handler http.Handler, log *slog.Logger) *Server {
	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		log: log,
	}
}

// Start begins listening. Returns when ListenAndServe returns — usually because
// Shutdown was called (nil) or the listener failed (non-nil).
func (s *Server) Start() error {
	s.log.Info("http server listening", slog.String("addr", s.srv.Addr))
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown drains in-flight requests within the given timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
