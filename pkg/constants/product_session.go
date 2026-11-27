package constants

import "time"

type ProductSessionPurpose string
type ProductSessionState string

const (
	ProductSessionStart       ProductSessionPurpose = "START"
	ProductSessionReauthorize ProductSessionPurpose = "REAUTHORIZE"
	ProductSessionPreparing   ProductSessionState   = "PREPARING"
	ProductSessionPending     ProductSessionState   = "PENDING"
	ProductSessionConsumed    ProductSessionState   = "CONSUMED"
	ProductSessionUnavailable ProductSessionState   = "UNAVAILABLE"
)

const (
	ProductSessionPendingLimit     = 4
	ProductSessionReceiptLimit     = 256
	ProductSessionPreparationLimit = 1
	ProductSessionPendingLifetime  = 300 * time.Second
	ProductSessionReceiptLifetime  = 300 * time.Second
	ProductSessionSweepInterval    = time.Second
	ProductSessionRSAKeyBits       = 3072
	ProductSessionRSAExponent      = 65537
)
