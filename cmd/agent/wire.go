//go:build wireinject

package main

import (
	"github.com/google/wire"

	"github.com/A2MaTex/a2matex-runtime/internal/api"
	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/discovery"
	"github.com/A2MaTex/a2matex-runtime/internal/logging"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/httpserver"
	"github.com/A2MaTex/a2matex-runtime/internal/supervisor"
)

//go:generate wire

func initializeApp(cfg config.Config, info buildinfo.Info) (*supervisor.App, error) {
	wire.Build(
		logging.New,
		auth.NewSession,
		discovery.NewPublisher,
		api.NewHandler,
		wire.Bind(new(middleware.AuthorizationVerifier), new(*auth.Session)),
		httpserver.New,
		supervisor.New,
	)

	return nil, nil
}
