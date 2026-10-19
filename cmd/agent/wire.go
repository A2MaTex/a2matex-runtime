//go:build wireinject

package main

import (
	"github.com/google/wire"

	"a2matex-runtime/internal/app"
	"a2matex-runtime/internal/buildinfo"
	"a2matex-runtime/internal/config"
	"a2matex-runtime/internal/logging"
	httptransport "a2matex-runtime/internal/transport/http"
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
