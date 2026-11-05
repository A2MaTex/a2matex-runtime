package nodecontract

import (
	"encoding/json"
	"regexp"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

const switchType = "flow.switch"
const maxSwitchRules = 32

var switchRuleID = regexp.MustCompile(`^rule_[A-Za-z0-9_]{1,64}$`)

func switchRules(node editorgraph.Node) map[string]string {
	var rows []editorgraph.JSONObject
	if len(node.Config) != 1 || json.Unmarshal(node.Config["rules"], &rows) != nil ||
		len(rows) == 0 || len(rows) > maxSwitchRules {
		return nil
	}
	rules := make(map[string]string, len(rows))
	for _, row := range rows {
		id, operator := bindingString(row["id"]), bindingString(row["operator"])
		if len(row) != 2 || !switchRuleID.MatchString(id) || rules[id] != "" {
			return nil
		}
		switch operator {
		case "EQ", "NE", "LT", "LTE", "GT", "GTE", "IS_EMPTY":
			rules[id] = operator
		default:
			return nil
		}
	}
	return rules
}

func validateSwitches(graph *editorgraph.Graph, registry *Registry) []validation.Diagnostic {
	if _, ok := registry.lookup(switchType); !ok {
		return nil
	}
	type instance struct {
		index int
		ports map[string]int
	}
	instances := make(map[string]instance)
	var diagnostics []validation.Diagnostic
	for index, node := range graph.Nodes {
		if node.Type != switchType {
			continue
		}
		rules := switchRules(node)
		if rules == nil {
			diagnostics = append(diagnostics, configDiagnostic(node.ID, index, "rules"))
			continue
		}
		ports := map[string]int{"default": 0}
		for id := range rules {
			ports[id] = 0
		}
		if node.ErrorPolicy == editorgraph.ErrorPolicyErrorBranch {
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
			diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeEdgeSourcePortNotFound,
				validation.Path{"edges", index, "sourcePort"}, edge.SourceNodeID, edge.ID))
			continue
		}
		instance.ports[edge.SourcePort]++
	}
	for nodeID, instance := range instances {
		for port, count := range instance.ports {
			if count != 1 {
				diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeSwitchPortEdgeCountInvalid,
					validation.Path{"nodes", instance.index, "ports", port}, nodeID, ""))
			}
		}
	}
	return diagnostics
}
