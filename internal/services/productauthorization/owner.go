// Package productauthorization coordinates internal Product authorization policy.
// The protected execution owner and dispatch adapters are installed by J11.
package productauthorization

import (
	"context"
	"reflect"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Decision struct {
	Kind       constants.ProductAttemptDecision `json:"-"`
	Generation uint64                           `json:"-"`
	Revision   uint64                           `json:"-"`
	issuer     *Controller
}

type Snapshot struct {
	Phase            constants.ProductAuthorizationPhase `json:"-"`
	Generation       uint64                              `json:"-"`
	Revision         uint64                              `json:"-"`
	BudgetBlocked    bool                                `json:"-"`
	RenewalReason    constants.ProductRenewalReason      `json:"-"`
	ExecutionID      string                              `json:"-"`
	RuntimeSessionID string                              `json:"-"`
	LeaseFamilyID    string                              `json:"-"`
	Sequence         int64                               `json:"-"`
	RefreshAt        time.Time                           `json:"-"`
	ExpiresAt        time.Time                           `json:"-"`
}

// Quiescence attests that every unfinished member and original borrower has
// joined. Completeness is the trusted owner's responsibility; SQLite verifies
// the exact boundary evidence. Terminal delegates cleanup to the terminal owner.
type Quiescence struct {
	ExecutionID    string               `json:"-"`
	Generation     uint64               `json:"-"`
	Members        []execution.Boundary `json:"-"`
	PauseConfirmed bool                 `json:"-"`
	Terminal       bool                 `json:"-"`
}

type Owner interface {
	Quiesce(context.Context, string, uint64, constants.ProductQuiesceMode) (Quiescence, error)
	ReleaseDefinition(context.Context, string, uint64) error
	Fail(error)
}

type PauseWriter interface {
	Pause(context.Context, execution.GroupControlRequest) (execution.GroupControlResult, error)
}

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Options struct{ Clock Clock }

type realClock struct{}
type realTimer struct{ *time.Timer }

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
func (t realTimer) C() <-chan time.Time          { return t.Timer.C }

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan:
		return reflected.IsNil()
	}
	return false
}

func configuration(operation, reason string) error {
	return &xerror.OperationError{Operation: operation, Detail: reason, Err: xerror.ErrProductAuthorizationConfiguration}
}

func validateReport(report Quiescence, state Snapshot) error {
	if report.ExecutionID != state.ExecutionID || report.Generation != state.Generation ||
		(report.Terminal && (len(report.Members) != 0 || report.PauseConfirmed)) ||
		(!report.Terminal && len(report.Members) == 0) {
		return configuration("quiesce_product_execution", "report_stale")
	}
	seen := make(map[string]struct{}, len(report.Members))
	for _, member := range report.Members {
		_, duplicate := seen[member.DeviceExecutionID]
		statusOK := member.Status == constants.ExecutionQueued || member.Status == constants.ExecutionRunning ||
			(report.PauseConfirmed && member.Status == constants.ExecutionPaused)
		if duplicate || !statusOK || !execution.ValidID(member.DeviceExecutionID) || !execution.ValidNodeID(member.NextNodeID) ||
			member.Sequence < 0 || (member.Sequence == 0) != (member.AttemptID == "") ||
			(member.AttemptID != "" && !execution.ValidID(member.AttemptID)) {
			return configuration("quiesce_product_execution", "boundary_invalid")
		}
		seen[member.DeviceExecutionID] = struct{}{}
	}
	return nil
}
