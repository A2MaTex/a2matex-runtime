package response

import (
	"errors"

	"github.com/gin-gonic/gin"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const failureKey = "a2matex.runtime.api.failure"

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type errorResponse struct {
	Error Error `json:"error"`
}

// Failure contains only the metadata that request logging can record.
type Failure struct {
	Code      string
	Operation string
	Reason    string
}

func RequestFailure(ctx *gin.Context) Failure {
	value, _ := ctx.Get(failureKey)
	failure, _ := value.(Failure)
	return failure
}

// Abort stops the Gin chain and writes the Runtime API error envelope.
func Abort(ctx *gin.Context, status int, code, message string) {
	AbortWithCause(ctx, status, Error{Code: code, Message: message}, nil)
}

// AbortWithCause keeps operational context separate from the public response.
func AbortWithCause(ctx *gin.Context, status int, err Error, cause error) {
	failure := Failure{Code: err.Code}
	var operation *xerror.OperationError
	if errors.As(cause, &operation) {
		failure.Operation = operation.Operation
		failure.Reason = operation.Detail
	}
	ctx.Set(failureKey, failure)
	ctx.AbortWithStatusJSON(status, errorResponse{Error: err})
}
