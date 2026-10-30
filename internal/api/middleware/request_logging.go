package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogging records request metadata without headers or payloads.
func RequestLogging(logger *slog.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		startedAt := time.Now()
		ctx.Next()

		route := ctx.FullPath()
		if route == "" {
			route = "unmatched"
		}

		status := ctx.Writer.Status()
		logger.Log(ctx.Request.Context(), requestLogLevel(ctx.Request.Method, route, status), "local API request completed",
			"component", "local_http_server",
			"event", "local_api_request_completed",
			"method", ctx.Request.Method,
			"route", route,
			"status", status,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	}
}

func requestLogLevel(method, route string, status int) slog.Level {
	switch {
	case status >= http.StatusBadRequest:
		return slog.LevelError
	case method == http.MethodGet && route == "/api/v1/health":
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}
