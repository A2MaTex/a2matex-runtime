package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
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
		attrs := []slog.Attr{
			slog.String("component", "local_http_server"),
			slog.String("event", "local_api_request_completed"),
			slog.String("method", ctx.Request.Method),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
		}
		if status >= http.StatusBadRequest {
			failure := apiresponse.RequestFailure(ctx)
			if failure.Code != "" {
				attrs = append(attrs, slog.String("error_code", failure.Code))
			}
			if failure.Operation != "" {
				attrs = append(attrs, slog.String("operation", failure.Operation))
			}
			if failure.Reason != "" {
				attrs = append(attrs, slog.String("reason", failure.Reason))
			}
		}
		logger.LogAttrs(ctx.Request.Context(), requestLogLevel(ctx.Request.Method, route, status), "local API request completed", attrs...)
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
