package runs

import (
	"context"
	"errors"
	"net/http"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/gin-gonic/gin"
)

type Starter interface {
	Start(context.Context, runadmission.Request) (runadmission.StartResult, error)
}

func (h *Handler) Start(ctx *gin.Context) {
	request, ok := readStart(ctx)
	if !ok {
		return
	}
	if h == nil || h.starter == nil {
		startError(ctx, xerror.ErrRunAdmissionContract)
		return
	}
	input := runadmission.Request{RequestID: request.RequestID, RuntimeIR: request.RuntimeIR,
		Devices: make([]runadmission.DeviceInput, len(request.Devices))}
	for i, device := range request.Devices {
		input.Devices[i] = runadmission.DeviceInput{ADBSerial: device.ADBSerial, Inputs: device.Inputs}
	}
	result, err := h.starter.Start(ctx.Request.Context(), input)
	if err != nil {
		startError(ctx, err)
		return
	}
	if result.Rejection != nil {
		failure := apiresponse.Error{Code: result.Rejection.FailureCode, Message: constants.APIErrorMessageAdmissionFailed,
			Retryable: result.Rejection.Validation == nil}
		apiresponse.SetFailure(ctx, failure, nil)
		response := AdmissionErrorResponse{Error: failure, Validation: result.Rejection.Validation}
		for _, issue := range result.Rejection.Unavailable {
			response.UnavailableDevices = append(response.UnavailableDevices, UnavailableDeviceResponse{
				Index: issue.Index, ADBSerial: issue.ADBSerial,
				Reason: apiresponse.Error{Code: issue.Reason.Code, Message: issue.Reason.Message, Retryable: true},
			})
		}
		ctx.AbortWithStatusJSON(http.StatusUnprocessableEntity, response)
		return
	}
	switch result.Disposition {
	case constants.AdmissionBusy:
		apiresponse.AbortWithCause(ctx, http.StatusConflict, apiresponse.Error{Code: constants.APIErrorCodeRuntimeBusy,
			Message: constants.APIErrorMessageRuntimeBusy, Retryable: true}, nil)
		return
	case constants.AdmissionCreated, constants.AdmissionExisting:
		if result.Snapshot != nil {
			status := http.StatusOK
			if result.Disposition == constants.AdmissionCreated {
				status = http.StatusCreated
			}
			ctx.JSON(status, result.Snapshot)
			return
		}
	}
	startError(ctx, xerror.ErrRunAdmissionContract)
}

func startPayloadTooLarge(ctx *gin.Context) {
	apiresponse.Abort(ctx, http.StatusRequestEntityTooLarge, constants.APIErrorCodePayloadTooLarge, constants.APIErrorMessagePayloadTooLarge)
}

func startError(ctx *gin.Context, cause error) {
	status := http.StatusInternalServerError
	failure := apiresponse.Error{Code: constants.APIErrorCodeInternal, Message: constants.APIErrorMessageInternal}
	var limit *http.MaxBytesError
	switch {
	case cause == nil, errors.Is(cause, xerror.ErrRunAdmissionInvalid):
		status = http.StatusBadRequest
		failure.Code, failure.Message = constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest
	case errors.As(cause, &limit), errors.Is(cause, xerror.ErrRuntimeIRPayloadTooLarge):
		startPayloadTooLarge(ctx)
		return
	case errors.Is(cause, xerror.ErrExecutionStopping):
		status, failure.Message = http.StatusServiceUnavailable, constants.APIErrorMessageRuntimeStopping
	case errors.Is(cause, xerror.ErrExecutionPersistence), errors.Is(cause, xerror.ErrExecutionCommitUnknown), errors.Is(cause, xerror.ErrRecoveryIntegrity):
		failure.Code, failure.Message = constants.PersistenceFailed, constants.APIErrorMessageAdmissionPersistenceFailed
	case errors.Is(cause, xerror.ErrADBDiscovery), errors.Is(cause, xerror.ErrADBReadiness):
		status = http.StatusServiceUnavailable
		failure = apiresponse.Error{Code: constants.ADBUnavailable, Message: constants.APIErrorMessageAdmissionADBUnavailable, Retryable: true}
	}
	apiresponse.AbortWithCause(ctx, status, failure, xerror.WithOperation("start_workflow_run", cause))
}
