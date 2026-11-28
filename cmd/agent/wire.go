//go:build wireinject

package main

import (
	"github.com/google/wire"

	"github.com/A2MaTex/a2matex-runtime/internal/api"
	"github.com/A2MaTex/a2matex-runtime/internal/api/artifacts"
	"github.com/A2MaTex/a2matex-runtime/internal/api/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/api/devices"
	"github.com/A2MaTex/a2matex-runtime/internal/api/events"
	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/api/productpackages"
	"github.com/A2MaTex/a2matex-runtime/internal/api/productruns"
	"github.com/A2MaTex/a2matex-runtime/internal/api/productsessions"
	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/api/workflowvalidation"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/discovery"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/logging"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/httpserver"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/eventdelivery"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productrun"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/supervisor"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
)

//go:generate wire

func initializeAppWithOwner(cfg config.Config, info buildinfo.Info, owner *discovery.Owner) (*supervisor.App, error) {
	wire.Build(
		logging.New,
		auth.NewSession,
		discovery.NewOwnerPublisher,
		nodecontract.NewDefaultRegistry,
		newHandlerRegistry,
		validationservice.New,
		workflowvalidation.NewHandler,
		wire.FieldsOf(new(buildinfo.Info), "Version"),
		packagevalidation.New,
		wire.Bind(new(productpackages.Inspector), new(*packagevalidation.Verifier)),
		productpackages.NewHandler,
		newProductLeaseVerifier,
		newProductSessions,
		newProductRun,
		wire.Bind(new(productruns.Service), new(*productrun.Service)),
		productruns.NewHandler,
		wire.Bind(new(productsessions.Preparer), new(*productsession.Service)),
		productsessions.NewHandler,
		wire.FieldsOf(new(config.Config), "ADB"),
		adb.NewResolver,
		adb.NewDispatcher,
		wire.Bind(new(services.DeviceInventorySource), new(*adb.Resolver)),
		services.NewDeviceInventory,
		services.NewDevicePreflight,
		wire.Bind(new(devices.Inventory), new(*services.DeviceInventory)),
		devices.NewHandler,
		api.NewHandler,
		wire.Bind(new(middleware.AuthorizationVerifier), new(*auth.Session)),
		httpserver.New,
		sqlitestore.New,
		repositories.NewExecutionRepository,
		wire.Bind(new(runs.SnapshotReader), new(*repositories.ExecutionRepository)),
		wire.Bind(new(attempts.Reader), new(*repositories.ExecutionRepository)),
		attempts.NewHandler,
		wire.Bind(new(runs.Controller), new(*executionlifecycle.Service)),
		runs.NewHandler,
		newRunAdmission,
		wire.Bind(new(runadmission.Admitter), new(*runadmission.Service)),
		wire.Bind(new(runadmission.SnapshotReader), new(*repositories.ExecutionRepository)),
		wire.Bind(new(runadmission.ExecutionOwner), new(*executionlifecycle.Service)),
		runadmission.NewStarter,
		wire.Bind(new(runs.Starter), new(*runadmission.StartService)),
		wire.Bind(new(execution.RecoveryRepository), new(*repositories.ExecutionRepository)),
		services.NewExecutionRecovery,
		wire.Bind(new(execution.ArtifactRepository), new(*repositories.ExecutionRepository)),
		services.NewArtifactService,
		wire.Bind(new(artifacts.Reader), new(*services.ArtifactService)),
		artifacts.NewHandler,
		wire.Bind(new(execution.EventReader), new(*repositories.ExecutionRepository)),
		eventdelivery.New,
		events.NewHandler,
		wire.Bind(new(services.ArtifactDiscarder), new(*services.ArtifactService)),
		wire.Bind(new(execution.EventRetentionRepository), new(*repositories.ExecutionRepository)),
		services.NewEventRetention,
		wire.Bind(new(execution.CheckpointRepository), new(*repositories.ExecutionRepository)),
		services.NewCheckpointService,
		executionlifecycle.New,
		supervisor.New,
	)

	return nil, nil
}
