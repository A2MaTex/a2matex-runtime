package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/api/artifacts"
	"github.com/A2MaTex/a2matex-runtime/internal/api/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/api/devices"
	"github.com/A2MaTex/a2matex-runtime/internal/api/events"
	"github.com/A2MaTex/a2matex-runtime/internal/api/foundation"
	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/api/productpackages"
	"github.com/A2MaTex/a2matex-runtime/internal/api/productruns"
	"github.com/A2MaTex/a2matex-runtime/internal/api/productsessions"
	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/api/workflowvalidation"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
)

// NewHandler assembles the Runtime HTTP API independently of listener
// lifecycle concerns.
func NewHandler(
	info buildinfo.Info,
	verifier middleware.AuthorizationVerifier,
	workflowValidation *workflowvalidation.Handler,
	deviceInventory *devices.Handler,
	runSnapshots *runs.Handler,
	attemptHistory *attempts.Handler,
	artifactDownloads *artifacts.Handler,
	eventStream *events.Handler,
	handlers *registry.Registry,
	logger *slog.Logger,
	productPackages *productpackages.Handler,
	productSessions *productsessions.Handler,
	productRuns *productruns.Handler,
) http.Handler {
	gin.SetMode(gin.ReleaseMode)

	return newRouter(info, verifier, workflowValidation, deviceInventory, runSnapshots, attemptHistory, artifactDownloads, eventStream, handlers.SupportedNodes(), logger, productPackages, productSessions, productRuns)
}

func newRouter(
	info buildinfo.Info,
	verifier middleware.AuthorizationVerifier,
	workflowValidation *workflowvalidation.Handler,
	deviceInventory *devices.Handler,
	runSnapshots *runs.Handler,
	attemptHistory *attempts.Handler,
	artifactDownloads *artifacts.Handler,
	eventStream *events.Handler,
	supported []registry.SupportedNode,
	logger *slog.Logger,
	productPackages *productpackages.Handler,
	productSessions *productsessions.Handler,
	productRuns *productruns.Handler,
) *gin.Engine {
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.Use(
		middleware.Recovery(logger),
		middleware.RequestLogging(logger),
		middleware.CacheControl(),
		middleware.Authentication(verifier),
	)

	v1 := router.Group("/api/v1")
	{
		v1.GET("/health", foundation.Health)
		v1.GET("/runtime-info", foundation.RuntimeInfo(info, supported))
		v1.POST("/workflow-validation", workflowValidation.Validate)
		v1.POST("/workflow-compilation", workflowValidation.Compile)
		v1.GET("/devices", deviceInventory.Snapshot)
		v1.POST("/devices/refresh", deviceInventory.Refresh)
		v1.POST("/runs", runSnapshots.Start)
		v1.GET("/runs/active", runSnapshots.Active)
		v1.GET("/runs/:runGroupId", runSnapshots.ByID)
		v1.POST("/runs/:runGroupId/actions", runSnapshots.Action)
		v1.GET("/device-executions/:deviceExecutionId/attempts", attemptHistory.ByDeviceExecution)
		v1.GET("/artifacts/:artifactId", artifactDownloads.ByID)
		v1.GET("/events", eventStream.Stream)
		v1.POST("/product-packages/input-schema", productPackages.InputSchema)
		v1.POST("/product-sessions", productSessions.Prepare)
		v1.POST("/product-runs", productRuns.Start)
		v1.GET("/product-runs/active", productRuns.Active)
		v1.GET("/product-runs/:runGroupId", productRuns.ByID)
		v1.POST("/product-runs/:runGroupId/actions", productRuns.Action)
		router.NoRoute(foundation.NotFound)
	}

	return router
}
