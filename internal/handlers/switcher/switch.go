// Package switcher evaluates ordered Switch rules without executing a device command.
package switcher

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/flow"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Invoke resolves only reached operands and proposes exactly one continuation.
func Invoke(ctx context.Context, call registry.Invocation) (map[string]any, error) {
	if err := flow.CheckAttempt(ctx); err != nil {
		return nil, xerror.WithOperation("evaluate_switch", err)
	}
	var rules []map[string]string
	if call.ResolveInput == nil || call.SelectPort == nil || len(call.Config) != 1 ||
		json.Unmarshal(call.Config["rules"], &rules) != nil || len(rules) < 1 || len(rules) > constants.MaxSwitchRules {
		return nil, invalid("switch_contract_invalid")
	}
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		_, duplicate := seen[rule["id"]]
		_, known := operandRequired(rule["operator"])
		if len(rule) != 2 || !nodecontract.ValidRuleID(rule["id"]) || duplicate || !known {
			return nil, invalid("switch_rule_invalid")
		}
		seen[rule["id"]] = struct{}{}
	}
	left, err := call.ResolveInput(ctx, "value")
	if err != nil {
		return nil, xerror.WithOperation("resolve_switch_value", err)
	}
	port := "default"
	for _, rule := range rules {
		if err := flow.CheckAttempt(ctx); err != nil {
			return nil, xerror.WithOperation("evaluate_switch", err)
		}
		var right json.RawMessage
		if binary, _ := operandRequired(rule["operator"]); binary {
			right, err = call.ResolveInput(ctx, rule["id"])
			if err != nil {
				return nil, xerror.WithOperation("resolve_switch_operand", err)
			}
		}
		matched, err := compare(rule["operator"], left, right)
		if err != nil {
			return nil, &xerror.InputBindingError{Field: rule["id"], Err: err}
		}
		if matched {
			port = rule["id"]
			break
		}
	}
	if err := flow.CheckAttempt(ctx); err != nil {
		return nil, xerror.WithOperation("evaluate_switch", err)
	}
	if err := call.SelectPort(ctx, port); err != nil {
		return nil, xerror.WithOperation("select_switch_port", err)
	}
	return map[string]any{}, nil
}

func operandRequired(operator string) (binary, known bool) {
	switch operator {
	case "IS_EMPTY", "IS_NOT_EMPTY", "IS_TRUE", "IS_FALSE":
		return false, true
	case "EQ", "NE", "LT", "LTE", "GT", "GTE", "CONTAINS", "NOT_CONTAINS", "STARTS_WITH", "ENDS_WITH":
		return true, true
	}
	return false, false
}

func compare(operator string, left, right json.RawMessage) (bool, error) {
	leftType, valid := nodecontract.ScalarType(left)
	binary, known := operandRequired(operator)
	if !known {
		return false, invalid("switch_operator_invalid")
	}
	rightType, validRight := nodecontract.ScalarType(right)
	if !valid || (binary && (!validRight || leftType != rightType)) {
		return false, typeError()
	}
	var l, r any
	if err := json.Unmarshal(left, &l); err != nil {
		return false, typeError()
	}
	if binary {
		if err := json.Unmarshal(right, &r); err != nil {
			return false, typeError()
		}
	}
	if operator == "EQ" || operator == "NE" {
		return (l == r) == (operator == "EQ"), nil
	}
	switch l := l.(type) {
	case float64:
		if !binary {
			break
		}
		r := r.(float64) // Both finite scalar types were checked above.
		switch operator {
		case "LT":
			return l < r, nil
		case "LTE":
			return l <= r, nil
		case "GT":
			return l > r, nil
		case "GTE":
			return l >= r, nil
		}
	case string:
		if operator == "IS_EMPTY" || operator == "IS_NOT_EMPTY" {
			return (l == "") == (operator == "IS_EMPTY"), nil
		}
		if !binary {
			break
		}
		r := r.(string)
		switch operator {
		case "CONTAINS":
			return strings.Contains(l, r), nil
		case "NOT_CONTAINS":
			return !strings.Contains(l, r), nil
		case "STARTS_WITH":
			return strings.HasPrefix(l, r), nil
		case "ENDS_WITH":
			return strings.HasSuffix(l, r), nil
		}
	case bool:
		if operator == "IS_TRUE" || operator == "IS_FALSE" {
			return l == (operator == "IS_TRUE"), nil
		}
	}
	return false, typeError()
}

func typeError() error {
	return xerror.WithOperation("compare_switch_operands", xerror.CommandError{
		Code: constants.CommandInputUnsupported, Message: "Switch operands do not satisfy the comparison type",
	})
}

func invalid(reason string) error {
	return &xerror.OperationError{Operation: "evaluate_switch", Detail: reason, Err: xerror.ErrInvocationInvalid}
}
