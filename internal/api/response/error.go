package response

import "github.com/gin-gonic/gin"

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

// Abort stops the Gin chain and writes the Runtime API error envelope.
func Abort(ctx *gin.Context, status int, code, message string) {
	ctx.AbortWithStatusJSON(status, errorResponse{
		Error: apiError{
			Code:      code,
			Message:   message,
			Retryable: false,
		},
	})
}
