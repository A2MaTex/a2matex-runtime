package constants

const (
	EditorGraphMaxContentBytes                  = 1_048_576
	LocalAPIWorkflowValidationEnvelopeAllowance = 4 * 1024
	LocalAPIWorkflowValidationBodyMaxBytes      = EditorGraphMaxContentBytes + LocalAPIWorkflowValidationEnvelopeAllowance

	LocalAPIWorkflowCompilationBodyMaxBytes = 2 * 1024 * 1024
	RuntimeIRMaxContentBytes                = 1024 * 1024
	RuntimeIRFormatVersion                  = 1
	CacheControlNoStore                     = "no-store"

	APIErrorCodeInvalidRequest   = "INVALID_REQUEST"
	APIErrorCodeUnauthorized     = "UNAUTHORIZED"
	APIErrorCodeResourceNotFound = "RESOURCE_NOT_FOUND"
	APIErrorCodePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
	APIErrorCodeInternal         = InternalError
	APIErrorCodeADBUnavailable   = ADBUnavailable

	APIErrorMessageInvalidRequest   = "request is invalid"
	APIErrorMessageUnauthorized     = "local session token is missing or invalid"
	APIErrorMessageResourceNotFound = "resource was not found"
	APIErrorMessagePayloadTooLarge  = "request payload is too large"
	APIErrorMessageInternal         = "internal server error"
	APIErrorMessageADBUnavailable   = "unable to refresh Android devices"

	APIDeviceStatusReady       = "READY"
	APIDeviceStatusUnavailable = "UNAVAILABLE"
)
