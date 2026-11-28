package main

import (
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services/executionlifecycle"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productrun"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"log/slog"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
)

func newProductLeaseVerifier(cfg config.Config) (*executionlease.Verifier, error) {
	if !cfg.Production.AuthorizationEnabled {
		return nil, nil
	}
	return executionlease.New(executionlease.Options{
		VerificationKeys: cfg.Production.VerificationKeys, ClockSkew: cfg.Production.ClockSkew,
	})
}

func newProductSessions(packages *packagevalidation.Verifier, logger *slog.Logger) (*productsession.Service, error) {
	return productsession.New(packages, nil, logger)
}

func newProductRun(catalog *nodecontract.Registry, handlers *registry.Registry, info buildinfo.Info, sessions *productsession.Service, verifier *executionlease.Verifier, admission *runadmission.Service, repository *repositories.ExecutionRepository, owner *executionlifecycle.Service) (*productrun.Service, error) {
	validator, err := runtimeir.New(catalog, handlers, info.Version)
	if err != nil {
		return nil, err
	}
	loader, err := productworkflow.New(validator)
	if err != nil {
		return nil, err
	}
	return productrun.New(sessions, verifier, loader, admission, repository, owner), nil
}
