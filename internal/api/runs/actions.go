package runs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Controller submits actions; the lifecycle owns their asynchronous completion.
type Controller interface {
	Pause(context.Context, string) (constants.GroupControlDisposition, error)
	Resume(context.Context, string) (constants.GroupControlDisposition, error)
	Cancel(context.Context, string) (constants.GroupControlDisposition, error)
}

func (h *Handler) Action(ctx *gin.Context) {
	id := ctx.Param("runGroupId")
	if !execution.ValidID(id) {
		actionInvalid(ctx)
		return
	}
	request, ok := readAction(ctx)
	if !ok {
		return
	}
	if h == nil || h.controller == nil || h.reader == nil {
		actionError(ctx, xerror.ErrExecutionLifecycleInvalid)
		return
	}
	var disposition constants.GroupControlDisposition
	var err error
	switch request.Action {
	case constants.RunActionPause:
		disposition, err = h.controller.Pause(ctx.Request.Context(), id)
	case constants.RunActionResume:
		disposition, err = h.controller.Resume(ctx.Request.Context(), id)
	case constants.RunActionCancel:
		disposition, err = h.controller.Cancel(ctx.Request.Context(), id)
	}
	conflict := errors.Is(err, xerror.ErrExecutionControlState)
	if err != nil && !conflict {
		actionError(ctx, err)
		return
	}
	if !conflict && disposition != constants.GroupControlAccepted && disposition != constants.GroupControlAlreadyTarget {
		actionError(ctx, xerror.ErrExecutionLifecycleInvalid)
		return
	}
	snapshot, readErr := h.reader.SnapshotByID(ctx.Request.Context(), id)
	if readErr != nil {
		if !conflict && errors.Is(readErr, xerror.ErrExecutionNotFound) {
			actionError(ctx, xerror.WithOperation("action_target_missing_after_submission", xerror.ErrExecutionLifecycleInvalid))
		} else {
			respond(ctx, SnapshotResponse{}, readErr, "read_action_snapshot")
		}
		return
	}
	if snapshot.RunGroup.ID != id {
		actionError(ctx, xerror.WithOperation("action_snapshot_identity_mismatch", xerror.ErrExecutionLifecycleInvalid))
		return
	}
	if conflict {
		if request.Action == constants.RunActionCancel && snapshot.RunGroup.Status == constants.ExecutionCancelled {
			ctx.JSON(http.StatusOK, snapshot)
			return
		}
		failure := apiresponse.Error{Code: constants.APIErrorCodeInvalidExecutionState, Message: constants.APIErrorMessageInvalidExecutionState}
		apiresponse.SetFailure(ctx, failure, xerror.WithOperation("apply_run_action", err))
		ctx.AbortWithStatusJSON(http.StatusConflict, ActionConflictResponse{Error: failure, Snapshot: snapshot})
		return
	}
	status := http.StatusAccepted
	if disposition == constants.GroupControlAlreadyTarget {
		status = http.StatusOK
	}
	ctx.JSON(status, snapshot)
}

func readAction(ctx *gin.Context) (ActionRequest, bool) {
	r := ctx.Request
	if !validJSONRequest(r) {
		actionInvalid(ctx)
		return ActionRequest{}, false
	}
	if r.ContentLength > constants.RunActionMaxContentBytes {
		startPayloadTooLarge(ctx)
		return ActionRequest{}, false
	}
	r.Body = http.MaxBytesReader(ctx.Writer, r.Body, constants.RunActionMaxContentBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			startPayloadTooLarge(ctx)
		} else {
			apiresponse.AbortWithCause(ctx, http.StatusBadRequest,
				apiresponse.Error{Code: constants.APIErrorCodeInvalidRequest, Message: constants.APIErrorMessageInvalidRequest},
				xerror.WithOperation("read_run_action", err))
		}
		return ActionRequest{}, false
	}
	var request ActionRequest
	if utf8.Valid(body) && json.Valid(body) && uniqueKeys(body) {
		fields, ok := objectFields(body, "action")
		if ok && stringField(fields["action"], &request.Action) {
			switch request.Action {
			case constants.RunActionPause, constants.RunActionResume, constants.RunActionCancel:
				return request, true
			}
		}
	}
	actionInvalid(ctx)
	return ActionRequest{}, false
}

func actionInvalid(ctx *gin.Context) {
	apiresponse.Abort(ctx, http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest)
}

func actionError(ctx *gin.Context, cause error) {
	status := http.StatusInternalServerError
	failure := apiresponse.Error{Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal}
	switch {
	case errors.Is(cause, xerror.ErrExecutionStopping):
		status, failure.Message = http.StatusServiceUnavailable, constants.APIErrorMessageRuntimeStopping
	case errors.Is(cause, xerror.ErrExecutionPersistence), errors.Is(cause, xerror.ErrExecutionCommitUnknown), errors.Is(cause, xerror.ErrRecoveryIntegrity):
		failure.Code, failure.Message = constants.PersistenceFailed, constants.APIErrorMessageActionFailed
	}
	apiresponse.AbortWithCause(ctx, status, failure, xerror.WithOperation("apply_run_action", cause))
}
