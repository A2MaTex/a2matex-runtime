package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/api/foundation"
	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/api/workflowvalidation"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
)

// NewHandler assembles the Runtime HTTP API independently of listener
// lifecycle concerns.
func NewHandler(
	info buildinfo.Info,
	verifier middleware.AuthorizationVerifier,
	workflowValidation *workflowvalidation.Handler,
	logger *slog.Logger,
) http.Handler {
	gin.SetMode(gin.ReleaseMode)

	return newRouter(info, verifier, workflowValidation, logger)
}

func newRouter(
	info buildinfo.Info,
	verifier middleware.AuthorizationVerifier,
	workflowValidation *workflowvalidation.Handler,
	logger *slog.Logger,
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
		v1.GET("/runtime-info", foundation.RuntimeInfo(info))
		v1.POST("/workflow-validation", workflowValidation.Validate)
		router.NoRoute(foundation.NotFound)
	}

	return router
}
