package response

import "github.com/gin-gonic/gin"

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type errorResponse struct {
	Error Error `json:"error"`
}

// Abort stops the Gin chain and writes the Runtime API error envelope.
func Abort(ctx *gin.Context, status int, code, message string) {
	AbortWithError(ctx, status, Error{Code: code, Message: message})
}

func AbortWithError(ctx *gin.Context, status int, err Error) {
	ctx.AbortWithStatusJSON(status, errorResponse{Error: err})
}
