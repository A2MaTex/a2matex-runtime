package foundation

import (
	"net/http"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// Health handles the one unauthenticated liveness endpoint.
func Health(ctx *gin.Context) {
	if rejectQuery(ctx) {
		return
	}

	ctx.JSON(http.StatusOK, healthResponse{Status: "UP"})
}

// RuntimeInfo returns the Runtime capabilities currently available to clients.
func RuntimeInfo(info buildinfo.Info) gin.HandlerFunc {
	response := runtimeInfoResponse{
		RuntimeVersion:          info.Version,
		RuntimeIRFormatVersions: []int{constants.RuntimeIRFormatVersion},
		SupportedNodes:          []supportedNode{},
	}

	return func(ctx *gin.Context) {
		if rejectQuery(ctx) {
			return
		}

		ctx.JSON(http.StatusOK, response)
	}
}

// NotFound returns the shared API envelope for an authenticated unknown route.
func NotFound(ctx *gin.Context) {
	apiresponse.Abort(
		ctx,
		http.StatusNotFound,
		constants.APIErrorCodeResourceNotFound,
		constants.APIErrorMessageResourceNotFound,
	)
}

func rejectQuery(ctx *gin.Context) bool {
	if ctx.Request.URL.RawQuery == "" {
		return false
	}

	apiresponse.Abort(
		ctx,
		http.StatusBadRequest,
		constants.APIErrorCodeInvalidRequest,
		constants.APIErrorMessageInvalidRequest,
	)

	return true
}
