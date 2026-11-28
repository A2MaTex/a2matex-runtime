package constants

const (
	APIErrorCodeAuthorizationInvalid   = "AUTHORIZATION_INVALID"
	APIErrorCodeAuthorizationExpired   = "AUTHORIZATION_EXPIRED"
	ProductAuthorizationInvalidMessage = "Product authorization could not be accepted."
	ProductAuthorizationExpiredMessage = "Product authorization has expired."
	ProductSessionUnavailableMessage   = "Product authorization session is unavailable."
	ProductPackageInvalidMessage       = "Product package could not be used."
	ProductInitialGeneration           = uint64(1)
	ExecutionKindDevelopment           = "DEVELOPMENT"
	ExecutionKindProduct               = "PRODUCT"
	ProductFailureMessage              = "Product execution failed."
)
