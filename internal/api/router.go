package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/api/foundation"
	"github.com/A2MaTex/a2matex-runtime/internal/api/middleware"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// NewHandler assembles the Runtime HTTP API independently of listener
// lifecycle concerns.
func NewHandler(
	info buildinfo.Info,
	verifier middleware.AuthorizationVerifier,
	logger *slog.Logger,
) http.Handler {
	gin.SetMode(gin.ReleaseMode)

	return newRouter(info, verifier, logger)
}

func newRouter(
	info buildinfo.Info,
	verifier middleware.AuthorizationVerifier,
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

	router.GET(constants.LocalAPIHealthPath, foundation.Health)
	router.GET(constants.LocalAPIRuntimeInfoPath, foundation.RuntimeInfo(info))
	router.NoRoute(foundation.NotFound)

	return router
}
