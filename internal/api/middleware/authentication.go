package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// AuthorizationVerifier validates the strict bearer header used by the local API.
type AuthorizationVerifier interface {
	VerifyAuthorization(values []string) bool
}

// Authentication protects every request except the exact health endpoint.
func Authentication(verifier AuthorizationVerifier) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if isHealthRequest(ctx.Request) {
			ctx.Next()
			return
		}

		if verifier.VerifyAuthorization(ctx.Request.Header.Values("Authorization")) {
			ctx.Next()
			return
		}

		apiresponse.Abort(
			ctx,
			http.StatusUnauthorized,
			constants.APIErrorCodeUnauthorized,
			constants.APIErrorMessageUnauthorized,
		)
	}
}

func isHealthRequest(request *http.Request) bool {
	return request.Method == http.MethodGet && request.URL.Path == "/api/v1/health"
}
