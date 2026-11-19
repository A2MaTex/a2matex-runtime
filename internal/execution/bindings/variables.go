package bindings

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type VariableLookup func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error)

// VariableView is trusted run-owner wiring, copied by value for one device.
// Lookup must capture that run's G16 store and the same DeviceExecutionID.
type VariableView struct {
	RunGroupID        string
	DeviceExecutionID string
	Lookup            VariableLookup
}

// ValidFor allows an absent view and checks supplied identities without reading.
// Lookup availability is checked lazily when a variable binding is reached.
func (v VariableView) ValidFor(groupID, deviceID string) bool {
	if v.RunGroupID == "" && v.DeviceExecutionID == "" && v.Lookup == nil {
		return true
	}
	return v.validIdentity() && v.RunGroupID == groupID && v.DeviceExecutionID == deviceID
}

func (v VariableView) validIdentity() bool {
	return execution.ValidID(v.RunGroupID) && execution.ValidID(v.DeviceExecutionID)
}

func resolveVariable(ctx context.Context, source ir.Object, kind, field string, view VariableView) (json.RawMessage, error) {
	const operation = "resolve_input_binding"
	var name string
	if len(source) != 2 || json.Unmarshal(source["name"], &name) != nil || !ir.ValidVariableName(name) {
		return nil, bindingError(ctx, operation, field, "binding_invalid", "Input binding does not match its contract")
	}
	if !view.validIdentity() || view.Lookup == nil {
		return nil, &xerror.InputBindingError{Field: field, Err: &xerror.OperationError{
			Operation: operation, Detail: "variable_context_unavailable", Err: xerror.ErrVariableInvalid,
		}}
	}
	scope := constants.VariableScopeFlow
	if kind == constants.InputSourceGlobalVariable {
		scope = constants.VariableScopeRunGroup
	}
	value, found, err := view.Lookup(ctx, scope, name)
	if err != nil {
		return nil, &xerror.InputBindingError{Field: field, Err: xerror.WithOperation("read_variable_binding", err)}
	}
	if !found {
		return nil, bindingError(ctx, operation, field, "variable_missing", "Variable value is unavailable")
	}
	return value, nil
}
