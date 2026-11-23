package constants

import "time"

type ExecutionStopReason string

const (
	ExecutionStopIntentional ExecutionStopReason = "intentional"
	ExecutionStopOwnerLost   ExecutionStopReason = "owner_lost"
	ExecutionStopFailure     ExecutionStopReason = "failure"
)

const (
	InternalError     = "INTERNAL_ERROR"
	NodeOutputInvalid = "NODE_OUTPUT_INVALID"
	NodeTimedOut      = "NODE_TIMED_OUT"
)

const (
	NodeTimedOutMessage         = "The node exceeded its execution time limit."
	PersistenceFailedMessage    = "The execution result could not be saved safely."
	OutcomeInternalErrorMessage = "The runtime could not process the node result safely."
)

const (
	DeviceInsertBatchSize           = 100
	MaxAttemptPageSize              = 100
	MaxEventPageSize                = 100
	MaxEventBatchSize               = 100
	MaxEventBytes                   = 16 * 1024
	EventRetention                  = 10_000
	EventPruneBatchSize             = 512
	EventObservationRecheckInterval = time.Second
	ExecutionTimestampLayout        = "2006-01-02T15:04:05.000Z"
	MaxNodeAttemptNumber            = 3
	MaxFlowDelayMS                  = 300_000
	MaxRetryDelayMS                 = 60_000
	MaxRuntimeErrorMessageChars     = 500
	CheckpointTimeout               = 5 * time.Second
)

const (
	ExecutionQueued             = "QUEUED"
	ExecutionRunning            = "RUNNING"
	ExecutionPaused             = "PAUSED"
	ExecutionSucceeded          = "SUCCEEDED"
	ExecutionFailed             = "FAILED"
	ExecutionTimedOut           = "TIMED_OUT"
	ExecutionCancelled          = "CANCELLED"
	ExecutionInterrupted        = "INTERRUPTED"
	ExecutionInterruptedCode    = "EXECUTION_INTERRUPTED"
	ExecutionInterruptedMessage = "Execution was interrupted by a runtime restart."
)

type CheckpointDisposition string

const (
	CheckpointCommitted        CheckpointDisposition = "COMMITTED"
	CheckpointAlreadyFinalized CheckpointDisposition = "ALREADY_FINALIZED"
	CheckpointStale            CheckpointDisposition = "STALE"
)

type AdmissionDisposition string

type GroupControlDisposition string

const (
	GroupControlAccepted      GroupControlDisposition = "ACCEPTED"
	GroupControlCommitted     GroupControlDisposition = "COMMITTED"
	GroupControlAlreadyTarget GroupControlDisposition = "ALREADY_TARGET"
	GroupControlStale         GroupControlDisposition = "STALE"
)

const (
	AdmissionCreated  AdmissionDisposition = "CREATED"
	AdmissionExisting AdmissionDisposition = "EXISTING"
	AdmissionBusy     AdmissionDisposition = "BUSY"
)

const (
	EventRunGroupStateChanged = "RUN_GROUP_STATE_CHANGED"
	EventDeviceStateChanged   = "DEVICE_EXECUTION_STATE_CHANGED"
	EventAttemptStateChanged  = "NODE_ATTEMPT_STATE_CHANGED"
	EventRetryScheduled       = "NODE_RETRY_SCHEDULED"
)
