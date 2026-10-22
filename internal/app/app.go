package app

import (
	"context"
	"fmt"
	"log/slog"

	httptransport "github.com/A2MaTex/a2matex-runtime/internal/transport/http"
)

type App struct {
	server *httptransport.Server
	logger *slog.Logger
}

func New(server *httptransport.Server, logger *slog.Logger) *App {
	return &App{
		server: server,
		logger: logger,
	}
}

func (a *App) Run(ctx context.Context) error {
	a.logger.Info("starting runtime agent", "address", a.server.Address())

	if err := a.server.Run(ctx); err != nil {
		return fmt.Errorf("run local HTTP server: %w", err)
	}

	a.logger.Info("runtime agent stopped")

	return nil
}
