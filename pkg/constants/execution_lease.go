package constants

import "time"

type LeaseVerificationMode string

const (
	LeaseVerificationInitial     LeaseVerificationMode = "INITIAL"
	LeaseVerificationRenew       LeaseVerificationMode = "RENEW"
	LeaseVerificationReauthorize LeaseVerificationMode = "REAUTHORIZE"

	ExecutionLeaseAlgorithm       = "EdDSA"
	ExecutionLeaseType            = "a2matex-execution-lease+jws"
	ExecutionLeaseMaxBytes        = 8192
	ExecutionLeaseSegments        = 3
	ExecutionLeaseKeyIDMaxBytes   = 64
	ExecutionLeaseHashBytes       = 64
	ExecutionLeaseHeaderFields    = 3
	ExecutionLeaseClaimFields     = 15
	ExecutionLeaseInitialSequence = 1
	ExecutionLeaseMaxSequence     = int64(2147483647)
	ExecutionLeaseMaxTimestamp    = int64(253402300799)
	ExecutionLeaseMaxSeconds      = int64(604800)
	ExecutionLeaseMaxClockSkew    = 30 * time.Second
)
