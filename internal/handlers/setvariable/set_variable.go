// Package setvariable validates assignments; the engine owns their publication.
package setvariable

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/flow"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Prepare copies a scalar candidate without assigning execution identity or writing.
func Prepare(config, inputs ir.Object) (execution.VariableWrite, error) {
	var write execution.VariableWrite
	if len(config) != 2 || json.Unmarshal(config["name"], &write.Name) != nil || !ir.ValidVariableName(write.Name) ||
		json.Unmarshal(config["scope"], &write.Scope) != nil ||
		(write.Scope != constants.VariableScopeFlow && write.Scope != constants.VariableScopeRunGroup) || len(inputs) != 1 {
		return write, xerror.WithOperation("prepare_variable_assignment", xerror.CommandError{
			Code: constants.CommandInputUnsupported, Message: "Set Variable requires a valid name, explicit scope, and one scalar value",
		})
	}
	value, err := execution.EncodeVariableScalar(inputs["value"])
	if err != nil {
		return execution.VariableWrite{}, xerror.WithOperation("prepare_variable_assignment", err)
	}
	write.Value = value
	return write, nil
}

func Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := flow.CheckAttempt(ctx); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	if _, err := Prepare(call.Config, call.Inputs); err != nil {
		return nil, err
	}
	if err := flow.CheckAttempt(ctx); err != nil {
		return nil, xerror.WithOperation("check_attempt", err)
	}
	return map[string]any{}, nil
}
