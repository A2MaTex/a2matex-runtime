package attempts

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Reader interface {
	Attempts(context.Context, string, int64, int) (execution.AttemptPage, error)
}

type Handler struct {
	reader Reader
}

func NewHandler(reader Reader) *Handler {
	return &Handler{reader: reader}
}

func (h *Handler) ByDeviceExecution(ctx *gin.Context) {
	id := ctx.Param("deviceExecutionId")
	query, ok := decodeRequest(id, ctx.Request)
	if !ok {
		apiresponse.AbortWithCause(ctx, http.StatusBadRequest, apiresponse.Error{
			Code: constants.APIErrorCodeInvalidRequest, Message: constants.APIErrorMessageInvalidRequest,
		}, &xerror.OperationError{Operation: "validate_attempt_history_request",
			Detail: "attempt_history_request_invalid", Err: xerror.ErrExecutionInvalidInput})
		return
	}
	if h == nil || h.reader == nil {
		fail(ctx, "configure_attempt_history_reader", xerror.ErrExecutionLifecycleInvalid)
		return
	}
	page, err := h.reader.Attempts(ctx.Request.Context(), id, query.AfterSequence, query.Limit)
	if err != nil {
		fail(ctx, "read_attempt_history", err)
		return
	}
	ctx.JSON(http.StatusOK, PageResponse(page))
}

func fail(ctx *gin.Context, operation string, cause error) {
	status := http.StatusInternalServerError
	failure := apiresponse.Error{Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal}
	switch {
	case errors.Is(cause, xerror.ErrExecutionNotFound):
		status = http.StatusNotFound
		failure.Code, failure.Message = constants.APIErrorCodeResourceNotFound, constants.APIErrorMessageResourceNotFound
	case errors.Is(cause, xerror.ErrExecutionPersistence), errors.Is(cause, xerror.ErrExecutionCommitUnknown), errors.Is(cause, xerror.ErrRecoveryIntegrity):
		failure.Code, failure.Message = constants.PersistenceFailed, constants.APIErrorMessageAttemptReadFailed
	}
	apiresponse.AbortWithCause(ctx, status, failure, xerror.WithOperation(operation, cause))
}
