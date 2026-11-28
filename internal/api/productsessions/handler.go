package productsessions

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	reader "github.com/A2MaTex/a2matex-runtime/internal/platform/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Preparer interface {
	Prepare(context.Context, productsession.Request) (productsession.Prepared, error)
}

type Handler struct {
	preparer Preparer
	verifier *executionlease.Verifier
}

func NewHandler(preparer Preparer, verifier *executionlease.Verifier) (*Handler, error) {
	missing := preparer == nil
	if !missing {
		value := reflect.ValueOf(preparer)
		switch value.Kind() {
		case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
			missing = value.IsNil()
		}
	}
	if missing {
		return nil, &xerror.OperationError{Operation: "configure_product_sessions_api", Detail: "preparer_missing", Err: xerror.ErrProductSessionConfiguration}
	}
	return &Handler{preparer: preparer, verifier: verifier}, nil
}

func (h *Handler) Prepare(ctx *gin.Context) {
	request, ok := decodeRequest(ctx)
	if !ok {
		return
	}
	internal := productsession.Request{RequestID: request.RequestID, Purpose: request.Purpose,
		DirectoryHeaderValues: ctx.Request.Header.Values(constants.ProductPackageDirectoryHeader),
		PackageRef:            request.Package.PackageRef, PackageHash: request.Package.PackageHash, LauncherVersion: request.Package.LauncherVersion}
	if request.RunGroupID != nil {
		internal.RunGroupID = *request.RunGroupID
	}
	if err := validateSelection(internal); err != nil {
		prepareFailure(ctx, err)
		return
	}
	if h.verifier == nil {
		prepareFailure(ctx, &xerror.OperationError{Operation: "prepare_product_session", Detail: "authorization_disabled", Err: xerror.ErrProductPackageUnavailable})
		return
	}
	prepared, err := h.preparer.Prepare(ctx.Request.Context(), internal)
	if err == nil {
		err = ctx.Request.Context().Err()
	}
	if err != nil {
		prepareFailure(ctx, err)
		return
	}
	if prepared.RequestID != request.RequestID || prepared.Purpose != request.Purpose || !execution.ValidID(prepared.ExecutionID) ||
		!execution.ValidID(prepared.RuntimeSessionID) || prepared.ExecutionID == prepared.RuntimeSessionID ||
		prepared.RuntimePublicKey == "" || prepared.RuntimeKeyThumbprint == "" || prepared.ExpiresAt == "" ||
		prepared.Package.PackageHash != internal.PackageHash {
		prepareFailure(ctx, xerror.ErrProductSessionConfiguration)
		return
	}
	status := http.StatusCreated
	if prepared.Replayed {
		status = http.StatusOK
	}
	ctx.JSON(status, PreparedSessionResponse(prepared))
}

func validateSelection(r productsession.Request) error {
	_, hashErr := hex.DecodeString(r.PackageHash)
	if !execution.ValidID(r.RequestID) || !ir.ValidVersion(r.LauncherVersion) || hashErr != nil ||
		len(r.PackageHash) != constants.ExecutionLeaseHashBytes || strings.ToLower(r.PackageHash) != r.PackageHash ||
		(r.Purpose != constants.ProductSessionStart && r.Purpose != constants.ProductSessionReauthorize) ||
		(r.Purpose == constants.ProductSessionReauthorize && !execution.ValidID(r.RunGroupID)) {
		return &xerror.OperationError{Operation: "decode_product_session_request", Detail: "selection_invalid", Err: xerror.ErrProductSessionInvalid}
	}
	parts := strings.SplitN(r.PackageRef, "/", 2)
	if len(parts) != 2 {
		return xerror.WithOperation("decode_product_session_request", xerror.ErrProductPackageLocation)
	}
	_, err := reader.ResolveLocation(reader.ReadRequest{DirectoryHeaderValues: r.DirectoryHeaderValues, PackageRef: r.PackageRef, ProductID: parts[0]})
	return xerror.WithOperation("decode_product_session_request", err)
}

func prepareFailure(ctx *gin.Context, cause error) {
	status, code, message := http.StatusInternalServerError, constants.APIErrorCodeInternal, constants.APIErrorMessageInternal
	switch {
	case errors.Is(cause, xerror.ErrProductSessionInvalid), errors.Is(cause, xerror.ErrProductPackageLocation), errors.Is(cause, xerror.ErrProductPackageSelection):
		status, code, message = http.StatusBadRequest, constants.APIErrorCodeInvalidRequest, constants.APIErrorMessageInvalidRequest
	case errors.Is(cause, xerror.ErrProductPackageNotFound), errors.Is(cause, xerror.ErrExecutionNotFound):
		status, code, message = http.StatusNotFound, constants.APIErrorCodeResourceNotFound, constants.APIErrorMessageResourceNotFound
	case errors.Is(cause, xerror.ErrProductPackageInvalid), errors.Is(cause, xerror.ErrProductPackageIncompatible):
		status, code, message = http.StatusUnprocessableEntity, constants.ValidationFailed, constants.APIErrorMessageProductUnusable
	case errors.Is(cause, xerror.ErrProductSessionBusy):
		status, code, message = http.StatusConflict, constants.APIErrorCodeRuntimeBusy, constants.APIErrorMessageProductSessionBusy
	case errors.Is(cause, xerror.ErrProductSessionConflict):
		status, code, message = http.StatusConflict, constants.APIErrorCodeIdempotencyConflict, constants.APIErrorMessageIdempotencyConflict
	case errors.Is(cause, xerror.ErrProductSessionUnavailable):
		status, code, message = http.StatusConflict, constants.APIErrorCodeAuthorizationSessionUnavailable, constants.APIErrorMessageAuthorizationSessionUnavailable
	case errors.Is(cause, xerror.ErrExecutionControlState):
		status, code, message = http.StatusConflict, constants.APIErrorCodeInvalidExecutionState, constants.APIErrorMessageProductExecutionState
	case errors.Is(cause, xerror.ErrProductPackageUnavailable), errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		status = http.StatusServiceUnavailable
	}
	apiresponse.AbortWithCause(ctx, status, apiresponse.Error{Code: code, Message: message}, xerror.WithOperation("prepare_product_session", cause))
}
