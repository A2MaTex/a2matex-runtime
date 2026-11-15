package devices

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Inventory interface {
	Snapshot() []adb.Readiness
	Refresh(context.Context) ([]adb.Readiness, error)
}

type Handler struct {
	inventory Inventory
}

func NewHandler(inventory Inventory) *Handler {
	return &Handler{inventory: inventory}
}

func (h *Handler) Snapshot(ctx *gin.Context) {
	if validRequest(ctx) {
		ctx.JSON(http.StatusOK, newInventoryResponse(h.inventory.Snapshot()))
	}
}

func (h *Handler) Refresh(ctx *gin.Context) {
	if !validRequest(ctx) {
		return
	}
	devices, err := h.inventory.Refresh(ctx.Request.Context())
	switch {
	case err == nil:
		ctx.JSON(http.StatusOK, newInventoryResponse(devices))
	case errors.Is(err, xerror.ErrADBDiscovery), errors.Is(err, xerror.ErrADBReadiness),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		apiresponse.AbortWithError(ctx, http.StatusServiceUnavailable, apiresponse.Error{
			Code:      constants.APIErrorCodeADBUnavailable,
			Message:   constants.APIErrorMessageADBUnavailable,
			Retryable: true,
		})
	default:
		apiresponse.Abort(ctx, http.StatusInternalServerError, constants.APIErrorCodeInternal, constants.APIErrorMessageInternal)
	}
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
