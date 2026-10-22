//go:build wireinject

package main

import (
	"github.com/google/wire"

	"github.com/A2MaTex/a2matex-runtime/internal/app"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/logging"
	httptransport "github.com/A2MaTex/a2matex-runtime/internal/transport/http"
)

//go:generate wire

func initializeApp(cfg config.Config, info buildinfo.Info) (*app.App, error) {
	wire.Build(
		logging.New,
		httptransport.NewServer,
		app.New,
	)

	return nil, nil
}
