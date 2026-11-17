package runtimeir_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
)

func TestSwitchContracts(t *testing.T) {
	v, calls := admission(t, "0.1.0")
	for _, operator := range []string{"EQ", "NE", "LT", "LTE", "GT", "GTE", "CONTAINS", "NOT_CONTAINS", "STARTS_WITH", "ENDS_WITH", "IS_EMPTY", "IS_NOT_EMPTY", "IS_TRUE", "IS_FALSE"} {
		t.Run(operator, func(t *testing.T) {
			var value any = 1.25
			var operand any = 2
			unary := false
			switch operator {
			case "CONTAINS", "NOT_CONTAINS", "STARTS_WITH", "ENDS_WITH", "IS_EMPTY", "IS_NOT_EMPTY":
				value = "hello"
				operand = "lo"
			case "IS_TRUE", "IS_FALSE":
				value = true
			}
			switch operator {
			case "IS_EMPTY", "IS_NOT_EMPTY", "IS_TRUE", "IS_FALSE":
				unary = true
			}
			inputs := ir.Object{"value": literal(value)}
			if !unary {
				inputs[ruleID] = literal(operand)
			}
			d := target(t, "flow.switch", ir.Object{"rules": raw([]map[string]string{{"id": ruleID, "operator": operator}})}, inputs)
			d.Edges[1].SourcePort = ruleID
			d.Edges = append(d.Edges, ir.Edge{ID: "default", SourceNodeID: "wait", SourcePort: "default", TargetNodeID: "end"})
			wantValid(t, check(t, v, raw(d)))
			if unary {
				d.Nodes[1].Inputs[ruleID] = literal(operand)
			} else {
				delete(d.Nodes[1].Inputs, ruleID)
			}
			wantCode(t, check(t, v, raw(d)), validation.CodeNodeValueBindingInvalid)
		})
	}
	d := target(t, "flow.switch", ir.Object{"rules": raw([]map[string]string{{"id": "rule_old", "operator": "EQ"}})}, ir.Object{"value": literal(1)})
	wantCode(t, check(t, v, raw(d)), validation.CodeNodeConfigInvalid)
	d.Nodes[1].Config["rules"] = raw([]map[string]string{{"id": ruleID, "operator": "EQ"}})
	d.Nodes[1].Inputs[ruleID] = literal(1)
	d.Edges[1].SourcePort = ruleID
	wantCode(t, check(t, v, raw(d)), validation.CodeSwitchPortEdgeCountInvalid)
	if calls.Load() != 0 {
		t.Fatal("Switch executed")
	}
}

func TestCorrectedContractsCompileThenAdmit(t *testing.T) {
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	compiler := validationservice.New(catalog)
	v, _ := admission(t, "0.1.0")
	for _, kind := range []string{"flow.set_variable", "flow.switch"} {
		d := target(t, kind, ir.Object{"name": raw("price"), "scope": raw("FLOW")}, ir.Object{"value": literal(1.25)})
		if kind == "flow.switch" {
			d.Nodes[1].Config = ir.Object{"rules": raw([]map[string]string{{"id": ruleID, "operator": "IS_TRUE"}})}
			d.Nodes[1].Inputs["value"] = literal(true)
			d.Edges[1].SourcePort = ruleID
			d.Edges = append(d.Edges, ir.Edge{ID: "default", SourceNodeID: "wait", SourcePort: "default", TargetNodeID: "end"})
		} else {
			d.InputSchema = raw(map[string]any{"type": "object", "properties": map[string]any{"price": map[string]any{"type": "number"}}, "required": []string{"price"}, "additionalProperties": false})
			d.Nodes[1].Inputs["value"] = source("WORKFLOW_INPUT", map[string]any{"inputName": "price"})
		}
		var nodes []map[string]any
		for _, n := range d.Nodes {
			bindings := ir.Object{}
			for field, value := range n.Inputs {
				var binding ir.Object
				if err := json.Unmarshal(value, &binding); err != nil {
					t.Fatal(err)
				}
				binding["kind"] = binding["source"]
				delete(binding, "source")
				bindings[field] = raw(binding)
			}
			nodes = append(nodes, map[string]any{"id": n.ID, "type": n.Type, "config": n.Config, "bindings": bindings, "editor": map[string]int{"x": 0, "y": 0}, "errorPolicy": n.ErrorPolicy})
		}
		graph := raw(map[string]any{"schemaVersion": 1, "entryNodeId": d.EntryNodeID, "nodes": nodes, "edges": d.Edges})
		compiled, err := compiler.Compile(graph, d.InputSchema)
		if err != nil || compiled.Validation.Status != validation.StatusValid {
			t.Fatalf("%s compile: %+v %v", kind, compiled, err)
		}
		input := json.RawMessage("{}")
		if kind == "flow.set_variable" {
			input = json.RawMessage(`{"price":1.25}`)
		}
		wantValid(t, check(t, v, compiled.RuntimeIR, input))
	}
}

func TestCapabilityDeclarationOrder(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	d := target(t, "android.screenshot", ir.Object{"targetMode": raw("FULL_SCREEN")}, ir.Object{})
	d.Nodes[2].Type = "android.press_home"
	d.Nodes = append(d.Nodes, base(t).Nodes[2])
	d.Nodes[2].ID = "home"
	d.Edges[1].TargetNodeID = "home"
	d.Edges = append(d.Edges, ir.Edge{ID: "e3", SourceNodeID: "home", SourcePort: "success", TargetNodeID: "end"})
	derive(t, d)
	wantValid(t, check(t, v, raw(d)))
	for _, caps := range [][]string{{"android.device.control", "android.screen.capture"}, {"android.screen.capture"}, {"android.screen.capture", "android.device.control", "android.device.control"}} {
		d.RequiredCapabilities = caps
		wantCode(t, check(t, v, raw(d)), validation.CodeRuntimeIRCapabilitiesInvalid)
	}
}

func TestProducerSuccessDominance(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	d := target(t, "android.screenshot", ir.Object{"targetMode": raw("FULL_SCREEN")}, ir.Object{})
	consumer := base(t).Nodes[1]
	consumer.ID = "consumer"
	consumer.Type = "android.input_text"
	consumer.Inputs = ir.Object{"text": source("NODE_OUTPUT", map[string]any{"nodeId": "wait", "field": "artifactId"})}
	d.Nodes = append(d.Nodes[:2], consumer, d.Nodes[2])
	d.Edges[1].TargetNodeID = "consumer"
	d.Edges = append(d.Edges, ir.Edge{ID: "e3", SourceNodeID: "consumer", SourcePort: "success", TargetNodeID: "end"})
	derive(t, d)
	wantValid(t, check(t, v, raw(d)))
	for _, tc := range []struct {
		name   string
		mutate func(*ir.Document)
	}{
		{"unknown output", func(d *ir.Document) {
			d.Nodes[2].Inputs["text"] = source("NODE_OUTPUT", map[string]any{"nodeId": "wait", "field": "missing"})
		}},
		{"incompatible", func(d *ir.Document) {
			d.Nodes[2].Type = "flow.delay"
			d.Nodes[2].Inputs = ir.Object{"durationMs": source("NODE_OUTPUT", map[string]any{"nodeId": "wait", "field": "artifactId"})}
			derive(t, d)
		}},
		{"self reference", func(d *ir.Document) {
			d.Nodes[2].Inputs["text"] = source("NODE_OUTPUT", map[string]any{"nodeId": "consumer", "field": "text"})
		}},
		{"error bypass", func(d *ir.Document) {
			d.Nodes[1].ErrorPolicy = "ERROR_BRANCH"
			d.Edges = append(d.Edges, ir.Edge{ID: "error", SourceNodeID: "wait", SourcePort: "error", TargetNodeID: "consumer"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *d
			copy.Nodes = append([]ir.Node{}, d.Nodes...)
			copy.Nodes[2].Inputs = ir.Object{"text": source("NODE_OUTPUT", map[string]any{"nodeId": "wait", "field": "artifactId"})}
			copy.Edges = append([]ir.Edge{}, d.Edges...)
			tc.mutate(&copy)
			wantCode(t, check(t, v, raw(copy)), validation.CodeNodeValueBindingInvalid)
		})
	}
	for _, scope := range []string{"FLOW_VARIABLE", "GLOBAL_VARIABLE"} {
		d.Nodes[2].Inputs["text"] = source(scope, map[string]any{"name": "notWrittenYet"})
		wantValid(t, check(t, v, raw(d)))
	}
}

func TestGraphAndMapCountBoundaries(t *testing.T) {
	v, _ := admission(t, "0.1.0")
	for _, count := range []int{500, 501} {
		t.Run(fmt.Sprintf("nodes_%d", count), func(t *testing.T) {
			d := base(t)
			middle := d.Nodes[1]
			d.Nodes = d.Nodes[:1]
			d.Edges = nil
			for i := 0; i < count-2; i++ {
				n := middle
				n.ID = fmt.Sprintf("wait%d", i)
				d.Nodes = append(d.Nodes, n)
			}
			d.Nodes = append(d.Nodes, base(t).Nodes[2])
			for i := 1; i < len(d.Nodes); i++ {
				d.Edges = append(d.Edges, ir.Edge{ID: fmt.Sprintf("e%d", i), SourceNodeID: d.Nodes[i-1].ID, SourcePort: "success", TargetNodeID: d.Nodes[i].ID})
			}
			r := check(t, v, raw(d))
			if count == 500 {
				wantValid(t, r)
			} else {
				wantCode(t, r, validation.CodeGraphNodeLimitExceeded)
				if len(r.Validation.Errors) != 1 {
					t.Fatalf("did not stop before traversal: %+v", r)
				}
			}
		})
	}
	for _, count := range []int{1000, 1001} {
		d := base(t)
		d.Edges = d.Edges[:1]
		for i := 1; i < count; i++ {
			d.Edges = append(d.Edges, ir.Edge{ID: fmt.Sprintf("e%d", i+1), SourceNodeID: "wait", SourcePort: "success", TargetNodeID: "end"})
		}
		r := check(t, v, raw(d))
		if count == 1001 {
			wantCode(t, r, validation.CodeGraphEdgeLimitExceeded)
		} else {
			wantCode(t, r, validation.CodeNodePortEdgeCountInvalid)
			for _, f := range r.Validation.Errors {
				if f.Code == validation.CodeGraphEdgeLimitExceeded {
					t.Fatal("rejected allowed edge count")
				}
			}
		}
	}
	for _, field := range []string{"config", "inputs"} {
		for _, count := range []int{100, 101} {
			d := base(t)
			values := ir.Object{}
			for i := 0; i < count; i++ {
				if field == "inputs" {
					values[fmt.Sprintf("field%d", i)] = literal(1)
				} else {
					values[fmt.Sprintf("field%d", i)] = raw(1)
				}
			}
			if field == "inputs" {
				d.Nodes[1].Inputs = values
			} else {
				d.Nodes[1].Config = values
			}
			r := check(t, v, raw(d))
			contract := false
			for _, f := range r.Validation.Errors {
				contract = contract || f.Code == validation.CodeRuntimeIRContractInvalid
			}
			if contract != (count == 101) {
				t.Fatalf("%s %d boundary: %+v", field, count, r)
			}
		}
	}
}
