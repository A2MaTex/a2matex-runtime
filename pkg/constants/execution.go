package constants

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
