package main

import (
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
)

func newRunAdmission(catalog *nodecontract.Registry, handlers *registry.Registry, info buildinfo.Info,
	preflight *services.DevicePreflight, repository *repositories.ExecutionRepository,
	lifecycle *executionlifecycle.Service, checkpoints *services.CheckpointService, artifacts *services.ArtifactService,
) (*runadmission.Service, error) {
	validator, err := runtimeir.New(catalog, handlers, info.Version)
	if err != nil {
		return nil, err
	}
	resolver, err := bindings.New(catalog)
	if err != nil {
		return nil, err
	}
	invoker, err := invocation.New(handlers)
	if err != nil {
		return nil, err
	}
	output, err := outputs.New(catalog)
	if err != nil {
		return nil, err
	}
	return runadmission.New(runadmission.Dependencies{Validator: validator, Preflight: preflight,
		Repository: repository, Lifecycle: lifecycle,
		Runner: runner.Dependencies{
			Bindings:    resolver,
			Invoker:     invoker,
			Outputs:     output,
			Checkpoints: checkpoints,
			Artifacts:   artifacts,
		}})
}
