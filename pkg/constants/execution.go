package constants

import "time"

const (
	MaxAdmissionDevices         = 32
	MaxAttemptPageSize          = 100
	MaxEventPageSize            = 100
	MaxEventBatchSize           = 100
	MaxEventBytes               = 16 * 1024
	EventRetention              = 10_000
	EventPruneBatchSize         = 512
	ExecutionTimestampLayout    = "2006-01-02T15:04:05.000Z"
	MaxNodeAttemptNumber        = 3
	MaxRetryDelayMS             = 60_000
	MaxRuntimeErrorMessageChars = 500
	CheckpointTimeout           = 5 * time.Second
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
