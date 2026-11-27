package constants

import "time"

const ProductAttemptBudgetMargin = 2 * time.Second

type ProductAuthorizationPhase string
type ProductAttemptDecision string
type ProductRenewalReason string
type ProductQuiesceMode string

const (
	ProductAuthorizationActive          ProductAuthorizationPhase = "ACTIVE"
	ProductAuthorizationRenewalRequired ProductAuthorizationPhase = "RENEWAL_REQUIRED"
	ProductAuthorizationExpiring        ProductAuthorizationPhase = "EXPIRING"
	ProductAuthorizationExpired         ProductAuthorizationPhase = "EXPIRED"
	ProductAuthorizationEnded           ProductAuthorizationPhase = "ENDED"

	ProductAttemptAllow   ProductAttemptDecision = "ALLOW"
	ProductAttemptHold    ProductAttemptDecision = "HOLD"
	ProductAttemptExpired ProductAttemptDecision = "EXPIRED"
	ProductAttemptEnded   ProductAttemptDecision = "ENDED"

	ProductRenewalScheduled     ProductRenewalReason = "SCHEDULED"
	ProductRenewalAttemptBudget ProductRenewalReason = "ATTEMPT_BUDGET"
	ProductQuiesceBudget        ProductQuiesceMode   = "BUDGET"
	ProductQuiesceExpiry        ProductQuiesceMode   = "EXPIRY"
)
