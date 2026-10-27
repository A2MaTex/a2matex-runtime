package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// Recovery converts panics into the shared API error envelope.
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		defer func() {
			if recover() == nil {
				return
			}

			logger.ErrorContext(ctx.Request.Context(), "recovered local API panic",
				"component", "local_http_server",
				"event", "local_api_panic_recovered",
			)
			apiresponse.Abort(
				ctx,
				http.StatusInternalServerError,
				constants.APIErrorCodeInternal,
				constants.APIErrorMessageInternal,
			)
		}()

		ctx.Next()
	}
}
