package constants

import "time"

const (
	SSEHeartbeatInterval = 15 * time.Second
	SSEWriteTimeout      = 5 * time.Second
)

const (
	EditorGraphMaxContentBytes                  = 1_048_576
	LocalAPIWorkflowValidationEnvelopeAllowance = 4 * 1024
	LocalAPIWorkflowValidationBodyMaxBytes      = EditorGraphMaxContentBytes + LocalAPIWorkflowValidationEnvelopeAllowance

	LocalAPIWorkflowCompilationBodyMaxBytes = 2 * 1024 * 1024
	RuntimeIRMaxContentBytes                = 1024 * 1024
	RuntimeIRFormatVersion                  = 1
	CacheControlNoStore                     = "no-store"
	RunActionMaxContentBytes                = 1024

	RunActionPause  = "PAUSE"
	RunActionResume = "RESUME"
	RunActionCancel = "CANCEL"

	APIErrorCodeInvalidRequest        = "INVALID_REQUEST"
	APIErrorCodeUnauthorized          = "UNAUTHORIZED"
	APIErrorCodeResourceNotFound      = "RESOURCE_NOT_FOUND"
	APIErrorCodePayloadTooLarge       = "PAYLOAD_TOO_LARGE"
	APIErrorCodeInternal              = InternalError
	APIErrorCodeADBUnavailable        = ADBUnavailable
	APIErrorCodeRuntimeBusy           = "RUNTIME_BUSY"
	APIErrorCodeInvalidExecutionState = "INVALID_EXECUTION_STATE"
	APIErrorCodeEventCursorExpired    = "EVENT_CURSOR_EXPIRED"

	APIErrorMessageInvalidRequest        = "request is invalid"
	APIErrorMessageUnauthorized          = "local session token is missing or invalid"
	APIErrorMessageResourceNotFound      = "resource was not found"
	APIErrorMessagePayloadTooLarge       = "request payload is too large"
	APIErrorMessageInternal              = "internal server error"
	APIErrorMessageADBUnavailable        = "unable to refresh Android devices"
	APIErrorMessageSnapshotReadFailed    = "unable to read execution snapshot"
	APIErrorMessageAttemptReadFailed     = "unable to read node attempt history"
	APIErrorMessageArtifactReadFailed    = "unable to read screenshot artifact"
	APIErrorMessageEventReadFailed       = "unable to read execution events"
	APIErrorMessageEventCursorExpired    = "event history has expired; refresh the execution snapshot"
	APIErrorMessageInvalidExecutionState = "action is not valid for the current execution state"
	APIErrorMessageActionFailed          = "unable to process workflow action"

	APIDeviceStatusReady       = "READY"
	APIDeviceStatusUnavailable = "UNAVAILABLE"

	APIErrorMessageAdmissionFailed            = "run admission failed"
	APIErrorMessageRuntimeBusy                = "another workflow run is active"
	APIErrorMessageRuntimeStopping            = "runtime is stopping"
	APIErrorMessageAdmissionADBUnavailable    = "unable to check selected Android device"
	APIErrorMessageAdmissionPersistenceFailed = "unable to persist workflow run"
)
