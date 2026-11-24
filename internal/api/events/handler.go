package events

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/services/eventdelivery"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Handler struct {
	delivery *eventdelivery.Service
	logger   *slog.Logger
}

func NewHandler(delivery *eventdelivery.Service, logger *slog.Logger) *Handler {
	return &Handler{delivery: delivery, logger: logger}
}

func (h *Handler) Stream(ctx *gin.Context) {
	request, ok := decodeRequest(ctx.Request)
	if !ok {
		fail(ctx, xerror.WithOperation("validate_event_stream_request", xerror.ErrExecutionInvalidInput))
		return
	}
	if h == nil || h.delivery == nil || h.logger == nil {
		fail(ctx, xerror.WithOperation("configure_event_stream", http.ErrNotSupported))
		return
	}
	session, err := h.delivery.Prepare(ctx.Request.Context(), request.After)
	if err != nil {
		if ctx.Request.Context().Err() != nil {
			panic(http.ErrAbortHandler)
		}
		fail(ctx, err)
		return
	}
	writer, err := newStreamWriter(ctx.Request.Context(), ctx.Writer)
	if err != nil {
		if ctx.Request.Context().Err() != nil {
			panic(http.ErrAbortHandler)
		}
		fail(ctx, err)
		return
	}
	started := time.Now()
	var cursor int64
	var streamErr error
	defer func() {
		cause := errors.Join(streamErr, writer.close())
		if ctx.Writer.Written() {
			h.logClose(ctx, started, cursor, cause)
		}
	}()
	if streamErr = ctx.Request.Context().Err(); streamErr != nil {
		panic(http.ErrAbortHandler)
	}
	ctx.Header("Content-Type", "text/event-stream; charset=utf-8")
	ctx.Header("Cache-Control", constants.CacheControlNoStore)
	ctx.Header("X-Accel-Buffering", "no")
	ctx.Status(http.StatusOK)
	ctx.Writer.WriteHeaderNow()
	if streamErr = writer.flush(ctx.Request.Context()); streamErr != nil {
		panic(http.ErrAbortHandler)
	}
	h.logger.InfoContext(ctx.Request.Context(), "execution event stream opened",
		"component", "local_http_server", "event", "execution_event_stream_opened", "route", ctx.FullPath())
	cursor, streamErr = session.Run(ctx.Request.Context(), writer)
	panic(http.ErrAbortHandler)
}

func fail(ctx *gin.Context, cause error) {
	status := http.StatusInternalServerError
	failure := apiresponse.Error{Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal}
	switch {
	case errors.Is(cause, xerror.ErrExecutionInvalidInput), errors.Is(cause, xerror.ErrEventCursorFuture):
		status = http.StatusBadRequest
		failure.Code, failure.Message = constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest
	case errors.Is(cause, xerror.ErrEventCursorExpired):
		status = http.StatusGone
		failure.Code, failure.Message = constants.APIErrorCodeEventCursorExpired, constants.APIErrorMessageEventCursorExpired
	case errors.Is(cause, xerror.ErrExecutionPersistence), errors.Is(cause, xerror.ErrExecutionCommitUnknown), errors.Is(cause, xerror.ErrRecoveryIntegrity):
		failure.Code, failure.Message = constants.PersistenceFailed, constants.APIErrorMessageEventReadFailed
	}
	apiresponse.AbortWithCause(ctx, status, failure, cause)
}

func (h *Handler) logClose(ctx *gin.Context, started time.Time, cursor int64, cause error) {
	level, outcome, reason, code := slog.LevelError, "failed", "event_stream_failed", constants.InternalError
	operation := "stream_execution_events"
	var failure *xerror.OperationError
	if errors.As(cause, &failure) {
		operation = failure.Operation
	}
	if ctx.Request.Context().Err() != nil {
		level, outcome, reason, code = slog.LevelDebug, "cancelled", "event_stream_cancelled", ""
	} else if errors.Is(cause, xerror.ErrEventCursorExpired) {
		code = constants.APIErrorCodeEventCursorExpired
	} else if errors.Is(cause, xerror.ErrExecutionPersistence) {
		code = constants.PersistenceFailed
	}
	attrs := []slog.Attr{
		slog.String("component", "local_http_server"), slog.String("event", "execution_event_stream_closed"),
		slog.String("method", ctx.Request.Method), slog.String("route", ctx.FullPath()),
		slog.Int("status", ctx.Writer.Status()), slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.String("cursor", strconv.FormatInt(cursor, 10)), slog.String("outcome", outcome),
		slog.String("operation", operation), slog.String("reason", reason),
	}
	if code != "" {
		attrs = append(attrs, slog.String("error_code", code))
	}
	h.logger.LogAttrs(ctx.Request.Context(), level, "execution event stream closed", attrs...)
}
