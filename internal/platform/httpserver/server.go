package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var ErrAddressInUse = errors.New("local HTTP address already in use")

// Server owns only the network listener and HTTP server lifecycle.
type Server struct {
	httpServer  *http.Server
	serveResult chan error
}

// New creates the lifecycle wrapper around the already-assembled API handler.
func New(cfg config.Config, handler http.Handler) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.HTTP.ListenAddress,
			Handler:           handler,
			ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
			ReadTimeout:       cfg.HTTP.ReadTimeout,
			WriteTimeout:      cfg.HTTP.WriteTimeout,
			IdleTimeout:       cfg.HTTP.IdleTimeout,
		},
		serveResult: make(chan error, 1),
	}
}

func (s *Server) Start(ctx context.Context) error {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", s.httpServer.Addr)
	if err != nil {
		if errors.Is(err, addressInUse) {
			err = errors.Join(ErrAddressInUse, err)
		}
		return xerror.WithOperation("bind_loopback_listener", fmt.Errorf("listen on %s: %w", s.httpServer.Addr, err))
	}

	go func() {
		err := s.httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.serveResult <- xerror.WithOperation("serve_local_http", err)
	}()

	return nil
}

func (s *Server) Wait() error {
	return <-s.serveResult
}

func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.httpServer.Shutdown(ctx); err != nil {
		shutdownErr := xerror.WithOperation("shutdown_local_http", fmt.Errorf("graceful shutdown: %w", err))
		if closeErr := s.httpServer.Close(); closeErr != nil {
			return errors.Join(shutdownErr, xerror.WithOperation("force_close_local_http", closeErr))
		}

		return shutdownErr
	}

	return nil
}
