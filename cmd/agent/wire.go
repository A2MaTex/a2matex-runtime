//go:build wireinject

package main

import (
	"github.com/google/wire"

	"github.com/A2MaTex/a2matex-runtime/internal/api"
	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/api/workflowvalidation"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/discovery"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/logging"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/httpserver"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/supervisor"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
)

//go:generate wire

func initializeApp(cfg config.Config, info buildinfo.Info) (*supervisor.App, error) {
	wire.Build(
		logging.New,
		auth.NewSession,
		discovery.NewPublisher,
		nodecontract.NewDefaultRegistry,
		validationservice.New,
		workflowvalidation.NewHandler,
		api.NewHandler,
		wire.Bind(new(middleware.AuthorizationVerifier), new(*auth.Session)),
		httpserver.New,
		sqlitestore.New,
		repositories.NewExecutionRepository,
		wire.Bind(new(execution.RecoveryRepository), new(*repositories.ExecutionRepository)),
		services.NewExecutionRecovery,
		wire.Bind(new(execution.EventRetentionRepository), new(*repositories.ExecutionRepository)),
		services.NewEventRetention,
		wire.Bind(new(execution.CheckpointRepository), new(*repositories.ExecutionRepository)),
		services.NewCheckpointService,
		supervisor.New,
	)

	return nil, nil
}
