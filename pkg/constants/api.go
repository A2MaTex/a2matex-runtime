package constants

const (
	LocalAPIBasePath        = "/api/v1"
	LocalAPIHealthPath      = LocalAPIBasePath + "/health"
	LocalAPIRuntimeInfoPath = LocalAPIBasePath + "/runtime-info"

	RuntimeIRFormatVersion = 1
	RuntimeHealthStatusUp  = "UP"
	CacheControlNoStore    = "no-store"

	APIErrorCodeInvalidRequest   = "INVALID_REQUEST"
	APIErrorCodeUnauthorized     = "UNAUTHORIZED"
	APIErrorCodeResourceNotFound = "RESOURCE_NOT_FOUND"
	APIErrorCodeInternal         = "INTERNAL_ERROR"

	APIErrorMessageInvalidRequest   = "request is invalid"
	APIErrorMessageUnauthorized     = "local session token is missing or invalid"
	APIErrorMessageResourceNotFound = "resource was not found"
	APIErrorMessageInternal         = "internal server error"
)
