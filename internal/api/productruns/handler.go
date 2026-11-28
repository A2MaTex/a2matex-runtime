package productruns

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productrun"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
)

type Service interface {
	Start(context.Context, productrun.Request) (productrun.Result, error)
	ByID(context.Context, string) (productrun.Snapshot, error)
	Active(context.Context) (productrun.Snapshot, error)
	Action(context.Context, string, string) (productrun.Snapshot, constants.GroupControlDisposition, error)
}
type Handler struct{ service Service }

func NewHandler(service Service) *Handler { return &Handler{service: service} }
func (h *Handler) Start(ctx *gin.Context) {
	request, err := decodeStart(ctx)
	if err != nil {
		failure(ctx, err)
		return
	}
	if h == nil || h.service == nil {
		failure(ctx, xerror.ErrProductRunUnavailable)
		return
	}
	result, err := h.service.Start(ctx.Request.Context(), request)
	if err != nil {
		failure(ctx, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	ctx.JSON(status, result.Snapshot)
}
func (h *Handler) Active(ctx *gin.Context) {
	if err := readRequest(ctx); err != nil {
		failure(ctx, err)
		return
	}
	if h == nil || h.service == nil {
		failure(ctx, xerror.ErrProductRunUnavailable)
		return
	}
	snapshot, err := h.service.Active(ctx.Request.Context())
	if err != nil {
		failure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, snapshot)
}
func (h *Handler) ByID(ctx *gin.Context) {
	id := ctx.Param("runGroupId")
	if !execution.ValidID(id) {
		failure(ctx, xerror.ErrProductSessionInvalid)
		return
	}
	if err := readRequest(ctx); err != nil {
		failure(ctx, err)
		return
	}
	if h == nil || h.service == nil {
		failure(ctx, xerror.ErrProductRunUnavailable)
		return
	}
	snapshot, err := h.service.ByID(ctx.Request.Context(), id)
	if err != nil {
		failure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, snapshot)
}
func (h *Handler) Action(ctx *gin.Context) {
	id := ctx.Param("runGroupId")
	if !execution.ValidID(id) {
		failure(ctx, xerror.ErrProductSessionInvalid)
		return
	}
	if err := optionalDirectory(ctx); err != nil {
		failure(ctx, err)
		return
	}
	raw, err := readBody(ctx, constants.RunActionMaxContentBytes)
	if err != nil {
		failure(ctx, err)
		return
	}
	var r ActionRequest
	if !object(raw, "action") || json.Unmarshal(raw, &r) != nil || (r.Action != constants.RunActionPause && r.Action != constants.RunActionResume && r.Action != constants.RunActionCancel) {
		failure(ctx, xerror.ErrProductSessionInvalid)
		return
	}
	if h == nil || h.service == nil {
		failure(ctx, xerror.ErrProductRunUnavailable)
		return
	}
	snapshot, disposition, err := h.service.Action(ctx.Request.Context(), id, r.Action)
	if err != nil {
		if (errors.Is(err, xerror.ErrExecutionControlState) || errors.Is(err, xerror.ErrProductAuthorizationExpired)) && snapshot.RunGroup.ID == id {
			code := constants.APIErrorCodeInvalidExecutionState
			message := constants.APIErrorMessageInvalidExecutionState
			if errors.Is(err, xerror.ErrProductAuthorizationExpired) {
				code = constants.APIErrorCodeAuthorizationExpired
				message = constants.ProductAuthorizationExpiredMessage
			}
			public := apiresponse.Error{Code: code, Message: message}
			apiresponse.SetFailure(ctx, public, xerror.WithOperation("control_product_run", err))
			ctx.AbortWithStatusJSON(http.StatusConflict, struct {
				Error    apiresponse.Error   `json:"error"`
				Snapshot productrun.Snapshot `json:"snapshot"`
			}{public, snapshot})
			return
		}
		failure(ctx, err)
		return
	}
	status := http.StatusAccepted
	if disposition == constants.GroupControlAlreadyTarget {
		status = http.StatusOK
	}
	ctx.JSON(status, snapshot)
}
func failure(ctx *gin.Context, cause error) {
	status := http.StatusInternalServerError
	code, message := constants.APIErrorCodeInternal, constants.APIErrorMessageInternal
	var limit *http.MaxBytesError
	var device *productrun.DeviceRejection
	switch {
	case errors.As(cause, &limit), errors.Is(cause, xerror.ErrRuntimeIRPayloadTooLarge):
		status = http.StatusRequestEntityTooLarge
		code, message = constants.APIErrorCodePayloadTooLarge, constants.APIErrorMessagePayloadTooLarge
	case errors.Is(cause, xerror.ErrProductSessionInvalid), errors.Is(cause, xerror.ErrInvalidDeviceSelection):
		status = http.StatusBadRequest
		code, message = constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest
	case errors.Is(cause, xerror.ErrExecutionNotFound):
		status = http.StatusNotFound
		code, message = constants.APIErrorCodeResourceNotFound, constants.APIErrorMessageResourceNotFound
	case errors.Is(cause, xerror.ErrProductRunConflict), errors.Is(cause, xerror.ErrProductSessionConflict):
		status = http.StatusConflict
		code, message = constants.APIErrorCodeIdempotencyConflict, constants.APIErrorMessageIdempotencyConflict
	case errors.Is(cause, xerror.ErrProductSessionBusy):
		status = http.StatusConflict
		code, message = constants.APIErrorCodeRuntimeBusy, constants.APIErrorMessageRuntimeBusy
	case errors.Is(cause, xerror.ErrProductSessionUnavailable):
		status = http.StatusConflict
		code, message = constants.APIErrorCodeAuthorizationSessionUnavailable, constants.ProductSessionUnavailableMessage
	case errors.Is(cause, xerror.ErrExecutionLeaseInvalid), errors.Is(cause, xerror.ErrProductKeyEnvelopeInvalid):
		status = http.StatusUnprocessableEntity
		code, message = constants.APIErrorCodeAuthorizationInvalid, constants.ProductAuthorizationInvalidMessage
	case errors.Is(cause, xerror.ErrProductPackageInvalid):
		status = http.StatusUnprocessableEntity
		code, message = constants.ValidationFailed, constants.ProductPackageInvalidMessage
	case errors.As(cause, &device):
		status = http.StatusUnprocessableEntity
		public := productrun.SafeError(device.Code)
		code, message = public.Code, public.Message
	case errors.Is(cause, xerror.ErrADBDiscovery), errors.Is(cause, xerror.ErrADBReadiness):
		status = http.StatusServiceUnavailable
		code, message = constants.ADBUnavailable, constants.APIErrorMessageAdmissionADBUnavailable
	case errors.Is(cause, xerror.ErrProductRunUnavailable), errors.Is(cause, xerror.ErrExecutionStopping):
		status = http.StatusServiceUnavailable
	case errors.Is(cause, xerror.ErrExecutionPersistence), errors.Is(cause, xerror.ErrExecutionCommitUnknown), errors.Is(cause, xerror.ErrRecoveryIntegrity):
		code, message = constants.PersistenceFailed, constants.APIErrorMessageAdmissionPersistenceFailed
	}
	apiresponse.AbortWithCause(ctx, status, apiresponse.Error{Code: code, Message: message}, xerror.WithOperation("product_run_request", cause))
}
