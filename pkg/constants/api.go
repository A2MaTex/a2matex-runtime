package constants

const (
	EditorGraphMaxContentBytes                  = 1_048_576
	LocalAPIWorkflowValidationEnvelopeAllowance = 4 * 1024
	LocalAPIWorkflowValidationBodyMaxBytes      = EditorGraphMaxContentBytes + LocalAPIWorkflowValidationEnvelopeAllowance

	RuntimeIRFormatVersion = 1
	CacheControlNoStore    = "no-store"

	APIErrorCodeInvalidRequest   = "INVALID_REQUEST"
	APIErrorCodeUnauthorized     = "UNAUTHORIZED"
	APIErrorCodeResourceNotFound = "RESOURCE_NOT_FOUND"
	APIErrorCodePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
	APIErrorCodeInternal         = "INTERNAL_ERROR"

	APIErrorMessageInvalidRequest   = "request is invalid"
	APIErrorMessageUnauthorized     = "local session token is missing or invalid"
	APIErrorMessageResourceNotFound = "resource was not found"
	APIErrorMessagePayloadTooLarge  = "request payload is too large"
	APIErrorMessageInternal         = "internal server error"
)
