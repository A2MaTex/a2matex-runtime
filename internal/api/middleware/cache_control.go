package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// CacheControl prevents clients from retaining protected API responses.
func CacheControl() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		// A health request with a query is an invalid API request rather than the
		// exact public liveness response, so its error must not be cached.
		if !isHealthRequest(ctx.Request) || ctx.Request.URL.RawQuery != "" {
			ctx.Header("Cache-Control", constants.CacheControlNoStore)
		}

		ctx.Next()
	}
}
