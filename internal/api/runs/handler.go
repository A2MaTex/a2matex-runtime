package runs

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type SnapshotReader interface {
	ActiveSnapshot(context.Context) (execution.Snapshot, error)
	SnapshotByID(context.Context, string) (execution.Snapshot, error)
}

type Handler struct {
	reader     SnapshotReader
	starter    Starter
	controller Controller
}

func NewHandler(reader SnapshotReader, starter Starter, controller Controller) *Handler {
	return &Handler{reader: reader, starter: starter, controller: controller}
}

func (h *Handler) Active(ctx *gin.Context) {
	const operation = "read_active_run_snapshot"
	if !validRequest(ctx) || !h.available(ctx, operation) {
		return
	}
	snapshot, err := h.reader.ActiveSnapshot(ctx.Request.Context())
	respond(ctx, snapshot, err, operation)
}

func (h *Handler) ByID(ctx *gin.Context) {
	const operation = "read_run_snapshot"
	if !validRequest(ctx) {
		return
	}
	id := ctx.Param("runGroupId")
	if !execution.ValidID(id) {
		apiresponse.Abort(ctx, http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest)
		return
	}
	if !h.available(ctx, operation) {
		return
	}
	snapshot, err := h.reader.SnapshotByID(ctx.Request.Context(), id)
	respond(ctx, snapshot, err, operation)
}

func (h *Handler) available(ctx *gin.Context, operation string) bool {
	if h != nil && h.reader != nil {
		return true
	}
	respond(ctx, SnapshotResponse{}, errors.New("snapshot reader is unavailable"), operation)
	return false
}

func respond(ctx *gin.Context, snapshot SnapshotResponse, err error, operation string) {
	if err == nil {
		ctx.JSON(http.StatusOK, snapshot)
		return
	}
	status, failure := snapshotFailure(err)
	apiresponse.AbortWithCause(ctx, status, failure, xerror.WithOperation(operation, err))
}

func snapshotFailure(err error) (int, apiresponse.Error) {
	status := http.StatusInternalServerError
	failure := apiresponse.Error{Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal}
	switch {
	case errors.Is(err, xerror.ErrExecutionNotFound):
		status = http.StatusNotFound
		failure.Code, failure.Message = constants.APIErrorCodeResourceNotFound, constants.APIErrorMessageResourceNotFound
	case errors.Is(err, xerror.ErrExecutionPersistence), errors.Is(err, xerror.ErrExecutionCommitUnknown), errors.Is(err, xerror.ErrRecoveryIntegrity):
		failure.Code, failure.Message = constants.PersistenceFailed, constants.APIErrorMessageSnapshotReadFailed
	}
	return status, failure
}

func validRequest(ctx *gin.Context) bool {
	request := ctx.Request
	if request.URL.RawQuery == "" && request.ContentLength <= 0 {
		if request.Body == nil || request.Body == http.NoBody {
			return true
		}
		var firstByte [1]byte
		if _, err := io.ReadFull(request.Body, firstByte[:]); err == io.EOF {
			return true
		}
	}
	apiresponse.Abort(ctx, http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest)
	return false
}
