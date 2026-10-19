package httptransport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	"a2matex-runtime/internal/buildinfo"
	"a2matex-runtime/internal/config"
)

type Server struct {
	httpServer      *http.Server
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

func NewServer(cfg config.Config, info buildinfo.Info, logger *slog.Logger) *Server {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(recoveryMiddleware(logger), requestLoggingMiddleware(logger))

	api := router.Group("/api/v1")
	api.GET("/health", healthHandler)
	api.GET("/runtime-info", runtimeInfoHandler(info, cfg.Mode))

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.HTTP.ListenAddress,
			Handler:           router,
			ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
			ReadTimeout:       cfg.HTTP.ReadTimeout,
			WriteTimeout:      cfg.HTTP.WriteTimeout,
			IdleTimeout:       cfg.HTTP.IdleTimeout,
		},
		shutdownTimeout: cfg.ShutdownTimeout,
		logger:          logger,
	}
}

func (s *Server) Address() string {
	return s.httpServer.Addr
}

func (s *Server) Run(ctx context.Context) error {
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- s.httpServer.ListenAndServe()
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}

	err := <-serveErrors
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

func healthHandler(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func runtimeInfoHandler(info buildinfo.Info, mode string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{
			"name":            info.Name,
			"version":         info.Version,
			"commit":          info.Commit,
			"buildTime":       info.BuildTime,
			"mode":            mode,
			"goVersion":       runtime.Version(),
			"operatingSystem": runtime.GOOS,
			"architecture":    runtime.GOARCH,
		})
	}
}

func requestLoggingMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		startedAt := time.Now()
		ctx.Next()

		logger.Info("local API request",
			"method", ctx.Request.Method,
			"path", ctx.Request.URL.Path,
			"status", ctx.Writer.Status(),
			"durationMs", time.Since(startedAt).Milliseconds(),
		)
	}
}

func recoveryMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(ctx *gin.Context, recovered any) {
		logger.Error("recovered local API panic", "panic", recovered)
		ctx.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "internal server error",
			},
		})
	})
}
