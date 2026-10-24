package httptransport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

type Server struct {
	httpServer  *http.Server
	serveResult chan error
}

func NewServer(cfg config.Config, info buildinfo.Info, logger *slog.Logger) *Server {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(recoveryMiddleware(logger), requestLoggingMiddleware(logger))

	api := router.Group("/api/v1")
	api.GET("/health", healthHandler)
	api.GET("/runtime-info", runtimeInfoHandler(info))

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.HTTP.ListenAddress,
			Handler:           router,
			ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
			ReadTimeout:       cfg.HTTP.ReadTimeout,
			WriteTimeout:      cfg.HTTP.WriteTimeout,
			IdleTimeout:       cfg.HTTP.IdleTimeout,
		},
		serveResult: make(chan error, 1),
	}
}

func (s *Server) Start(ctx context.Context) error {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.httpServer.Addr, err)
	}

	go func() {
		err := s.httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.serveResult <- err
	}()

	return nil
}

func (s *Server) Wait() error {
	return <-s.serveResult
}

func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.httpServer.Shutdown(ctx); err != nil {
		shutdownErr := fmt.Errorf("graceful shutdown: %w", err)
		if closeErr := s.httpServer.Close(); closeErr != nil {
			return errors.Join(shutdownErr, fmt.Errorf("force close: %w", closeErr))
		}

		return shutdownErr
	}

	return nil
}

func healthHandler(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func runtimeInfoHandler(info buildinfo.Info) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{
			"name":            info.Name,
			"version":         info.Version,
			"commit":          info.Commit,
			"buildTime":       info.BuildTime,
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
