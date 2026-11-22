package artifacts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Reader interface {
	Open(context.Context, string) (execution.Artifact, *os.File, error)
}

type Handler struct {
	reader Reader
	logger *slog.Logger
}

func NewHandler(reader Reader, logger *slog.Logger) *Handler {
	return &Handler{reader: reader, logger: logger}
}

func (h *Handler) ByID(ctx *gin.Context) {
	started := time.Now()
	id := ctx.Param("artifactId")
	if !execution.ValidID(id) || !validRequest(ctx.Request) {
		apiresponse.Abort(ctx, http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest)
		return
	}
	if h == nil || h.reader == nil || h.logger == nil {
		fail(ctx, errors.New("artifact reader is unavailable"))
		return
	}
	if ctx.Request.Context().Err() != nil {
		h.abortTransfer(ctx, started)
	}
	artifact, file, err := h.reader.Open(ctx.Request.Context(), id)
	failed := true
	if file != nil {
		defer func() {
			if err := file.Close(); err != nil && !failed {
				h.logger.ErrorContext(ctx.Request.Context(), "artifact handle close failed",
					"component", "local_http_server", "event", "artifact_handle_close_failed",
					"operation", "close_artifact_handle", "reason", "artifact_close_failed",
					"error_code", constants.InternalError)
			}
		}()
	}
	if ctx.Request.Context().Err() != nil {
		h.abortTransfer(ctx, started)
	}
	if err != nil {
		fail(ctx, err)
		return
	}
	if file == nil || artifact.ID != id || !artifact.Valid() {
		fail(ctx, errors.New("artifact reader returned invalid metadata or handle"))
		return
	}
	ctx.Header("Content-Type", constants.ScreenshotMediaType)
	ctx.Header("Content-Length", strconv.FormatInt(artifact.ByteLength, 10))
	ctx.Status(http.StatusOK)
	_, err = io.CopyN(ctx.Writer, requestReader{ctx: ctx.Request.Context(), source: file}, artifact.ByteLength)
	if err != nil {
		if ctx.Writer.Written() || ctx.Request.Context().Err() != nil {
			h.abortTransfer(ctx, started)
		}
		ctx.Writer.Header().Del("Content-Type")
		ctx.Writer.Header().Del("Content-Length")
		fail(ctx, &xerror.OperationError{Operation: "stream_artifact", Detail: "artifact_stream_read_failed",
			Err: errors.Join(xerror.ErrExecutionPersistence, err)})
		return
	}
	failed = false
}

// abortTransfer never appends JSON or pretends an already-started status changed.
func (h *Handler) abortTransfer(ctx *gin.Context, started time.Time) {
	status, reason := 0, "artifact_transfer_incomplete"
	if ctx.Writer.Written() {
		status = ctx.Writer.Status()
	}
	if ctx.Request.Context().Err() != nil {
		reason = "artifact_transfer_cancelled"
	}
	h.logger.ErrorContext(ctx.Request.Context(), "artifact transfer aborted",
		"component", "local_http_server", "event", "artifact_transfer_aborted",
		"method", ctx.Request.Method, "route", ctx.FullPath(), "status", status,
		"duration_ms", time.Since(started).Milliseconds(), "operation", "stream_artifact",
		"reason", reason, "error_code", constants.InternalError)
	panic(http.ErrAbortHandler)
}

func fail(ctx *gin.Context, err error) {
	status := http.StatusInternalServerError
	failure := apiresponse.Error{Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal}
	switch {
	case errors.Is(err, xerror.ErrExecutionNotFound):
		status = http.StatusNotFound
		failure.Code, failure.Message = constants.APIErrorCodeResourceNotFound, constants.APIErrorMessageResourceNotFound
	case errors.Is(err, xerror.ErrExecutionPersistence), errors.Is(err, xerror.ErrRecoveryIntegrity):
		failure.Code, failure.Message = constants.PersistenceFailed, constants.APIErrorMessageArtifactReadFailed
	}
	apiresponse.AbortWithCause(ctx, status, failure, xerror.WithOperation("read_artifact", err))
}

func validRequest(request *http.Request) bool {
	if request.URL.RawQuery != "" || request.ContentLength > 0 {
		return false
	}
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	var first [1]byte
	_, err := io.ReadFull(request.Body, first[:])
	return err == io.EOF
}

// A non-embedded source keeps io.CopyN from bypassing cancellation via File.WriteTo.
type requestReader struct {
	ctx    context.Context
	source io.Reader
}

func (r requestReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}
