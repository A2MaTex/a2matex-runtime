package nodecontract

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const switchType = "flow.switch"
const maxSwitchRules = constants.MaxSwitchRules

func ValidRuleID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return false
	}
	if id == uuid.Nil || strings.EqualFold(value, "ffffffff-ffff-ffff-ffff-ffffffffffff") {
		return true
	}
	return id.Variant() == uuid.RFC4122 && id.Version() >= 1 && id.Version() <= 8
}

func operatorType(operator string) FieldType {
	switch operator {
	case "EQ", "NE":
		return ""
	case "LT", "LTE", "GT", "GTE":
		return FieldTypeNumber
	case "CONTAINS", "NOT_CONTAINS", "STARTS_WITH", "ENDS_WITH", "IS_EMPTY", "IS_NOT_EMPTY":
		return FieldTypeString
	case "IS_TRUE", "IS_FALSE":
		return FieldTypeBoolean
	default:
		return "invalid"
	}
}

func needsOperand(operator string) bool {
	return operator != "IS_EMPTY" && operator != "IS_NOT_EMPTY" && operator != "IS_TRUE" && operator != "IS_FALSE"
}

func switchRules(node nodeView) map[string]string {
	var rows []ir.Object
	if len(node.Config) != 1 || json.Unmarshal(node.Config["rules"], &rows) != nil ||
		len(rows) == 0 || len(rows) > maxSwitchRules {
		return nil
	}
	rules := make(map[string]string, len(rows))
	for _, row := range rows {
		id, operator := bindingString(row["id"]), bindingString(row["operator"])
		if len(row) != 2 || !ValidRuleID(id) || rules[id] != "" {
			return nil
		}
		if operatorType(operator) == "invalid" {
			return nil
		}
		rules[id] = operator
	}
	return rules
}

func validateSwitches(graph *graphView, registry *Registry, report func(validation.Diagnostic)) error {
	if _, ok := registry.lookup(switchType); !ok {
		return nil
	}
	type instance struct {
		index int
		ports map[string]int
	}
	instances := make(map[string]instance)
	for index, node := range graph.Nodes {
		if err := graph.ctx.Err(); err != nil {
			return err
		}
		if node.Type != switchType {
			continue
		}
		rules := switchRules(node)
		if rules == nil {
			report(node.configDiagnostic(index, "rules"))
			continue
		}
		ports := map[string]int{"default": 0}
		for id := range rules {
			ports[id] = 0
		}
		if node.ErrorPolicy == constants.ErrorPolicyErrorBranch {
			ports["error"] = 0
		}
		instances[node.ID] = instance{index: index, ports: ports}
	}
	for index, edge := range graph.Edges {
		instance, ok := instances[edge.SourceNodeID]
		if !ok {
			continue
		}
		if _, exists := instance.ports[edge.SourcePort]; !exists {
			report(validation.NewDiagnostic(validation.CodeEdgeSourcePortNotFound,
				graph.edgePath(index, "sourcePort"), edge.SourceNodeID, edge.ID))
			continue
		}
		instance.ports[edge.SourcePort]++
	}
	for nodeID, instance := range instances {
		for port, count := range instance.ports {
			if count != 1 {
				report(validation.NewDiagnostic(validation.CodeSwitchPortEdgeCountInvalid,
					graph.Nodes[instance.index].portPath(instance.index, port), nodeID, ""))
			}
		}
	}
	return graph.ctx.Err()
}
