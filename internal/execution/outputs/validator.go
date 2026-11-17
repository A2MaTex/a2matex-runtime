// Package outputs checks handler results before success can be committed.
package outputs

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Validator struct {
	definitions *nodecontract.Registry
}

func New(definitions *nodecontract.Registry) (*Validator, error) {
	if definitions == nil {
		return nil, xerror.WithOperation("configure_output_validation", xerror.ErrOutputValidationDependencies)
	}
	return &Validator{definitions: definitions}, nil
}

// Validate returns the owned JSON snapshot checked against the output contract.
// Callers must not mutate handler output during this call or re-encode it for commit.
func (v *Validator) Validate(ctx context.Context, nodeType string, output map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("validate_node_output", err)
	}
	fail := func(code, reason, message string) (json.RawMessage, error) {
		return nil, outputError(ctx, code, reason, message)
	}
	if _, known := v.definitions.CompilationDefinition(nodeType); !known {
		return fail(constants.InternalError, "output_definition_unavailable", "Node output definition is unavailable")
	}
	if output == nil {
		return fail(constants.NodeOutputInvalid, "output_missing", "Node output must be a JSON object")
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		// Marshaler errors may contain protected data; retain only the safe classification.
		return fail(constants.NodeOutputInvalid, "output_not_serializable", "Node output cannot be encoded as JSON")
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("validate_node_output", err)
	}
	if len(encoded) > constants.MaxEventBytes {
		return fail(constants.NodeOutputInvalid, "output_too_large", "Node output exceeds the execution event limit")
	}
	var snapshot ir.Object
	if json.Unmarshal(encoded, &snapshot) != nil || !v.definitions.ValidOutput(nodeType, snapshot) {
		return fail(constants.NodeOutputInvalid, "output_contract_invalid", "Node output does not satisfy its installed contract")
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("validate_node_output", err)
	}
	return encoded, nil
}

func outputError(ctx context.Context, code, reason, message string) error {
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("validate_node_output", err)
	}
	return &xerror.OperationError{
		Operation: "validate_node_output",
		Detail:    reason,
		Err:       xerror.CommandError{Code: code, Message: message},
	}
}
