// Package runadmission coordinates checked admission and process-owned workflow starts.
package runadmission

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type IRValidator interface {
	Validate(context.Context, json.RawMessage, []json.RawMessage) (runtimeir.Result, error)
}
type DevicePreflight interface {
	Check(context.Context, []string) (services.DeviceSelectionResult, error)
}
type Repository interface {
	LookupRequest(context.Context, string) (string, error)
	Admit(context.Context, execution.Admission) (execution.AdmissionResult, error)
	SnapshotByID(context.Context, string) (execution.Snapshot, error)
}
type AdmissionGate interface {
	BeginAdmission(context.Context) (func(), error)
}
type Dependencies struct {
	Validator  IRValidator
	Preflight  DevicePreflight
	Repository Repository
	Lifecycle  AdmissionGate
	Runner     runner.Dependencies
}
type DeviceInput struct {
	ADBSerial string
	Inputs    json.RawMessage
}
type Request struct {
	RequestID string
	RuntimeIR json.RawMessage
	Devices   []DeviceInput
}

// PreparedRun transfers immutable checked content and ordered identities to one owner.
// It is returned only for a confirmed new admission and contains no request context.
type PreparedRun struct {
	Runner  *runner.Runner
	Devices []runner.Request
}
type Rejection struct {
	FailureCode string
	Validation  *validation.Result
	Unavailable []services.DeviceSelectionIssue
}

// Result has Work only for CREATED, Snapshot only for EXISTING, and Rejection
// only for expected validation/readiness rejection. Errors return a zero Result.
type Result struct {
	Disposition constants.AdmissionDisposition
	RunGroupID  string
	Snapshot    *execution.Snapshot
	Work        *PreparedRun
	Rejection   *Rejection
}
